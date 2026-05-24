package supervisor

import "fmt"

// LaunchVars holds the launch facts the supervisor exposes to children.
// They are exported as OP_*-prefixed environment variables (see EnvSlice)
// and as top-level keys in the *.tmpl render context (see LaunchVars.asMap
// in render.go).
//
// They are NOT substituted into the component's command argv — the command
// is parsed verbatim and the child's working directory is set to the
// version dir, so a relative `./bin/hello` resolves naturally.
type LaunchVars struct {
	Version     string
	VersionDir  string
	StateDir    string
	MonitorPort int
	KillSock    string
	// LogDir is the per-version log directory the supervisor manages on the
	// child's behalf. The supervisor writes stdout.log + stderr.log here;
	// the child may write any other files it wants in this directory.
	LogDir string
}

// EnvSlice returns the OP_* environment variables a child can read.
// Format: KEY=VALUE strings suitable for exec.Cmd.Env.
func EnvSlice(vars LaunchVars) []string {
	return []string{
		"OP_VERSION=" + vars.Version,
		"OP_VERSION_DIR=" + vars.VersionDir,
		"OP_STATE_DIR=" + vars.StateDir,
		fmt.Sprintf("OP_MONITOR_PORT=%d", vars.MonitorPort),
		"OP_KILL_SOCK=" + vars.KillSock,
		"OP_LOG_DIR=" + vars.LogDir,
	}
}
