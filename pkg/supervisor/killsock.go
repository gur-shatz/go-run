package supervisor

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
)

// KillSocket is a per-component Unix-domain-socket listener that fires a
// callback when a peer writes "KILL\n". The supported invocation is a single
// fire-and-forget; further connections after the first KILL are accepted but
// ignored. The supervisor relies on filesystem permissions (0600, owned by
// the supervisor's UID) to gate access — no shared secret.
type KillSocket struct {
	path     string
	listener net.Listener
	onceKill sync.Once
	onKill   func()
	closed   chan struct{}
}

// ListenKillSocket creates the UDS at path with mode 0600. The supplied
// onKill callback is invoked at most once, the first time a "KILL\n" line is
// received. The listener runs in a goroutine until Close is called.
func ListenKillSocket(path string, onKill func()) (*KillSocket, error) {
	if onKill == nil {
		return nil, errors.New("ListenKillSocket: onKill is required")
	}
	// Remove any leftover socket from a prior run; ignore "not exist".
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove stale socket %s: %w", path, err)
	}

	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", path, err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		_ = l.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("chmod %s: %w", path, err)
	}

	this := &KillSocket{
		path:     path,
		listener: l,
		onKill:   onKill,
		closed:   make(chan struct{}),
	}
	go this.serve()
	return this, nil
}

// Close stops accepting connections and removes the socket file. Safe to call
// multiple times.
func (this *KillSocket) Close() error {
	select {
	case <-this.closed:
		return nil
	default:
	}
	close(this.closed)
	err := this.listener.Close()
	_ = os.Remove(this.path)
	return err
}

// Path returns the absolute socket path the listener was created at.
func (this *KillSocket) Path() string { return this.path }

func (this *KillSocket) serve() {
	for {
		conn, err := this.listener.Accept()
		if err != nil {
			select {
			case <-this.closed:
				return
			default:
				// Best effort: a temporary error shouldn't kill the listener.
				if errors.Is(err, net.ErrClosed) {
					return
				}
				continue
			}
		}
		go this.handle(conn)
	}
}

func (this *KillSocket) handle(conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	line, _ := br.ReadString('\n')
	if strings.TrimRight(line, "\r\n") == "KILL" {
		this.onceKill.Do(this.onKill)
	}
}
