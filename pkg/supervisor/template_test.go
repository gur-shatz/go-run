package supervisor_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/pkg/supervisor"
)

var _ = Describe("LaunchVars / EnvSlice", func() {
	vars := supervisor.LaunchVars{
		Version:     "1.4.2",
		VersionDir:  "/var/lib/go-run/api/versions/1.4.2",
		StateDir:    "/var/lib/go-run/api",
		MonitorPort: 38271,
		KillSock:    "/var/lib/go-run/api/kill.sock",
		LogDir:      "/var/lib/go-run/logs/api/1.4.2",
	}

	Describe("EnvSlice", func() {
		It("exports every variable with the OP_ prefix", func() {
			env := supervisor.EnvSlice(vars)
			Expect(env).To(ConsistOf(
				"OP_VERSION=1.4.2",
				"OP_VERSION_DIR=/var/lib/go-run/api/versions/1.4.2",
				"OP_STATE_DIR=/var/lib/go-run/api",
				"OP_MONITOR_PORT=38271",
				"OP_KILL_SOCK=/var/lib/go-run/api/kill.sock",
				"OP_LOG_DIR=/var/lib/go-run/logs/api/1.4.2",
			))
		})
	})
})
