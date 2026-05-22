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

func (this ComponentPaths) dirName() string {
	return filepath.Base(this.Root)
}
