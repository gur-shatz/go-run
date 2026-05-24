package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/shlex"

	"github.com/gur-shatz/go-run/internal/log"
)

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

	// Channel from Updater → Lifecycle. Coalescing: at most one pending
	// switch at a time; signalSwitch replaces an older queued value.
	switchCh chan string

	// State (mu protects everything below). Severity AND the human-readable
	// reason both live on the bundle's lifecycle leaf — Component owns no
	// separate vocabulary or error-string buffer.
	mu         sync.Mutex
	pid        int
	launchedAt time.Time

	counters *Counters
	backoff  *Backoff

	// Cumulative uptime accounting for stability/promotion.
	currentRunning   string
	currentSince     time.Time
	cumulativeUptime time.Duration
}

// NewComponent constructs a Component. Call Run to start the goroutines.
// bundle may be nil; when set, lifecycle transitions and counters push into
// the supervisor's statekit registry.
func NewComponent(cfg ComponentConfig, paths ComponentPaths, install *Installer, top Config, getForced func() ForcedOverride, bundle *statekitBundle, logger *log.Logger) *Component {
	if getForced == nil {
		getForced = func() ForcedOverride { return ForcedOverride{Kind: ForcedKindNone} }
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
		counters:        NewCounters(top.CrashWindow, top.CrashThreshold, top.ExecFailThreshold),
		backoff:         NewBackoff(),
	}
}

// Name returns the component's name.
func (this *Component) Name() string { return this.cfg.Name }

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
	if this.bundle != nil {
		status = this.bundle.lifecycleSnapshot(this.cfg.Name).Status.String()
	}

	return ComponentSnapshot{
		Name:          this.cfg.Name,
		Description:   this.cfg.Description,
		Status:        status,
		Stable:        stable,
		Current:       current,
		ChildPID:      this.pid,
		UptimeSeconds: uptimeSeconds,
		FastCrashes:   this.counters.FastCrashes,
		ExecFailures:  this.counters.ExecFailures,
		Port:          this.cfg.Port,
		MonitorURLs: MonitorURLs{
			Healthz: this.cfg.URLs.Healthz,
			Readyz:  this.cfg.URLs.Readyz,
			State:   this.cfg.URLs.State,
			Metrics: this.cfg.URLs.Metrics,
		},
	}
}

// Run drives the component until ctx is cancelled.
func (this *Component) Run(ctx context.Context) error {
	this.logger.Status("[%s] starting", this.cfg.Name)
	defer this.logger.Status("[%s] stopped", this.cfg.Name)

	var wg sync.WaitGroup
	wg.Go(func() { this.runUpdater(ctx) })
	wg.Go(func() { this.runLifecycle(ctx) })
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
// the full prepare pipeline (download + verify + extract + render), and
// commit the swap. A failure at any step in PrepareVersion fails the
// update — the update sub-state goes to fail/warn and SwitchToVersion
// never fires.
//
// Render runs every cycle (even when target equals current), so a
// supervisor.yml `vars:` edit propagates to on-disk rendered files on the
// next poll tick; the child consumes them at its next restart.
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
		this.bundle.observeUpdateOK(this.cfg.Name, "prepared "+target)
	}

	if err := this.SwitchToVersion(ctx, target); err != nil {
		if this.bundle != nil {
			this.bundle.observeUpdateError(this.cfg.Name, err)
		}
	}
}

// computeDesiredVersion returns the version the supervisor should be running
// — possibly the same one it's already on. The reconcile loop then runs
// PrepareVersion (which re-renders templates every tick) and
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
		remoteVersion, err := this.install.Remote.ResolveVersion(ctx, this.cfg.Remote.BaseURL, this.cfg.Name, this.cfg.Remote.Target)
		if err != nil {
			if this.bundle != nil {
				this.bundle.observeUpdateError(this.cfg.Name, err)
			}
			return "", err
		}
		if this.bundle != nil {
			this.bundle.observeUpdateOK(this.cfg.Name, "remote required = "+remoteVersion)
		}
		target = remoteVersion
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
//  4. render every *.tmpl against (supervisor vars + defaults.yml + launch
//     vars + env), with validation by file extension.
//
// Steps 1–3 are idempotent against an already-extracted version dir (they
// become a no-op). Step 4 always runs, so a vars-block edit reaches the
// on-disk rendered files on the next reconcile tick. Any step failing
// returns an error; the caller (reconcileTarget) treats that as an update
// failure and the version never makes it to current.txt.
func (this *Component) PrepareVersion(ctx context.Context, version string) error {
	if !versionExtracted(this.paths.VersionDir(version)) {
		if err := this.install.PrepareVersion(ctx, this.cfg.Name, this.cfg.Remote, this.paths, version); err != nil {
			return err
		}
	}
	return this.renderTemplates(version)
}

// renderTemplates is the per-version template-render step extracted so it
// can be called both from PrepareVersion (canonical path) and from focused
// tests. Not exported — the public entry point is PrepareVersion.
func (this *Component) renderTemplates(version string) error {
	versionDir := this.paths.VersionDir(version)
	if !versionExtracted(versionDir) {
		return fmt.Errorf("version dir %s is missing", versionDir)
	}
	launchVars := LaunchVars{
		Version:     version,
		VersionDir:  versionDir,
		StateDir:    this.paths.Root,
		MonitorPort: this.cfg.Port,
		KillSock:    this.paths.KillSock(),
		LogDir:      this.paths.LogsDir(version),
	}
	return renderVersionTemplates(versionDir, this.supervisorVars, launchVars)
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
		version, _ := this.paths.ReadCurrent()
		if version == "" || !versionExtracted(this.paths.VersionDir(version)) {
			// Nothing launchable yet — wait for Updater.
			if !this.waitForSwitch(ctx) {
				return
			}
			continue
		}

		child, err := this.LaunchChild(ctx, version)
		if err != nil {
			this.counters.OnExecFailure()
			if this.bundle != nil {
				this.bundle.observeExecFailure(this.cfg.Name)
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
			case newVersion := <-this.switchCh:
				if newVersion == version {
					// Stale signal (e.g. updater re-signalling same version). Stay on this version.
					continue
				}
				this.logger.Status("[%s] switching %s → %s", this.cfg.Name, version, newVersion)
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

// waitForSwitch blocks until either ctx is cancelled or the Updater posts a
// new version on switchCh. Returns false on cancellation.
func (this *Component) waitForSwitch(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-this.switchCh:
		return true
	}
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

	// Rotating log files the supervisor writes the child's stdout / stderr
	// into. Closed when the child exits. Nil-safe so tests / nil bundles
	// don't crash if log opening was skipped.
	stdoutLog *rotatingFile
	stderrLog *rotatingFile
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
//     ${VAR} expansion. The five launch facts (VERSION, VERSION_DIR,
//     STATE_DIR, MONITOR_PORT, KILL_SOCK) are passed as OP_*-prefixed
//     environment variables; the child reads them from env.
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
		return nil, err
	}

	// Open per-version rotating log files. The child also gets the dir
	// via OP_LOG_DIR so its own application logs can live alongside.
	stdoutLog, err := openRotatingFile(filepath.Join(logDir, "stdout.log"), this.logMaxSize, this.logMaxFiles)
	if err != nil {
		_ = ks.Close()
		return nil, fmt.Errorf("open stdout log: %w", err)
	}
	stderrLog, err := openRotatingFile(filepath.Join(logDir, "stderr.log"), this.logMaxSize, this.logMaxFiles)
	if err != nil {
		_ = stdoutLog.Close()
		_ = ks.Close()
		return nil, fmt.Errorf("open stderr log: %w", err)
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = versionDir
	// MultiWriter: kubectl-logs-style supervisor stdout AND the per-version
	// on-disk copy. Both stay populated for the lifetime of the child.
	cmd.Stdout = io.MultiWriter(newPrefixedWriter(this.cfg.Name, os.Stdout), stdoutLog)
	cmd.Stderr = io.MultiWriter(newPrefixedWriter(this.cfg.Name, os.Stderr), stderrLog)
	cmd.Env = this.buildEnv(vars)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	this.logger.Status("[%s] launching %s port=%d (logs=%s)", this.cfg.Name, version, port, logDir)
	if err := cmd.Start(); err != nil {
		_ = stdoutLog.Close()
		_ = stderrLog.Close()
		_ = ks.Close()
		return nil, err
	}

	rc := &runningChild{
		version:    version,
		cmd:        cmd,
		killSocket: ks,
		exitCh:     make(chan childExit, 1),
		launchedAt: time.Now(),
		stdoutLog:  stdoutLog,
		stderrLog:  stderrLog,
	}
	go func() {
		err := cmd.Wait()
		// cmd.Wait() returned, so stdout/stderr pipes are drained — safe to
		// close the rotating log files. Errors here are best-effort: a closed
		// log file at most loses the very last byte and is independent of the
		// supervisor's ability to react to the exit itself.
		if rc.stdoutLog != nil {
			_ = rc.stdoutLog.Close()
		}
		if rc.stderrLog != nil {
			_ = rc.stderrLog.Close()
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
	defer rc.killSocket.Close()

	if rc.cmd == nil || rc.cmd.Process == nil {
		return
	}

	go tryKillSocketSelf(rc.killSocket.Path())

	select {
	case <-rc.exitCh:
		return
	case <-time.After(this.killGracePeriod):
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
// rejects.txt and either swap current to a different stable (preferred) or
// keep retrying the rejected version with growing backoff. The lifecycle
// loop's sleep call (sleepOrSwitch) provides the spacing in both cases.
func (this *Component) DemoteToStable(version, reason string) {
	this.logger.Warn("[%s] demoting %s: %s", this.cfg.Name, version, reason)
	if err := this.paths.AppendReject(version); err != nil {
		this.markFail("append reject: " + err.Error())
		return
	}
	stable, _ := this.paths.ReadStable()
	if stable != "" && stable != version {
		if err := this.paths.WriteCurrent(stable); err != nil {
			this.markFail("write current to stable: " + err.Error())
			return
		}
		this.counters.Reset()
		this.backoff.Reset()
		this.markWarn("demoted to stable " + stable)
		return
	}
	// The demote was logged; the regression to stable failed. Surface the
	// specific reason as a second warning so an operator scanning logs sees
	// "we couldn't actually fall back" right next to "we tried to demote".
	failReason := this.demoteFailureReason(version, stable)
	this.logger.Warn("[%s] cannot regress to stable: %s; will keep retrying %s under backoff (cap %s)",
		this.cfg.Name, failReason, version, this.backoff.Cap.String())
	this.counters.Reset()
	this.markWarn("rejected " + version + "; " + failReason + ", retrying under backoff")
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

// buildEnv merges the component's configured env with the OP_* launch vars
// and the supervisor's own environment.
func (this *Component) buildEnv(vars LaunchVars) []string {
	env := os.Environ()
	for k, v := range this.cfg.Env {
		env = append(env, k+"="+v)
	}
	env = append(env, EnvSlice(vars)...)
	return env
}
