package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/shlex"

	"github.com/gur-shatz/go-run/internal/log"
)

// healthProbeInterval is how often the built-in global-health probe hits each
// component's /healthz. Always on, independent of statemonitor.scrape.
const healthProbeInterval = 5 * time.Second

// healthProbeClient is the short-timeout client the global-health probe uses.
var healthProbeClient = &http.Client{Timeout: 2 * time.Second}

// Component is one supervised child plus all the state that decides what
// version it should be running and what to do when it crashes.
//
// Run() drives two coordinated goroutines:
//
//   - runUpdater: polls the remote, applies forced overrides, downloads and
//     extracts new versions in the background, and writes current.txt only
//     after a usable on-disk version exists. Signals the lifecycle via
//     switchCh.
//   - runLifecycle: owns the child process — launch, monitor, exit handling,
//     backoff, stability detection, demote on bad-version.
//
// Operations are exposed as named methods (PrepareVersion, SwitchToVersion,
// LaunchChild, StopChild, HandleChildExit, DemoteToStable, MaybeStabilize)
// so the orchestrator reads as a vocabulary instead of an inline switch.
type Component struct {
	cfg     ComponentConfig
	paths   ComponentPaths
	install *Installer
	logger  *log.Logger
	bundle  *statekitBundle

	// Knobs copied out of the top-level Config for cheap access.
	// crash_window / crash_threshold / exec_fail_threshold live inside the
	// embedded Counters; version_folder_retention is owned by the Supervisor
	// (GC) rather than the Component, so they don't appear here.
	killGracePeriod time.Duration
	stabilityTime   time.Duration
	pollingInterval time.Duration
	rejectExpiry    time.Duration
	logMaxSize      int64
	logMaxFiles     int

	// supervisorVars is the shared template context applied to every
	// component's *.tmpl files at launch time. Read-only after construction.
	supervisorVars map[string]any

	// Forced overrides snapshot accessor — read each Updater tick.
	getForced func() ForcedOverride

	// memIncident, when set, captures a memory incident (a snapshot of recent
	// samples) on an abnormal child exit. nil when the memory subsystem is off.
	// Set after construction via SetMemoryIncidentHook.
	memIncident func(component, kind, reason string)

	// cgroupAttach, when set, charges a launched child into this component's
	// cgroup v2 leaf. It is called inside LaunchChild with the prepared *exec.Cmd
	// before Start and returns a post-Start func invoked with the child pid. nil
	// when there are no leaves (non-cgroup2 platform or enforcement disabled).
	cgroupAttach func(cmd *exec.Cmd) func(pid int)

	// Limp-mode warn-once flags for the launch-path fallbacks (kill socket,
	// on-disk child log). Only touched by the lifecycle goroutine.
	warnedNoKillSock bool
	warnedNoChildLog bool

	// limpPendingUpdate carries this reconcile cycle's "update available but
	// state dir is not writable" message so the prepared-OK push at the end of
	// the cycle does not erase it from the update leaf. Set and read by the
	// updater goroutine only; empty when nothing is pending.
	limpPendingUpdate string

	// Channel from Updater → Lifecycle. Coalescing: at most one pending
	// switch at a time; signalSwitch replaces an older queued value.
	switchCh chan string

	// Channel from HTTP/control planes → Lifecycle. The lifecycle goroutine is
	// the only owner that stops children, so manual controls stay ordered with
	// normal update/restart decisions.
	controlCh chan componentControlRequest

	// State (mu protects everything below). Severity AND the human-readable
	// reason both live on the bundle's lifecycle leaf — Component owns no
	// separate vocabulary or error-string buffer.
	mu            sync.Mutex
	pid           int
	launchedAt    time.Time
	manualStopped bool

	counters *Counters
	backoff  *Backoff

	// Cumulative uptime accounting for stability/promotion.
	currentRunning   string
	currentSince     time.Time
	cumulativeUptime time.Duration

	// globalState holds the latest /healthz read (pass/warn/fail/down), set by
	// the built-in health probe and read by Snapshot. atomic.Value so the probe
	// goroutine and Snapshot don't contend on mu.
	globalState atomic.Value // string
}

type componentControlAction string

const (
	componentControlStart   componentControlAction = "start"
	componentControlStop    componentControlAction = "stop"
	componentControlRestart componentControlAction = "restart"
)

type componentControlRequest struct {
	action componentControlAction
	reply  chan error
}

// NewComponent constructs a Component. Call Run to start the goroutines.
// bundle may be nil; when set, lifecycle transitions and counters push into
// the supervisor's statekit registry.
func NewComponent(cfg ComponentConfig, paths ComponentPaths, install *Installer, top Config, getForced func() ForcedOverride, bundle *statekitBundle, logger *log.Logger) *Component {
	if getForced == nil {
		getForced = func() ForcedOverride { return ForcedOverride{Kind: ForcedKindNone} }
	}
	if cfg.Remote.BaseURL != "" && !cfg.Remote.EnabledSet {
		cfg.Remote.Enabled = true
	}
	if cfg.Factory != nil {
		paths = paths.WithFactory(cfg.Factory.Dir, cfg.Factory.Version)
	}
	return &Component{
		cfg:             cfg,
		paths:           paths,
		install:         install,
		logger:          logger,
		bundle:          bundle,
		killGracePeriod: top.KillGracePeriod,
		stabilityTime:   top.StabilityTime,
		pollingInterval: cfg.Remote.PollingInterval,
		rejectExpiry:    top.RejectExpiry,
		logMaxSize:      top.LogMaxSize,
		logMaxFiles:     top.LogMaxFiles,
		supervisorVars:  top.Vars,
		getForced:       getForced,
		switchCh:        make(chan string, 1),
		controlCh:       make(chan componentControlRequest),
		counters:        NewCounters(top.CrashWindow, top.CrashThreshold, top.ExecFailThreshold),
		backoff:         NewBackoff(),
	}
}

// Name returns the component's name.
func (this *Component) Name() string { return this.cfg.Name }

// PID returns the running child's process ID, or 0 if no child is currently
// running. Read by the memory monitor each sample tick.
func (this *Component) PID() int {
	this.mu.Lock()
	defer this.mu.Unlock()
	return this.pid
}

// UptimeSeconds returns the running child's uptime in seconds, or 0 if no child
// is currently running. Read by the memory enforcer to honor min_uptime.
func (this *Component) UptimeSeconds() int64 {
	this.mu.Lock()
	defer this.mu.Unlock()
	if this.launchedAt.IsZero() || this.pid == 0 {
		return 0
	}
	return int64(time.Since(this.launchedAt).Seconds())
}

// SetMemoryIncidentHook wires the memory monitor's incident capture, invoked on
// an abnormal child exit. A nil hook (memory subsystem off) leaves exit handling
// unchanged.
func (this *Component) SetMemoryIncidentHook(fn func(component, kind, reason string)) {
	this.memIncident = fn
}

// SetCgroupAttach wires the cgroup-leaf charging hook used at child launch. A
// nil hook (no leaves) leaves launch unchanged.
func (this *Component) SetCgroupAttach(fn func(cmd *exec.Cmd) func(pid int)) {
	this.cgroupAttach = fn
}

// TerminateForMemory kills the running child because it breached its memory
// budget. It signals the child's process group (SIGTERM, then SIGKILL after
// kill_grace_period), and the resulting exit flows through the normal lifecycle
// exit path — so a memory kill is handled exactly like the process crashing on
// its own: fast-crash accounting, backoff, and bad-version rollback all apply.
// It does NOT go through the control channel (which would be a clean restart, not
// a crash). No-op when no child is running. Called from the memory sampling
// goroutine; safe to race with the lifecycle's own exit handling.
func (this *Component) TerminateForMemory(reason string) {
	this.mu.Lock()
	pid := this.pid
	this.mu.Unlock()
	if pid <= 0 {
		return
	}
	this.logger.Warn("[%s] memory kill (%s); pid=%d — terminating, counts as a crash", this.cfg.Name, reason, pid)
	this.requestOverflowDump(pid, reason)
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	// Escalate to SIGKILL after the grace window in case the child ignores
	// SIGTERM. Signalling a dead/relaunched group is a harmless ESRCH.
	grace := this.killGracePeriod
	go func(pid int) {
		time.Sleep(grace)
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}(pid)
}

// requestOverflowDump gives a component one last chance to dump diagnostics
// before a supervisor-driven memory kill. overflow-path is resolved against the
// component's known HTTP base URL: http://127.0.0.1:<port>.
func (this *Component) requestOverflowDump(pid int, reason string) {
	pathSpec := ""
	if this.cfg.Memory != nil {
		pathSpec = this.cfg.Memory.OverflowPath
	}
	if strings.TrimSpace(pathSpec) == "" {
		return
	}
	path, err := normalizeOverflowPath(pathSpec)
	if err != nil {
		this.logger.Warn("[%s] overflow-path skipped: %v", this.cfg.Name, err)
		return
	}
	timeout := this.killGracePeriod
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	url := fmt.Sprintf("http://127.0.0.1:%d%s", this.cfg.Port, path)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		this.logger.Warn("[%s] overflow-path request build failed: %v", this.cfg.Name, err)
		return
	}
	req.Header.Set("X-Go-Run-Overflow-PID", fmt.Sprintf("%d", pid))
	req.Header.Set("X-Go-Run-Overflow-Child-PID", fmt.Sprintf("%d", pid))
	req.Header.Set("X-Go-Run-Overflow-Reason", reason)

	this.logger.Status("[%s] requesting overflow dump before memory kill: POST %s", this.cfg.Name, url)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		this.logger.Warn("[%s] overflow-path request failed: %v", this.cfg.Name, err)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		this.logger.Warn("[%s] overflow-path request returned HTTP %d", this.cfg.Name, resp.StatusCode)
	}
}

// Snapshot returns a JSON/YAML-friendly snapshot of the current state.
func (this *Component) Snapshot() ComponentSnapshot {
	this.mu.Lock()
	defer this.mu.Unlock()

	stable, _ := this.paths.ReadStable()
	current, _ := this.paths.ReadCurrent()

	var uptimeSeconds int64
	if !this.launchedAt.IsZero() && this.pid != 0 {
		uptimeSeconds = int64(time.Since(this.launchedAt).Seconds())
	}

	// Severity comes from the statekit lifecycle leaf — single source of
	// truth. "down" when no bundle is wired (test-mode without registry).
	status := "down"
	statusReason := ""
	updateState := ""
	updateReason := ""
	var runCount int64
	if this.bundle != nil {
		lifecycleSnap := this.bundle.lifecycleSnapshot(this.cfg.Name)
		status = lifecycleSnap.Status.String()
		statusReason = lifecycleSnap.Reason
		updateSnap := this.bundle.updateSnapshot(this.cfg.Name)
		updateState = updateSnap.Status.String()
		updateReason = updateSnap.Reason
		runCount = this.bundle.runCountFor(this.cfg.Name)
	}

	// Last upgrade = current.txt's mtime, which only advances on a real
	// version switch (SwitchToVersion writes it only when the version changes).
	var lastUpgrade string
	if current != "" {
		if fi, err := os.Stat(this.paths.Current()); err == nil {
			lastUpgrade = fi.ModTime().UTC().Format(time.RFC3339)
		}
	}

	return ComponentSnapshot{
		Name:          this.cfg.Name,
		Description:   this.cfg.Description,
		GlobalState:   this.currentGlobalState(),
		UpdateStatus:  this.updateStatus(),
		UpdateState:   updateState,
		UpdateReason:  updateReason,
		Status:        status,
		StatusReason:  statusReason,
		Stable:        stable,
		Current:       current,
		ChildPID:      this.pid,
		UptimeSeconds: uptimeSeconds,
		RunCount:      runCount,
		FastCrashes:   this.counters.FastCrashes,
		ExecFailures:  this.counters.ExecFailures,
		LastUpgrade:   lastUpgrade,
		Port:          this.cfg.Port,
		MonitorURLs: MonitorURLs{
			Healthz:     this.cfg.URLs.Healthz,
			Readyz:      this.cfg.URLs.Readyz,
			State:       this.cfg.URLs.State,
			Metrics:     this.cfg.URLs.Metrics,
			Escalations: this.cfg.URLs.Escalations,
		},
		ProxyURLs: this.cfg.ProxyURLs,
	}
}

// Start resumes normal supervision after a manual stop.
func (this *Component) Start(ctx context.Context) error {
	return this.control(ctx, componentControlStart)
}

// Stop terminates the child, if any, and keeps this component intentionally
// stopped until Start or Restart is called.
func (this *Component) Stop(ctx context.Context) error {
	return this.control(ctx, componentControlStop)
}

// Restart asks the lifecycle goroutine to stop the current child and relaunch
// the latest current version through the normal launch path.
func (this *Component) Restart(ctx context.Context) error {
	return this.control(ctx, componentControlRestart)
}

func (this *Component) control(ctx context.Context, action componentControlAction) error {
	req := componentControlRequest{action: action, reply: make(chan error, 1)}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case this.controlCh <- req:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-req.reply:
		return err
	}
}

// currentGlobalState returns the latest /healthz read, or "down" if the probe
// hasn't produced a value yet.
func (this *Component) currentGlobalState() string {
	if s, ok := this.globalState.Load().(string); ok && s != "" {
		return s
	}
	return "down"
}

// updateStatus describes the supervisor's versioning posture for this
// component: "live" when auto-updating, or pinned by a forced override.
func (this *Component) updateStatus() string {
	switch f := this.getForced(); f.Kind {
	case ForcedKindStable:
		return "pinned to stable"
	case ForcedKindVersion:
		return "pinned to " + f.Version
	default:
		return "live"
	}
}

// runHealthProbe is the built-in global-health loop: it polls the component's
// /healthz on a fixed cadence (always on, independent of statemonitor.scrape)
// and stores the graded result for Snapshot.
func (this *Component) runHealthProbe(ctx context.Context) {
	t := time.NewTicker(healthProbeInterval)
	defer t.Stop()
	this.globalState.Store(this.probeGlobalState(ctx))
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			this.globalState.Store(this.probeGlobalState(ctx))
		}
	}
}

// probeGlobalState performs one /healthz read and maps it to the global-state
// vocabulary: the response body ("pass"/"warn"/"fail") on 200, else "down". A
// 200 with an unrecognised body counts as "pass" — it answered, so it's alive.
func (this *Component) probeGlobalState(ctx context.Context) string {
	url := fmt.Sprintf("http://127.0.0.1:%d%s", this.cfg.Port, this.cfg.URLs.Healthz)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "down"
	}
	resp, err := healthProbeClient.Do(req)
	if err != nil {
		return "down"
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "down"
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	switch strings.TrimSpace(string(body)) {
	case "warn":
		return "warn"
	case "fail":
		return "fail"
	default:
		return "pass"
	}
}

// Run drives the component until ctx is cancelled.
func (this *Component) Run(ctx context.Context) error {
	this.logger.Status("[%s] starting", this.cfg.Name)
	defer this.logger.Status("[%s] stopped", this.cfg.Name)

	var wg sync.WaitGroup
	wg.Go(func() { this.runUpdater(ctx) })
	wg.Go(func() { this.runLifecycle(ctx) })
	wg.Go(func() { this.runHealthProbe(ctx) })
	wg.Wait()
	return nil
}

// runUpdater periodically reconciles "what version should we run?" with the
// on-disk state. Downloads happen here; the lifecycle goroutine is only
// signalled once a new version is fully prepared.
func (this *Component) runUpdater(ctx context.Context) {
	this.reconcileTarget(ctx) // immediate first pass — don't wait one tick at startup

	t := time.NewTicker(this.pollingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			this.reconcileTarget(ctx)
		}
	}
}

// reconcileTarget runs one update cycle: compute the desired version, run
// the full prepare pipeline (download + verify + extract + best-effort
// template validation), and commit the swap. A failure at any step in
// PrepareVersion fails the update — the update sub-state goes to fail/warn
// and SwitchToVersion never fires.
//
// Validation runs every cycle (even when target equals current), so obvious
// manifest/template/env problems are caught before the child is asked to run
// the version.
func (this *Component) reconcileTarget(ctx context.Context) {
	target, err := this.computeDesiredVersion(ctx)
	if err != nil {
		// observeUpdateError already set the substate; nothing else to do here.
		return
	}
	if target == "" {
		// Halt-like state (forced-no-stable / required-rejected-no-fallback).
		// Lifecycle stays where it is. Kick its select in case it's still
		// waiting for first switch.
		if current, _ := this.paths.ReadCurrent(); current != "" {
			this.signalSwitch(current)
		}
		return
	}

	if err := this.PrepareVersion(ctx, target); err != nil {
		if errors.Is(err, ErrVersionRejected) {
			// Unreachable in normal flow: computeDesiredVersion filters
			// rejected targets. Keep the guard so future logic shifts can't
			// produce a confusing prepare loop.
			return
		}
		if this.bundle != nil {
			this.bundle.observeUpdateError(this.cfg.Name, err)
		}
		this.logger.Warn("[%s] prepare %s failed: %v", this.cfg.Name, target, err)
		return
	}
	if this.bundle != nil {
		if this.limpPendingUpdate != "" {
			// Keep the "update available, state dir unwritable" signal on the
			// health surface — it is the operator's cue that the install is
			// degraded rather than current.
			this.bundle.observeUpdateWarn(this.cfg.Name, this.limpPendingUpdate)
		} else {
			this.bundle.observeUpdateOK(this.cfg.Name, "prepared "+target)
		}
	}

	if err := this.SwitchToVersion(ctx, target); err != nil {
		if this.bundle != nil {
			this.bundle.observeUpdateError(this.cfg.Name, err)
		}
	}
}

// computeDesiredVersion returns the version the supervisor should be running
// — possibly the same one it's already on. The reconcile loop then runs
// PrepareVersion (which re-validates manifest templates every tick) and
// SwitchToVersion (a no-op when target equals current).
//
// Empty means "no actionable target" — halt-like states only:
//   - `* = stable` forced but no stable.txt,
//   - remote required is rejected and not on disk to retry,
//   - remote poll failed and there's nothing local to fall back on.
//
// The decision is intentionally first-principles:
//
//  1. Resolve the target (forced override beats remote; remote beats nothing).
//  2. If the target is in rejects.txt AND differs from current, the
//     supervisor refuses to switch onto it — return empty (lifecycle keeps
//     current going).
//  3. Otherwise return the target.
//
// Side effects on the update sub-state: poll success/failure are pushed to
// observeUpdateOK / observeUpdateError so the YAML reader always knows
// "did the supervisor talk to the vendor recently?"
func (this *Component) computeDesiredVersion(ctx context.Context) (string, error) {
	this.limpPendingUpdate = ""
	override := this.getForced()
	stable, _ := this.paths.ReadStable()
	current, _ := this.paths.ReadCurrent()

	// 1. Forced override resolves to a concrete target.
	var target string
	switch override.Kind {
	case ForcedKindVersion:
		target = override.Version
	case ForcedKindStable:
		if stable == "" {
			this.markFail("* = stable but no stable.txt")
			return "", nil
		}
		target = stable
	default:
		if !this.cfg.Remote.Enabled {
			if fv := this.factoryVersion(); fv != "" && !this.versionUsable(current) {
				if this.bundle != nil {
					this.bundle.observeUpdateOK(this.cfg.Name, "factory "+fv)
				}
				target = fv
			} else if current == "" {
				this.markWarn("updates disabled: current.txt is empty")
				return "", nil
			} else {
				if this.bundle != nil {
					this.bundle.observeUpdateOK(this.cfg.Name, "local current = "+current)
				}
				target = current
			}
		} else {
			remoteVersion, err := this.install.Remote.ResolveVersion(ctx, this.cfg.Remote.BaseURL, this.cfg.Name, this.cfg.Remote.Target)
			if err != nil {
				if this.bundle != nil {
					this.bundle.observeUpdateError(this.cfg.Name, err)
				}
				// Boot must never block on the origin: with nothing usable in
				// the state dir, fall through to the baked factory version and
				// let the poll ticker adopt the remote target once it resolves.
				if fv := this.factoryVersion(); fv != "" && !this.versionUsable(current) {
					target = fv
				} else {
					return "", err
				}
			} else {
				if this.bundle != nil {
					this.bundle.observeUpdateOK(this.cfg.Name, "remote required = "+remoteVersion)
				}
				target = remoteVersion
			}
		}
	}

	// Limp mode: an unusable target cannot be installed (the state dir takes
	// no writes), so run what is runnable instead — but only after the poll
	// above, which stays live so health keeps reporting the pending remote
	// target ("update available, state dir unwritable").
	if !this.paths.Writable() && !this.versionUsable(target) {
		this.limpPendingUpdate = "update " + target + " available but state dir is not writable"
		if this.bundle != nil {
			this.bundle.observeUpdateWarn(this.cfg.Name, this.limpPendingUpdate)
		}
		switch {
		case this.versionUsable(current):
			target = current
		case this.factoryVersion() != "":
			target = this.factoryVersion()
		default:
			return "", nil
		}
	}

	// Target differs from current AND is rejected: refuse to switch onto
	// it. (No fallback-to-stable hop here: if a stable is available and
	// not the rejected one, demote already swapped current to it; if
	// there isn't one, we keep retrying current with backoff and recover
	// when stability_time is reached.)
	if target != current {
		if rejected, _ := this.paths.IsActivelyRejected(target, time.Now(), this.rejectExpiry); rejected {
			if this.bundle != nil {
				this.bundle.observeUpdateWarn(this.cfg.Name, "target "+target+" is rejected; holding current")
			}
			return "", nil
		}
	}
	return target, nil
}

// PrepareVersion is the four-step install pipeline:
//
//  1. download archive,
//  2. verify signature,
//  3. extract into versions/<v>/,
//  4. validate manifest-listed templates against the same deployment/launch
//     env shape the child receives, including file-extension validation.
//
// Steps 1–3 are idempotent against an already-extracted version dir (they
// become a no-op). Step 4 always runs and writes nothing. Any step failing
// returns an error; the caller (reconcileTarget) treats that as an update
// failure and the version never makes it to current.txt. This is a best-effort
// check only; the application remains the authority for loading its config.
func (this *Component) PrepareVersion(ctx context.Context, version string) error {
	// The factory version is always "prepared": its content ships in the image,
	// so nothing is fetched and no signature is checked (trust comes from the
	// image, same as the supervisor binary).
	if version != localVersion && version != this.factoryVersion() && !versionExtracted(this.paths.VersionDir(version)) {
		if err := this.install.PrepareVersion(ctx, this.cfg.Name, this.cfg.Remote, this.paths, version); err != nil {
			return err
		}
	}
	return this.validateTemplates(version)
}

// factoryVersion returns the configured factory version name, or "" when the
// component has no factory block.
func (this *Component) factoryVersion() string {
	if this.cfg.Factory == nil {
		return ""
	}
	return this.cfg.Factory.Version
}

// versionUsable reports whether the named version can be launched right now:
// non-empty and resolving to a non-empty directory. The factory version is
// usable by construction (config load verifies its dir).
func (this *Component) versionUsable(version string) bool {
	return version != "" && versionExtracted(this.paths.VersionDir(version))
}

func (this *Component) launchVars(version string) (LaunchVars, error) {
	versionDir := this.paths.VersionDir(version)
	if !versionExtracted(versionDir) {
		return LaunchVars{}, fmt.Errorf("version dir %s is missing", versionDir)
	}
	return LaunchVars{
		Version:     version,
		VersionDir:  versionDir,
		StateDir:    this.paths.Root,
		MonitorPort: this.cfg.Port,
		KillSock:    this.paths.KillSock(),
		LogDir:      this.paths.LogsDir(version),
	}, nil
}

// validateTemplates is the prepare-time template check. It renders in memory
// only. Supervisor does not write application config files.
func (this *Component) validateTemplates(version string) error {
	launchVars, err := this.launchVars(version)
	if err != nil {
		return err
	}
	return validateVersionTemplatesWithEnv(launchVars.VersionDir, this.supervisorVars, this.cfg.Env, launchVars)
}

// SwitchToVersion commits the named version to current.txt and signals the
// lifecycle goroutine to switch. Resets counters and backoff on a real
// version change. Idempotent on no-change.
func (this *Component) SwitchToVersion(_ context.Context, version string) error {
	if version == "" {
		return fmt.Errorf("SwitchToVersion: empty version")
	}
	current, _ := this.paths.ReadCurrent()
	if current != version {
		if err := this.paths.WriteCurrent(version); err != nil {
			return fmt.Errorf("write current.txt: %w", err)
		}
		this.counters.Reset()
		this.backoff.Reset()
		this.setCurrentRunning(version)
	}
	this.signalSwitch(version)
	return nil
}

// signalSwitch posts version to the lifecycle goroutine. Coalesces — if a
// switch is already pending we replace it with the newer target.
func (this *Component) signalSwitch(version string) {
	select {
	case <-this.switchCh:
	default:
	}
	select {
	case this.switchCh <- version:
	default:
	}
}

// runLifecycle owns the child process. It reads current.txt at the top of
// every iteration and reacts to: a switch signal (graceful stop + relaunch),
// child exit (count crashes, demote on threshold, sleep backoff), and a
// stability ticker (uptime gauge updates, maybe-promote-to-stable).
func (this *Component) runLifecycle(ctx context.Context) {
runLoop:
	for {
		if this.isManuallyStopped() {
			if !this.waitWhileStopped(ctx) {
				return
			}
			continue
		}

		version, _ := this.paths.ReadCurrent()
		if version == "" || !versionExtracted(this.paths.VersionDir(version)) {
			// Nothing launchable yet — wait for Updater.
			if !this.waitForSwitchOrControl(ctx) {
				return
			}
			continue
		}

		child, err := this.LaunchChild(ctx, version)
		if err != nil {
			this.counters.OnExecFailure()
			if this.bundle != nil {
				this.bundle.observeExecFailure(this.cfg.Name)
				this.bundle.incidentCrash(this.cfg.Name, version, "exec failed: "+err.Error(), nil)
			}
			this.logger.Error("[%s] launch %s failed: %v", this.cfg.Name, version, err)
			if this.counters.ShouldReject() {
				this.DemoteToStable(version, "exec failures exceeded threshold")
			} else {
				this.markWarn("exec failed: " + err.Error())
			}
			if !this.sleepOrSwitch(ctx, this.backoff.Next()) {
				return
			}
			continue
		}
		this.setStatusRunning(child)

		// Persistent ticker — using time.After inside the select would reset
		// every time another case fires (e.g. the updater signalling a
		// stale "same version" switch each poll tick), starving the
		// stability check.
		stableTicker := time.NewTicker(this.stabilityTickInterval())
		for {
			select {
			case <-ctx.Done():
				stableTicker.Stop()
				this.StopChild(child)
				return
			case req := <-this.controlCh:
				switch req.action {
				case componentControlStart:
					req.reply <- nil
				case componentControlStop:
					this.setManualStopped(true)
					stableTicker.Stop()
					this.StopChild(child)
					this.clearChildState()
					this.markDown("manually stopped")
					if this.bundle != nil {
						this.bundle.closeIncident(this.cfg.Name, "manually stopped")
					}
					req.reply <- nil
					continue runLoop
				case componentControlRestart:
					this.setManualStopped(false)
					stableTicker.Stop()
					this.StopChild(child)
					this.clearChildState()
					req.reply <- nil
					continue runLoop
				default:
					req.reply <- fmt.Errorf("unknown component control action: %s", req.action)
				}
			case newVersion := <-this.switchCh:
				if newVersion == version {
					// Stale signal (e.g. updater re-signalling same version). Stay on this version.
					continue
				}
				this.logger.Status("[%s] switching %s → %s", this.cfg.Name, version, newVersion)
				if this.bundle != nil {
					this.bundle.incidentDeploy(this.cfg.Name, version, newVersion)
				}
				stableTicker.Stop()
				this.StopChild(child)
				continue runLoop
			case exit := <-child.exitCh:
				stableTicker.Stop()
				delay := this.HandleChildExit(child, exit)
				if !this.sleepOrSwitch(ctx, delay) {
					return
				}
				continue runLoop
			case <-stableTicker.C:
				if this.bundle != nil {
					this.bundle.observeUptime(this.cfg.Name, child.launchedAt)
				}
				this.MaybeStabilize(version)
			}
		}
	}
}

// waitForSwitchOrControl blocks until either ctx is cancelled, the Updater
// posts a new version on switchCh, or an operator control changes the launch
// posture. Returns false on cancellation.
func (this *Component) waitForSwitchOrControl(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-this.switchCh:
		return true
	case req := <-this.controlCh:
		return this.applyControlWithoutChild(req)
	}
}

func (this *Component) waitWhileStopped(ctx context.Context) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case <-this.switchCh:
			// Keep accepting updater signals while stopped, but do not launch
			// until an explicit Start/Restart arrives.
		case req := <-this.controlCh:
			if !this.applyControlWithoutChild(req) {
				return false
			}
			if !this.isManuallyStopped() {
				return true
			}
		}
	}
}

func (this *Component) applyControlWithoutChild(req componentControlRequest) bool {
	switch req.action {
	case componentControlStart:
		this.setManualStopped(false)
		req.reply <- nil
	case componentControlStop:
		this.setManualStopped(true)
		this.clearChildState()
		this.markDown("manually stopped")
		req.reply <- nil
	case componentControlRestart:
		this.setManualStopped(false)
		req.reply <- nil
	default:
		req.reply <- fmt.Errorf("unknown component control action: %s", req.action)
	}
	return true
}

// sleepOrSwitch is a context-cancellable sleep that also wakes early on a
// Updater switch signal (the value is re-queued so the next iteration sees
// it). Used between launch attempts so backoff doesn't delay an update.
func (this *Component) sleepOrSwitch(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return true
	}
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	case v := <-this.switchCh:
		this.signalSwitch(v)
		return true
	case req := <-this.controlCh:
		return this.applyControlWithoutChild(req)
	}
}

// stabilityTickInterval returns a "reasonable" cadence at which the
// lifecycle checks for stability. Short relative to stability_time so we
// detect it promptly, but never below 1s to avoid busy looping.
func (this *Component) stabilityTickInterval() time.Duration {
	d := this.stabilityTime / 10
	if d < time.Second {
		return time.Second
	}
	if d > 30*time.Second {
		return 30 * time.Second
	}
	return d
}

// runningChild captures the live child process and its launch context.
type runningChild struct {
	version    string
	cmd        *exec.Cmd
	killSocket *KillSocket
	exitCh     chan childExit
	launchedAt time.Time

	// Rotating log file the supervisor writes the child's stdout / stderr into.
	// Closed when the child exits. Nil-safe so tests / nil bundles don't crash
	// if log opening was skipped.
	log *rotatingFile
}

type childExit struct {
	at  time.Time
	err error
}

// LaunchChild parses the command, sets the child's working directory to the
// version dir, opens the kill socket, and starts the child with Setpgid.
// Returns ExecFailure-eligible errors if the child cannot be started.
//
// Notes:
//   - The command is plain shell-style argv (parsed via shlex). There is no
//     ${VAR} expansion. Launch facts are passed as OP_*-prefixed variables
//     plus artifact-compatible aliases such as VERSION, BUILD_DIR, BUILDDIR,
//     MONITOR_PORT, and REQUIRED_VERSION; the child reads them from env.
//   - cmd.Dir is set to the version dir, so a relative argv[0] like
//     "./bin/hello" resolves naturally. For absolute argv[0], cmd.Dir
//     still applies (the child runs with cwd = version dir, with global
//     paths like /var/log still reachable — not chrooted).
func (this *Component) LaunchChild(_ context.Context, version string) (*runningChild, error) {
	versionDir := this.paths.VersionDir(version)
	if !versionExtracted(versionDir) {
		return nil, fmt.Errorf("version dir %s is missing", versionDir)
	}

	port := this.cfg.Port
	logDir := this.paths.LogsDir(version)
	vars := LaunchVars{
		Version:     version,
		VersionDir:  versionDir,
		StateDir:    this.paths.Root,
		MonitorPort: port,
		KillSock:    this.paths.KillSock(),
		LogDir:      logDir,
	}

	argv, err := shlex.Split(this.cfg.Command)
	if err != nil {
		return nil, fmt.Errorf("split command: %w", err)
	}
	if len(argv) == 0 {
		return nil, fmt.Errorf("empty command")
	}
	// Relative argv[0] is resolved against the version dir so that
	// `./bin/hello` (or `bin/hello`) finds the on-disk binary even though
	// the supervisor's own cwd is elsewhere. Bare names without a slash
	// fall through to PATH lookup, matching shell semantics.
	if !filepath.IsAbs(argv[0]) && strings.ContainsAny(argv[0], "/\\") {
		argv[0] = filepath.Join(versionDir, argv[0])
	}

	ks, err := ListenKillSocket(vars.KillSock, func() {
		this.logger.Status("[%s] kill socket signalled", this.cfg.Name)
	})
	if err != nil {
		if this.paths.Writable() {
			return nil, err
		}
		// Limp mode: the kill socket is a graceful-stop nicety; StopChild
		// already degrades to SIGTERM → SIGKILL without it.
		if !this.warnedNoKillSock {
			this.warnedNoKillSock = true
			this.logger.Warn("[%s] no kill socket (state dir not writable); graceful stop degrades to SIGTERM", this.cfg.Name)
		}
		ks = nil
	}

	// Open the per-version rotating process log. The child also gets logDir via
	// OP_LOG_DIR so its own application logs can live alongside.
	childLog, err := openRotatingFile(this.paths.Log(version), this.logMaxSize, this.logMaxFiles)
	if err != nil {
		if this.paths.Writable() {
			if ks != nil {
				_ = ks.Close()
			}
			return nil, fmt.Errorf("open component log: %w", err)
		}
		// Limp mode: pass child output through to the console only — in a
		// container that is where `docker logs` looks anyway.
		if !this.warnedNoChildLog {
			this.warnedNoChildLog = true
			this.logger.Warn("[%s] no on-disk component log (state dir not writable); child output goes to console only", this.cfg.Name)
		}
		childLog = nil
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = versionDir
	// MultiWriter: kubectl-logs-style process output AND the per-version
	// on-disk copy. The process output uses the original stdout/stderr, so
	// component output does not pollute the supervisor's own process log.
	stdout := io.Writer(newPrefixedWriter(this.cfg.Name, processConsoleStdout()))
	stderr := io.Writer(newPrefixedWriter(this.cfg.Name, processConsoleStderr()))
	if childLog != nil {
		stdout = io.MultiWriter(stdout, childLog)
		stderr = io.MultiWriter(stderr, childLog)
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env, err = this.buildEnv(vars)
	if err != nil {
		if childLog != nil {
			_ = childLog.Close()
		}
		if ks != nil {
			_ = ks.Close()
		}
		return nil, err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Charge the child into its cgroup leaf. The hook may prepare cmd before
	// Start and returns a post-Start func run with the child pid. Strictly
	// additive: a nil hook (no leaves) leaves launch untouched.
	var chargeLeaf func(pid int)
	if this.cgroupAttach != nil {
		chargeLeaf = this.cgroupAttach(cmd)
	}

	this.logger.Status("[%s] launching %s port=%d (logs=%s)", this.cfg.Name, version, port, logDir)
	if err := cmd.Start(); err != nil {
		if childLog != nil {
			_ = childLog.Close()
		}
		if ks != nil {
			_ = ks.Close()
		}
		return nil, err
	}
	if chargeLeaf != nil {
		chargeLeaf(cmd.Process.Pid)
	}

	rc := &runningChild{
		version:    version,
		cmd:        cmd,
		killSocket: ks,
		exitCh:     make(chan childExit, 1),
		launchedAt: time.Now(),
		log:        childLog,
	}
	go func() {
		err := cmd.Wait()
		// cmd.Wait() returned, so stdout/stderr pipes are drained. Errors here
		// are best-effort: a closed log file at most loses the very last byte
		// and is independent of the supervisor's ability to react to the exit.
		if rc.log != nil {
			_ = rc.log.Close()
		}
		rc.exitCh <- childExit{at: time.Now(), err: err}
	}()
	return rc, nil
}

// StopChild attempts a graceful shutdown via the kill socket, then SIGTERM,
// then SIGKILL on the process group, with kill_grace_period between
// escalations. Always closes the kill socket.
func (this *Component) StopChild(rc *runningChild) {
	if rc == nil {
		return
	}
	if rc.killSocket != nil {
		defer rc.killSocket.Close()
	}

	if rc.cmd == nil || rc.cmd.Process == nil {
		return
	}

	// Without a kill socket (limp mode) skip straight to SIGTERM.
	if rc.killSocket != nil {
		go tryKillSocketSelf(rc.killSocket.Path())

		select {
		case <-rc.exitCh:
			return
		case <-time.After(this.killGracePeriod):
		}
	}

	_ = syscall.Kill(-rc.cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-rc.exitCh:
		return
	case <-time.After(this.killGracePeriod):
	}

	_ = syscall.Kill(-rc.cmd.Process.Pid, syscall.SIGKILL)
	<-rc.exitCh
}

// HandleChildExit feeds the exit event into counters and metrics, clears
// per-launch state, and calls DemoteToStable if the threshold tripped.
// Returns the backoff delay the caller should sleep before relaunching;
// computing it here keeps the status-string and the actual delay in sync.
func (this *Component) HandleChildExit(rc *runningChild, exit childExit) time.Duration {
	// A memory kill (supervisor-terminated or kernel OOM) arrives here as an
	// ordinary child exit and is handled identically to the process crashing on
	// its own — the memory subsystem does not special-case it. The attribution
	// (which component, why) is captured separately as a memory incident.
	before := this.counters.FastCrashes
	this.counters.OnExit(rc.launchedAt, exit.at)
	if this.bundle != nil && this.counters.FastCrashes > before {
		this.bundle.observeFastCrash(this.cfg.Name)
	}
	if this.bundle != nil {
		this.bundle.observeUptime(this.cfg.Name, time.Time{})
	}
	this.clearChildState()
	this.logger.Warn("[%s] child %s exited after %s (err=%v)",
		this.cfg.Name, rc.version,
		exit.at.Sub(rc.launchedAt).Truncate(time.Millisecond), exit.err)
	exitReason := fmt.Sprintf("child exited after %s (err=%v)", exit.at.Sub(rc.launchedAt).Truncate(time.Millisecond), exit.err)
	if this.bundle != nil {
		this.bundle.incidentCrash(this.cfg.Name, rc.version, exitReason,
			map[string]any{"version": rc.version, "fast_crashes": this.counters.FastCrashes})
	}
	// Snapshot recent memory samples so a post-restart reader can see which
	// component grew and whether it climbed or spiked before the exit.
	if this.memIncident != nil {
		this.memIncident(this.cfg.Name, memIncidentChildExit, exitReason)
	}

	if this.counters.ShouldReject() {
		this.DemoteToStable(rc.version, "fast crashes exceeded threshold")
		// DemoteToStable owns the status string. Still use backoff so the
		// next relaunch attempt isn't immediate.
		return this.backoff.Next()
	}
	delay := this.backoff.Next()
	this.markWarn(fmt.Sprintf("child exited, restarting in %s", delay.Truncate(time.Millisecond)))
	return delay
}

// DemoteToStable handles the bad-version-criterion outcome: append to
// rejects.txt and swap current to a different stable (preferred), fall back
// to the factory version when stable is absent or unusable (stable of last
// resort), or keep retrying the rejected version with growing backoff. The
// lifecycle loop's sleep call (sleepOrSwitch) provides the spacing in all
// cases.
func (this *Component) DemoteToStable(version, reason string) {
	this.logger.Warn("[%s] demoting %s: %s", this.cfg.Name, version, reason)
	if err := this.paths.AppendReject(version); err != nil {
		this.markFail("append reject: " + err.Error())
		return
	}
	stable, _ := this.paths.ReadStable()
	// With a factory configured, an unusable stable no longer counts as a
	// regression target — the factory floor below catches it instead.
	if stable != "" && stable != version && (this.factoryVersion() == "" || this.versionUsable(stable)) {
		if err := this.paths.WriteCurrent(stable); err != nil {
			this.markFail("write current to stable: " + err.Error())
			return
		}
		this.counters.Reset()
		this.backoff.Reset()
		this.markWarn("demoted to stable " + stable)
		if this.bundle != nil {
			this.bundle.incidentRollback(this.cfg.Name, version, stable, reason)
		}
		return
	}
	if fv := this.factoryVersion(); fv != "" && fv != version {
		rejected, _ := this.paths.IsActivelyRejected(fv, time.Now(), this.rejectExpiry)
		if !rejected {
			if err := this.paths.WriteCurrent(fv); err != nil {
				this.markFail("write current to factory: " + err.Error())
				return
			}
			this.counters.Reset()
			this.backoff.Reset()
			this.markWarn("demoted to factory " + fv)
			if this.bundle != nil {
				this.bundle.incidentRollback(this.cfg.Name, version, fv, reason)
			}
			return
		}
	}
	// The demote was logged; the regression to stable failed. Surface the
	// specific reason as a second warning so an operator scanning logs sees
	// "we couldn't actually fall back" right next to "we tried to demote".
	failReason := this.demoteFailureReason(version, stable)
	this.logger.Warn("[%s] cannot regress to stable: %s; will keep retrying %s under backoff (cap %s)",
		this.cfg.Name, failReason, version, this.backoff.Cap.String())
	this.counters.Reset()
	this.markWarn("rejected " + version + "; " + failReason + ", retrying under backoff")
	if this.bundle != nil {
		this.bundle.incidentRollback(this.cfg.Name, version, "", failReason)
	}
}

// isPinnedToStable reports whether the supervisor is currently holding the
// stable version as a fallback. True when the named version equals stable.txt
// AND at least one rejection is still active (within reject_expiry).
func (this *Component) isPinnedToStable(version string, now time.Time) bool {
	stable, _ := this.paths.ReadStable()
	if stable == "" || stable != version {
		return false
	}
	entries, _ := this.paths.ReadRejectEntries()
	for _, e := range entries {
		if e.RejectedAt.IsZero() {
			return true // legacy / indefinite rejection
		}
		if this.rejectExpiry <= 0 || now.Sub(e.RejectedAt) < this.rejectExpiry {
			return true
		}
	}
	return false
}

// activeRejectionsSummary returns a comma-separated list of version names
// currently in active rejection. Used for the StatusRunningPinnedStable
// reason text. Empty if nothing is rejected (caller normally won't reach
// this path in that case).
func (this *Component) activeRejectionsSummary(now time.Time) string {
	entries, _ := this.paths.ReadRejectEntries()
	var names []string
	for _, e := range entries {
		active := e.RejectedAt.IsZero() || this.rejectExpiry <= 0 || now.Sub(e.RejectedAt) < this.rejectExpiry
		if active {
			names = append(names, e.Version)
		}
	}
	return strings.Join(names, ", ")
}

// demoteFailureReason returns the specific reason DemoteToStable couldn't
// swap to a stable version.
func (this *Component) demoteFailureReason(version, stable string) string {
	switch stable {
	case "":
		return "no stable known yet"
	case version:
		return "stable.txt names the same version " + version
	default:
		// Shouldn't be reachable — the caller already handled
		// `stable != "" && stable != version`. Keep an explicit message in
		// case logic shifts.
		return "stable=" + stable + " available but not switched"
	}
}

// MaybeStabilize reacts to the child reaching stability_time of uptime: reset
// crash counters and backoff, observe the stability event in metrics, and
// (if not rejected) promote the version to stable.txt.
func (this *Component) MaybeStabilize(version string) {
	this.mu.Lock()
	if this.currentRunning != version {
		this.currentRunning = version
		this.currentSince = time.Now()
		this.cumulativeUptime = 0
	}
	uptime := this.cumulativeUptime + time.Since(this.currentSince)
	this.mu.Unlock()

	if uptime < this.stabilityTime {
		return
	}

	this.counters.Reset()
	this.backoff.Reset()
	if this.bundle != nil {
		this.bundle.observeStability(this.cfg.Name)
		this.bundle.incidentStabilized(this.cfg.Name, version)
	}

	rejected, _ := this.paths.IsActivelyRejected(version, time.Now(), this.rejectExpiry)
	if rejected {
		return
	}
	stable, _ := this.paths.ReadStable()
	if stable == version {
		return
	}
	if err := this.paths.WriteStable(version); err != nil {
		this.markFail("write stable: " + err.Error())
		return
	}
	this.logger.Success("[%s] promoted %s → stable", this.cfg.Name, version)
}

func (this *Component) setCurrentRunning(version string) {
	this.mu.Lock()
	defer this.mu.Unlock()
	if this.currentRunning != version {
		this.currentRunning = version
		this.currentSince = time.Now()
		this.cumulativeUptime = 0
	}
}

func (this *Component) clearChildState() {
	this.mu.Lock()
	this.pid = 0
	this.launchedAt = time.Time{}
	this.mu.Unlock()
}

func (this *Component) setManualStopped(stopped bool) {
	this.mu.Lock()
	this.manualStopped = stopped
	this.mu.Unlock()
}

func (this *Component) isManuallyStopped() bool {
	this.mu.Lock()
	defer this.mu.Unlock()
	return this.manualStopped
}

// markPass / markWarn / markFail / markDown push a transition directly to the
// statekit lifecycle leaf. There is no separate ComponentStatus enum or
// lastError buffer — the call site picks severity (mirroring statekit's
// vocabulary) and the reason text lives on the leaf.
func (this *Component) markPass(reason string) {
	if this.bundle != nil {
		this.bundle.markPass(this.cfg.Name, reason)
	}
}

func (this *Component) markWarn(reason string) {
	if this.bundle != nil {
		this.bundle.markWarn(this.cfg.Name, reason)
	}
}

func (this *Component) markFail(reason string) {
	if this.bundle != nil {
		this.bundle.markFail(this.cfg.Name, reason)
	}
}

func (this *Component) markDown(reason string) {
	if this.bundle != nil {
		this.bundle.markDown(this.cfg.Name, reason)
	}
}

// setStatusRunning records that a child has been launched and picks the
// severity of the running state: pass (clean), warn (pinned to stable as a
// fallback), or fail (running a still-rejected version because there's
// nothing else to fall back to).
func (this *Component) setStatusRunning(rc *runningChild) {
	now := time.Now()
	rejected, _ := this.paths.IsActivelyRejected(rc.version, now, this.rejectExpiry)

	this.mu.Lock()
	this.pid = rc.cmd.Process.Pid
	this.launchedAt = rc.launchedAt
	this.mu.Unlock()

	if this.bundle != nil {
		this.bundle.observeLaunch(this.cfg.Name)
		this.bundle.observeUptime(this.cfg.Name, rc.launchedAt)
	}

	switch {
	case rejected:
		stable, _ := this.paths.ReadStable()
		failReason := this.demoteFailureReason(rc.version, stable)
		this.markFail(fmt.Sprintf("running rejected %s (pid=%d); %s",
			rc.version, rc.cmd.Process.Pid, failReason))
	case this.isPinnedToStable(rc.version, now):
		this.markWarn(fmt.Sprintf("running stable %s (pid=%d); pinned after rejecting %s",
			rc.version, rc.cmd.Process.Pid, this.activeRejectionsSummary(now)))
	default:
		this.markPass(fmt.Sprintf("running %s (pid=%d)", rc.version, rc.cmd.Process.Pid))
	}
}

// buildEnv merges process env, manifest default_vars, supervisor vars,
// component env, and launch facts. This mirrors runctl's child-process model:
// deployment vars are available as environment variables, and component env can
// override them for one component.
func (this *Component) buildEnv(vars LaunchVars) ([]string, error) {
	env := environSliceToMap(os.Environ())
	defaults, err := readDefaultVars(vars.VersionDir)
	if err != nil {
		return nil, err
	}
	for k, v := range defaults {
		env[k] = fmt.Sprintf("%v", v)
	}
	for k, v := range this.supervisorVars {
		env[k] = fmt.Sprintf("%v", v)
	}
	for k, v := range this.cfg.Env {
		env[k] = v
	}
	for k, v := range EnvMap(vars) {
		env[k] = v
	}
	return envMapToSlice(env), nil
}
