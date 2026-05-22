package supervisor_test

import (
	"net"
	"runtime"

	. "github.com/onsi/gomega"
)

var (
	goosForTest   = runtime.GOOS
	goarchForTest = runtime.GOARCH
)

// freeTCPPort grabs an ephemeral 127.0.0.1 port and closes the listener so the
// supervisor under test can hand it to the child. There is a small TOCTOU
// window in principle but it's fine for a quiet test host.
func freeTCPPort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	Expect(err).NotTo(HaveOccurred())
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
