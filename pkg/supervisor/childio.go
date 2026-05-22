package supervisor

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

// newPrefixedWriter returns an io.Writer that prefixes every newline-terminated
// chunk with "[<name>] ". Used to make multi-component pod stdout legible.
func newPrefixedWriter(name string, dst *os.File) io.Writer {
	return &prefixedWriter{prefix: "[" + name + "] ", dst: dst}
}

type prefixedWriter struct {
	mu     sync.Mutex
	prefix string
	dst    *os.File
	buf    bytes.Buffer
}

func (this *prefixedWriter) Write(p []byte) (int, error) {
	this.mu.Lock()
	defer this.mu.Unlock()
	this.buf.Write(p)
	for {
		idx := bytes.IndexByte(this.buf.Bytes(), '\n')
		if idx < 0 {
			break
		}
		line := this.buf.Next(idx + 1)
		_, _ = fmt.Fprintf(this.dst, "%s%s", this.prefix, line)
	}
	return len(p), nil
}

// tryKillSocketSelf is the supervisor-side "ask the child to shut down" call:
// connect to the UDS the supervisor created and write KILL\n. The child is
// expected to be listening on this socket — but the supervisor created it
// itself, so this is just a way to be uniform with external KILL senders.
//
// In practice the supervisor signals shutdown by SIGTERM (see killChild).
// We keep this here so a future "supervisor → child via socket" protocol can
// be slotted in without changing the call site.
func tryKillSocketSelf(path string) {
	conn, err := net.DialTimeout("unix", path, 200*time.Millisecond)
	if err != nil {
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(200 * time.Millisecond))
	w := bufio.NewWriter(conn)
	_, _ = w.WriteString("KILL\n")
	_ = w.Flush()
}
