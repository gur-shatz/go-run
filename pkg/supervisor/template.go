package supervisor

import (
	"fmt"
	"sort"
	"strings"
)

// LaunchVars holds the launch facts the supervisor exposes to children.
// They are exported as OP_*-prefixed environment variables (see EnvSlice)
// and as top-level keys in manifest validate_templates checks.
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
	// LogDir is the per-version application log directory the supervisor
	// creates on the child's behalf. The child may write any files it wants in
	// this directory.
	LogDir string
}

// EnvMap returns the launch environment variables a child can read.
//
// OP_* names are the supervisor-native contract. The unprefixed aliases line
// up with artifact-build conventions, which lets a locally-built version
// directory run under supervisor without a separate dev config vocabulary.
func EnvMap(vars LaunchVars) map[string]string {
	monitorPort := fmt.Sprintf("%d", vars.MonitorPort)
	return map[string]string{
		"OP_VERSION":      vars.Version,
		"OP_VERSION_DIR":  vars.VersionDir,
		"OP_STATE_DIR":    vars.StateDir,
		"OP_MONITOR_PORT": monitorPort,
		"OP_KILL_SOCK":    vars.KillSock,
		"OP_LOG_DIR":      vars.LogDir,

		"VERSION":          vars.Version,
		"REQUIRED_VERSION": vars.Version,
		"VERSION_DIR":      vars.VersionDir,
		"BUILD_DIR":        vars.VersionDir,
		"BUILDDIR":         vars.VersionDir,
		"STATE_DIR":        vars.StateDir,
		"MONITOR_PORT":     monitorPort,
		"KILL_SOCK":        vars.KillSock,
		"LOG_DIR":          vars.LogDir,
	}
}

// EnvSlice returns the launch environment variables a child can read.
// Format: KEY=VALUE strings suitable for exec.Cmd.Env.
func EnvSlice(vars LaunchVars) []string {
	env := EnvMap(vars)
	return []string{
		"OP_VERSION=" + env["OP_VERSION"],
		"OP_VERSION_DIR=" + env["OP_VERSION_DIR"],
		"OP_STATE_DIR=" + env["OP_STATE_DIR"],
		"OP_MONITOR_PORT=" + env["OP_MONITOR_PORT"],
		"OP_KILL_SOCK=" + env["OP_KILL_SOCK"],
		"OP_LOG_DIR=" + env["OP_LOG_DIR"],
		"VERSION=" + env["VERSION"],
		"REQUIRED_VERSION=" + env["REQUIRED_VERSION"],
		"VERSION_DIR=" + env["VERSION_DIR"],
		"BUILD_DIR=" + env["BUILD_DIR"],
		"BUILDDIR=" + env["BUILDDIR"],
		"STATE_DIR=" + env["STATE_DIR"],
		"MONITOR_PORT=" + env["MONITOR_PORT"],
		"KILL_SOCK=" + env["KILL_SOCK"],
		"LOG_DIR=" + env["LOG_DIR"],
	}
}

func environSliceToMap(items []string) map[string]string {
	env := make(map[string]string, len(items))
	for _, item := range items {
		if k, v, ok := strings.Cut(item, "="); ok {
			env[k] = v
		}
	}
	return env
}

func envMapToSlice(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}
