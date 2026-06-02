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
		It("exports supervisor-native names and artifact-compatible aliases", func() {
			env := supervisor.EnvSlice(vars)
			Expect(env).To(ConsistOf(
				"OP_VERSION=1.4.2",
				"OP_VERSION_DIR=/var/lib/go-run/api/versions/1.4.2",
				"OP_STATE_DIR=/var/lib/go-run/api",
				"OP_MONITOR_PORT=38271",
				"OP_KILL_SOCK=/var/lib/go-run/api/kill.sock",
				"OP_LOG_DIR=/var/lib/go-run/logs/api/1.4.2",
				"VERSION=1.4.2",
				"REQUIRED_VERSION=1.4.2",
				"VERSION_DIR=/var/lib/go-run/api/versions/1.4.2",
				"BUILD_DIR=/var/lib/go-run/api/versions/1.4.2",
				"BUILDDIR=/var/lib/go-run/api/versions/1.4.2",
				"STATE_DIR=/var/lib/go-run/api",
				"MONITOR_PORT=38271",
				"KILL_SOCK=/var/lib/go-run/api/kill.sock",
				"LOG_DIR=/var/lib/go-run/logs/api/1.4.2",
			))
		})
	})
})
