package supervisor

import "path/filepath"

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

// LogsRoot is the per-supervisor top-level logs directory:
// state_dir/logs/<component>/<version>/.
func (this Paths) LogsRoot() string {
	return filepath.Join(this.StateDir, "logs")
}

// LogsForComponent returns the per-component log directory:
// state_dir/logs/<component>/. Used by HTTP routes that browse the
// whole tree for one component.
func (this Paths) LogsForComponent(name string) string {
	return filepath.Join(this.LogsRoot(), name)
}

// LogsForVersion returns the per-version log directory the child writes to:
// state_dir/logs/<component>/<version>/. The supervisor writes stdout.log
// and stderr.log here; the child may write its own app log files alongside.
func (this Paths) LogsForVersion(name, version string) string {
	return filepath.Join(this.LogsRoot(), name, version)
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
	return filepath.Join(this.Versions(), version)
}

// LogsDir is the per-version log directory for this component. The supervisor
// writes stdout.log and stderr.log here (rotating); the child receives the
// same path via OP_LOG_DIR and is free to write its own app log files
// alongside. It lives outside the version dir so logs survive a version
// folder GC of an obsolete release if the operator wants to keep them.
func (this ComponentPaths) LogsDir(version string) string {
	return filepath.Join(filepath.Dir(this.Root), "logs", this.dirName(), version)
}

func (this ComponentPaths) dirName() string {
	return filepath.Base(this.Root)
}
