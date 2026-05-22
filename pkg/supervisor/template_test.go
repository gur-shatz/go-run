package supervisor_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/pkg/supervisor"
)

var _ = Describe("Template expansion", func() {
	vars := supervisor.LaunchVars{
		Version:     "1.4.2",
		VersionDir:  "/var/lib/go-run/api/versions/1.4.2",
		StateDir:    "/var/lib/go-run/api",
		MonitorPort: 38271,
		KillSock:    "/var/lib/go-run/api/kill.sock",
	}

	Describe("ExpandCommand", func() {
		It("substitutes every supported variable and splits into argv", func() {
			argv, err := supervisor.ExpandCommand(
				"${VERSION_DIR}/bin/api --monitor=:${MONITOR_PORT} --kill=${KILL_SOCK} --v=${VERSION}",
				vars,
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(argv).To(Equal([]string{
				"/var/lib/go-run/api/versions/1.4.2/bin/api",
				"--monitor=:38271",
				"--kill=/var/lib/go-run/api/kill.sock",
				"--v=1.4.2",
			}))
		})

		It("returns an error naming an unknown variable", func() {
			_, err := supervisor.ExpandCommand("${VERSION_DIR}/bin/api --weird=${MYSTERY}", vars)
			Expect(err).To(MatchError(ContainSubstring("MYSTERY")))
		})

		It("preserves quoted arguments", func() {
			argv, err := supervisor.ExpandCommand(
				`${VERSION_DIR}/bin/api --motd="hello world"`,
				vars,
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(argv).To(Equal([]string{
				"/var/lib/go-run/api/versions/1.4.2/bin/api",
				"--motd=hello world",
			}))
		})

		It("errors on empty command", func() {
			_, err := supervisor.ExpandCommand("   ", vars)
			Expect(err).To(MatchError(ContainSubstring("empty command")))
		})
	})

	Describe("EnvSlice", func() {
		It("exports every variable with the OP_ prefix", func() {
			env := supervisor.EnvSlice(vars)
			Expect(env).To(ConsistOf(
				"OP_VERSION=1.4.2",
				"OP_VERSION_DIR=/var/lib/go-run/api/versions/1.4.2",
				"OP_STATE_DIR=/var/lib/go-run/api",
				"OP_MONITOR_PORT=38271",
				"OP_KILL_SOCK=/var/lib/go-run/api/kill.sock",
			))
		})
	})
})
