package supervisor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gur-shatz/go-run/internal/log"
)

// Options controls runtime behaviour of New + Run.
type Options struct {
	Verbose bool

	// LogPrefix overrides the default "[supervisor]" log prefix.
	LogPrefix string

	// PublicKey is the Ed25519 key used to verify update signatures. If nil,
	// New attempts to load it from cfg.Remote.SignaturePublicKeyPath. A missing
	// key is a fatal config error.
	PublicKey []byte

	// Build carries the binary's version/branch/commit metadata (set via
	// ldflags at the cmd level) so the backoffice can report it. Injected
	// rather than read here to keep the library decoupled from buildinfo.
	Build BuildInfo
}

// BuildInfo is the version metadata the supervisor reports at
// /backoffice/version and on the backoffice index. Empty fields are omitted.
type BuildInfo struct {
	Version string `json:"version,omitempty" yaml:"version,omitempty"`
	Commit  string `json:"commit,omitempty" yaml:"commit,omitempty"`
	Branch  string `json:"branch,omitempty" yaml:"branch,omitempty"`
	Date    string `json:"date,omitempty" yaml:"date,omitempty"`
}

// Supervisor manages N components against a shared state directory. One
// supervisor instance per state_dir/.
type Supervisor struct {
	cfg       Config
	paths     Paths
	logger    *log.Logger
	verbose   bool
	buildInfo BuildInfo

	startedAt  time.Time
	components []*Component
	httpServer *httpServer
	bundle     *statekitBundle
	scraper    *componentScraper
	observer   *observer // optional health-aggregation role; nil when disabled

	// Forced overrides snapshot, refreshed on every polling tick.
	forcedMu sync.RWMutex
	forced   ForcedOverrides

	lastPoll      atomic.Value // time.Time
	lastPollError atomic.Value // string
}

// New resolves the state directory, ensures per-component state directories
// exist, and returns a ready-to-Run Supervisor. The flock is the caller's
// responsibility — see AcquireLock.
func New(cfg Config, opts Options) (*Supervisor, error) {
	if cfg.StateDir == "" {
		return nil, fmt.Errorf("state_dir is required")
	}

	absStateDir, err := filepath.Abs(cfg.StateDir)
	if err != nil {
		return nil, fmt.Errorf("resolve state_dir %s: %w", cfg.StateDir, err)
	}
	cfg.StateDir = absStateDir

	if err := os.MkdirAll(absStateDir, 0755); err != nil {
		return nil, fmt.Errorf("create state_dir %s: %w", absStateDir, err)
	}

	paths := NewPaths(absStateDir)

	for _, c := range cfg.Components {
		if err := paths.Component(c.Name).EnsureDirs(); err != nil {
			return nil, fmt.Errorf("prepare component %q: %w", c.Name, err)
		}
		if _, err := CleanOrphanVersions(paths.Component(c.Name), cfg.VersionFolderRetention); err != nil {
			return nil, fmt.Errorf("gc orphan versions for %q: %w", c.Name, err)
		}
	}

	prefix := opts.LogPrefix
	if prefix == "" {
		prefix = "[supervisor]"
	}
	logger := log.New(prefix, opts.Verbose)

	// Resolve the signing public key.
	pub, err := resolvePublicKey(cfg, opts, logger)
	if err != nil {
		return nil, err
	}

	this := &Supervisor{
		cfg:       cfg,
		paths:     paths,
		logger:    logger,
		verbose:   opts.Verbose,
		buildInfo: opts.Build,
		startedAt: time.Now(),
		bundle:    newStatekitBundle(cfg),
	}
	this.lastPoll.Store(time.Time{})
	this.lastPollError.Store("")

	// Build one Component per config entry.
	for _, ccfg := range cfg.Components {
		client := NewRemoteClient(ccfg.Remote.Secret)
		install := &Installer{Remote: client, PublicKey: pub}
		comp := NewComponent(ccfg, paths.Component(ccfg.Name), install, cfg, this.makeForcedGetter(ccfg.Name), this.bundle, logger)
		this.components = append(this.components, comp)
	}

	// State monitoring. scrape (default on) polls each component into the
	// registry; observe (default off) persists that state and serves /health.
	// Both are independently toggleable; with scrape off the registry still
	// carries the per-component lifecycle state pushed by the supervisor.
	if cfg.StateMonitor.Scrape.IsEnabled() {
		sc, err := newComponentScraper(cfg, this.bundle, logger)
		if err != nil {
			return nil, err
		}
		this.scraper = sc
	}

	if cfg.StateMonitor.Observe.Enabled {
		this.observer = newObserver(cfg.StateMonitor.Observe, this.bundle.registry, logger)
	}

	// Wire the HTTP server (constructed but not started until Run).
	this.httpServer = newHTTPServer(cfg.Supervisor.BindAddress, this, this, this.paths, this.bundle, cfg.Components, this.observer, this.buildInfo, cfg.Supervisor.BasicAuth, logger)

	return this, nil
}

// Config returns the resolved configuration the supervisor is running with.
func (this *Supervisor) Config() Config { return this.cfg }

// Paths returns the path resolver rooted at state_dir.
func (this *Supervisor) Paths() Paths { return this.paths }

// Snapshot implements stateProvider for httpserver.
func (this *Supervisor) Snapshot() SupervisorSnapshot {
	snap := SupervisorSnapshot{
		StateDir:   this.cfg.StateDir,
		PublicPort: this.cfg.Supervisor.PublicPort,
		StartedAt:  this.startedAt.UTC().Format(time.RFC3339),
	}
	if t, ok := this.lastPoll.Load().(time.Time); ok && !t.IsZero() {
		snap.LastPollAt = t.UTC().Format(time.RFC3339)
	}
	if s, _ := this.lastPollError.Load().(string); s != "" {
		snap.LastPollError = s
	}
	this.forcedMu.RLock()
	if this.forced.HasAny() {
		snap.ForceOverride = make(map[string]string)
		if this.forced.wildcard.Kind != ForcedKindNone {
			snap.ForceOverride["*"] = describeForced(this.forced.wildcard)
		}
		for name, o := range this.forced.overrides {
			snap.ForceOverride[name] = describeForced(o)
		}
	}
	this.forcedMu.RUnlock()
	for _, c := range this.components {
		snap.Components = append(snap.Components, c.Snapshot())
	}
	sort.Slice(snap.Components, func(i, j int) bool { return snap.Components[i].Name < snap.Components[j].Name })
	return snap
}

// ComponentSnapshot implements stateProvider for httpserver.
func (this *Supervisor) ComponentSnapshot(name string) (ComponentSnapshot, bool) {
	for _, c := range this.components {
		if c.Name() == name {
			return c.Snapshot(), true
		}
	}
	return ComponentSnapshot{}, false
}

// RejectVersion appends version to the named component's rejects.txt. The
// component goroutine sees the change on its next poll tick and demotes if
// the rejected version is the one it is running.
func (this *Supervisor) RejectVersion(name, version string) error {
	for _, c := range this.components {
		if c.Name() == name {
			return c.paths.AppendReject(version)
		}
	}
	return fmt.Errorf("no such component: %s", name)
}

// Run starts every component goroutine, the HTTP server, and a single
// forced-overrides refresher. It blocks until ctx is cancelled, then performs
// a graceful shutdown.
func (this *Supervisor) Run(ctx context.Context) error {
	this.logger.Status("starting (state_dir=%s, %d components)", this.cfg.StateDir, len(this.cfg.Components))

	this.refreshForced()

	// Start every component goroutine.
	var wg sync.WaitGroup
	for _, comp := range this.components {
		wg.Go(func() {
			if err := comp.Run(ctx); err != nil {
				this.logger.Error("[%s] terminated with error: %v", comp.Name(), err)
			}
		})
	}

	// Forced-overrides refresher: re-reads the file on every polling tick
	// (using the supervisor-wide top-level poll interval as a coarse cadence;
	// individual components see overrides via this.getForced() at decision time).
	wg.Go(func() { this.runForcedRefresher(ctx) })

	// Scraper (optional): polls each component's /healthz, /state, /metrics
	// and feeds the results into the supervisor's statekit registry.
	if this.scraper != nil {
		wg.Go(func() { this.scraper.Run(ctx) })
	}

	// Observer (optional): snapshots the registry into the health store so
	// the /health console has current state + transition events.
	if this.observer != nil {
		wg.Go(func() { this.observer.Run(ctx) })
	}

	// HTTP server.
	httpErr := make(chan error, 1)
	this.httpServer.Start(httpErr)

	select {
	case <-ctx.Done():
		this.logger.Status("shutdown requested")
	case err := <-httpErr:
		this.logger.Error("HTTP server: %v", err)
	}

	// Graceful HTTP shutdown.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	_ = this.httpServer.Shutdown(shutdownCtx)
	cancel()

	wg.Wait()
	this.logger.Status("exited")
	return nil
}

// runForcedRefresher periodically re-reads forced_versions.txt so a customer
// can break-glass without restarting the supervisor.
func (this *Supervisor) runForcedRefresher(ctx context.Context) {
	interval := this.cfg.Remote.PollingInterval
	if interval <= 0 {
		interval = time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			this.refreshForced()
		}
	}
}

func (this *Supervisor) refreshForced() {
	f, err := ReadForcedOverrides(this.paths.ForcedVersions())
	if err != nil {
		this.lastPollError.Store("forced overrides: " + err.Error())
		this.logger.Warn("read forced_versions.txt: %v", err)
		return
	}
	this.forcedMu.Lock()
	this.forced = f
	this.forcedMu.Unlock()
	this.lastPoll.Store(time.Now())
}

// makeForcedGetter returns a closure that a Component can call to consult the
// supervisor's latest forced-overrides snapshot for its name.
func (this *Supervisor) makeForcedGetter(name string) func() ForcedOverride {
	return func() ForcedOverride {
		this.forcedMu.RLock()
		defer this.forcedMu.RUnlock()
		return this.forced.Lookup(name)
	}
}

func resolvePublicKey(cfg Config, opts Options, logger *log.Logger) ([]byte, error) {
	if opts.PublicKey != nil {
		pub, err := ParsePublicKey(opts.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("Options.PublicKey: %w", err)
		}
		return pub, nil
	}
	if cfg.Remote.SignaturePublicKeyPath == "" {
		if hasRemoteComponents(cfg) {
			logger.Warn("remote.signature_public_key_path is empty: image signatures will NOT be verified (dev / trusted local FS only)")
		}
		return nil, nil
	}
	pub, err := LoadPublicKeyFile(cfg.Remote.SignaturePublicKeyPath)
	if err != nil {
		return nil, err
	}
	return pub, nil
}

func hasRemoteComponents(cfg Config) bool {
	for _, c := range cfg.Components {
		if c.Remote.Enabled {
			return true
		}
	}
	return false
}

func describeForced(o ForcedOverride) string {
	switch o.Kind {
	case ForcedKindStable:
		return "stable"
	case ForcedKindVersion:
		return o.Version
	default:
		return ""
	}
}
