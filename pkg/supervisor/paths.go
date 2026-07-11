package supervisor

import "path/filepath"

const localVersion = "."

// Paths derives every state-dir path the supervisor cares about from a single
// state_dir root. It is a value type so test code can construct one against a
// tmpdir without going through Config.
type Paths struct {
	StateDir string
}

// NewPaths returns a Paths rooted at the given absolute state directory.
func NewPaths(stateDir string) Paths {
	return Paths{StateDir: stateDir}
}

// SupervisorLock is the flock file the supervisor holds while running.
func (this Paths) SupervisorLock() string {
	return filepath.Join(this.StateDir, "supervisor.lock")
}

// ForcedVersions is the path to the (optional) forced_versions.txt overrides file.
func (this Paths) ForcedVersions() string {
	return filepath.Join(this.StateDir, "forced_versions.txt")
}

// LogsRoot is the per-supervisor top-level logs directory.
func (this Paths) LogsRoot() string {
	return filepath.Join(this.StateDir, "logs")
}

// LogsForComponent returns the per-component log directory:
// state_dir/logs/<component>/. Used by HTTP routes that browse the
// whole tree for one component.
func (this Paths) LogsForComponent(name string) string {
	return filepath.Join(this.LogsRoot(), name)
}

// LogsForVersion returns the per-version application log directory the child
// receives as OP_LOG_DIR. The supervisor's captured child stdout/stderr stream
// uses ComponentPaths.Log instead.
func (this Paths) LogsForVersion(name, version string) string {
	return filepath.Join(this.LogsRoot(), name, version)
}

// SupervisorLogs returns the directory containing supervisor process log
// streams and status files. Legacy per-run directories may also be present.
func (this Paths) SupervisorLogs() string {
	return filepath.Join(this.LogsRoot(), "_supervisor")
}

// SupervisorRunLogs returns the legacy stdout/stderr log directory for one
// supervisor process run. New supervisor process logs use SupervisorRunLog.
func (this Paths) SupervisorRunLogs(runID string) string {
	return filepath.Join(this.SupervisorLogs(), runID)
}

// SupervisorRunLog returns the combined stdout/stderr log stream for one
// supervisor process run.
func (this Paths) SupervisorRunLog(runID string) string {
	return filepath.Join(this.SupervisorLogs(), runID+"_log.log")
}

// Component returns paths scoped to a single component.
func (this Paths) Component(name string) ComponentPaths {
	return ComponentPaths{Root: filepath.Join(this.StateDir, name)}
}

// ComponentPaths owns every path under state_dir/<component>/.
type ComponentPaths struct {
	Root string
}

func (this ComponentPaths) Config() string   { return filepath.Join(this.Root, this.dirName()+".yml") }
func (this ComponentPaths) Stable() string   { return filepath.Join(this.Root, "stable.txt") }
func (this ComponentPaths) Current() string  { return filepath.Join(this.Root, "current.txt") }
func (this ComponentPaths) Rejects() string  { return filepath.Join(this.Root, "rejects.txt") }
func (this ComponentPaths) KillSock() string { return filepath.Join(this.Root, "kill.sock") }
func (this ComponentPaths) Versions() string { return filepath.Join(this.Root, "versions") }

// VersionDir is the read-only folder holding an extracted image for one version.
func (this ComponentPaths) VersionDir(version string) string {
	if version == localVersion {
		return this.Root
	}
	return filepath.Join(this.Versions(), version)
}

// LogsDir is the per-version application log directory for this component. The
// child receives it via OP_LOG_DIR and is free to write its own files there.
// Supervisor-captured child stdout/stderr is stored by Log.
func (this ComponentPaths) LogsDir(version string) string {
	return filepath.Join(filepath.Dir(this.Root), "logs", this.dirName(), version)
}

// Log returns the combined stdout/stderr stream captured by the supervisor for
// this component version. It is append-opened across same-version restarts and
// rotated in place.
func (this ComponentPaths) Log(version string) string {
	return filepath.Join(filepath.Dir(this.Root), "logs", this.dirName(), version+"_log.log")
}

func (this ComponentPaths) dirName() string {
	return filepath.Base(this.Root)
}
