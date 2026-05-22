package supervisor

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/google/shlex"
)

// LaunchVars holds the values substituted into a component's command template
// at launch time. They are also exported as OP_* environment variables to
// children that prefer env over argv.
type LaunchVars struct {
	Version     string
	VersionDir  string
	StateDir    string
	MonitorPort int
	KillSock    string
}

// supported is the set of variable names recognised by ExpandCommand and
// included in EnvSlice. Anything else in the template is an error.
var supportedTemplateVars = []string{
	"VERSION",
	"VERSION_DIR",
	"STATE_DIR",
	"MONITOR_PORT",
	"KILL_SOCK",
}

// ExpandCommand expands the shell-style ${VAR} placeholders in cmd against
// vars and returns the resulting argv. Unknown variables are an error rather
// than being silently substituted as empty (which would mask config typos).
func ExpandCommand(cmd string, vars LaunchVars) ([]string, error) {
	expanded, err := expandTemplate(cmd, vars)
	if err != nil {
		return nil, err
	}
	argv, err := shlex.Split(expanded)
	if err != nil {
		return nil, fmt.Errorf("split command: %w", err)
	}
	if len(argv) == 0 {
		return nil, fmt.Errorf("empty command after expansion")
	}
	return argv, nil
}

// EnvSlice returns the OP_* environment variables a child can read instead of
// parsing argv. Format: KEY=VALUE strings suitable for exec.Cmd.Env.
func EnvSlice(vars LaunchVars) []string {
	return []string{
		"OP_VERSION=" + vars.Version,
		"OP_VERSION_DIR=" + vars.VersionDir,
		"OP_STATE_DIR=" + vars.StateDir,
		fmt.Sprintf("OP_MONITOR_PORT=%d", vars.MonitorPort),
		"OP_KILL_SOCK=" + vars.KillSock,
	}
}

// expandTemplate runs os.Expand against vars, returning an error if the template
// references a name outside the supported set.
func expandTemplate(s string, vars LaunchVars) (string, error) {
	var missing []string
	out := os.Expand(s, func(name string) string {
		switch name {
		case "VERSION":
			return vars.Version
		case "VERSION_DIR":
			return vars.VersionDir
		case "STATE_DIR":
			return vars.StateDir
		case "MONITOR_PORT":
			return fmt.Sprintf("%d", vars.MonitorPort)
		case "KILL_SOCK":
			return vars.KillSock
		default:
			missing = append(missing, name)
			return ""
		}
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("unknown template variable(s): %s (supported: %s)",
			strings.Join(missing, ", "),
			strings.Join(supportedTemplateVars, ", "))
	}
	return out, nil
}

// SupportedTemplateVars returns a copy of the supported variable name set.
// Exposed for diagnostics and tests.
func SupportedTemplateVars() []string {
	return slices.Clone(supportedTemplateVars)
}
