package backoffice

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/gur-shatz/go-run/pkg/chiutil"
)

// EnvSockPath is the environment variable that holds the UDS path
// for the backoffice server. Set by execrun on the child process.
const EnvSockPath = "GORUN_BACKOFFICE_SOCK"

// AuthScope controls which transports require authentication.
type AuthScope int

const (
	AuthTCPOnly  AuthScope = iota // only protect TCP (default)
	AuthUnixOnly                  // only protect unix socket
	AuthBoth                      // protect both
)

var startTime = time.Now()

// sensitiveKeys are substrings that mark an env var as sensitive.
var sensitiveKeys = []string{"SECRET", "PASSWORD", "TOKEN", "KEY", "CREDENTIAL"}

func isSensitive(name string) bool {
	upper := strings.ToUpper(name)
	for _, k := range sensitiveKeys {
		if strings.Contains(upper, k) {
			return true
		}
	}
	return false
}

func handleEnv(w http.ResponseWriter, r *http.Request) {
	env := make(map[string]string)
	for _, e := range os.Environ() {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) != 2 {
			continue
		}
		name, value := parts[0], parts[1]
		if isSensitive(name) {
			value = "***"
		}
		env[name] = value
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(env)
}

func handleInfo(w http.ResponseWriter, r *http.Request) {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	info := map[string]interface{}{
		"pid":            os.Getpid(),
		"uptime":         time.Since(startTime).String(),
		"go_version":     runtime.Version(),
		"os":             runtime.GOOS,
		"arch":           runtime.GOARCH,
		"num_goroutines": runtime.NumGoroutine(),
		"num_cpu":        runtime.NumCPU(),
		"memory": map[string]interface{}{
			"alloc":       mem.Alloc,
			"total_alloc": mem.TotalAlloc,
			"sys":         mem.Sys,
			"num_gc":      mem.NumGC,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

// Backoffice holds the backoffice HTTP server with its route folder.
// Create with New(), register custom routes via Folder(), then call
// ListenAndServe or ListenAndServeBackground.
type Backoffice struct {
	router chi.Router
	folder *chiutil.RouteFolder

	authUser  string
	authPass  string
	authScope AuthScope
	authSet   bool
}

// New creates a new Backoffice instance with built-in routes
// (env, info, debug/pprof) already registered.
func New() *Backoffice {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	folder := chiutil.NewRouteFolderOn(r, "/")
	folder.ServiceName("Backoffice")

	// Built-in routes
	folder.GetDesc("/env", "Environment variables (sensitive values masked)", handleEnv)
	folder.GetDesc("/info", "Process and runtime information", handleInfo)

	// pprof is registered on the top-level router because pprof.Index
	// expects to see the full /debug/pprof/ path in the request.
	folder.ExternalLink("/debug/pprof", "Go profiling (pprof)")
	r.HandleFunc("/debug/pprof/*", pprof.Index)
	r.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	r.HandleFunc("/debug/pprof/profile", pprof.Profile)
	r.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	r.HandleFunc("/debug/pprof/trace", pprof.Trace)

	return &Backoffice{router: r, folder: folder}
}

// Folder returns the root RouteFolder for registering custom routes and sub-folders.
func (this *Backoffice) Folder() *chiutil.RouteFolder {
	return this.folder
}

// SetAuth enables HTTP Basic Auth on the backoffice server.
// scope defaults to AuthTCPOnly if omitted.
func (this *Backoffice) SetAuth(username, password string, scope ...AuthScope) {
	this.authUser = username
	this.authPass = password
	this.authSet = true
	this.authScope = AuthTCPOnly
	if len(scope) > 0 {
		this.authScope = scope[0]
	}
}

// buildHandler wraps this.router with auth middleware if required for the given transport.
// transport is "tcp" or "unix".
func (this *Backoffice) buildHandler(transport string) http.Handler {
	if !this.authSet {
		return this.router
	}

	needAuth := false
	switch this.authScope {
	case AuthTCPOnly:
		needAuth = transport == "tcp"
	case AuthUnixOnly:
		needAuth = transport == "unix"
	case AuthBoth:
		needAuth = true
	}

	if !needAuth {
		return this.router
	}

	wantUser := []byte(this.authUser)
	wantPass := []byte(this.authPass)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok ||
			subtle.ConstantTimeCompare([]byte(user), wantUser) != 1 ||
			subtle.ConstantTimeCompare([]byte(pass), wantPass) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="backoffice"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		this.router.ServeHTTP(w, r)
	})
}

func (this *Backoffice) serve(ctx context.Context, ln net.Listener, transport string) error {
	srv := &http.Server{Handler: this.buildHandler(transport)}

	go func() {
		<-ctx.Done()
		srv.Close()
	}()

	err := srv.Serve(ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// ListenAndServe starts the backoffice HTTP server on the UDS specified
// by the GORUN_BACKOFFICE_SOCK environment variable. If the env var is
// not set, it returns nil immediately (safe to call outside go-run).
// Blocks until ctx is cancelled or an error occurs.
func (this *Backoffice) ListenAndServe(ctx context.Context) error {
	sockPath := os.Getenv(EnvSockPath)
	if sockPath == "" {
		return nil
	}

	// Remove stale socket file if it exists
	os.Remove(sockPath)

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return fmt.Errorf("backoffice listen unix: %w", err)
	}

	return this.serve(ctx, ln, "unix")
}

// ListenAndServeBackground starts the backoffice server in a background
// goroutine. Errors are silently ignored (best-effort). The server shuts
// down when ctx is cancelled.
func (this *Backoffice) ListenAndServeBackground(ctx context.Context) {
	go this.ListenAndServe(ctx)
}

// ListenAndServeTCP starts the backoffice HTTP server on a TCP address.
// Blocks until ctx is cancelled or an error occurs.
func (this *Backoffice) ListenAndServeTCP(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("backoffice listen tcp: %w", err)
	}

	return this.serve(ctx, ln, "tcp")
}

// ListenAndServeTCPBackground starts the backoffice TCP server in a background
// goroutine. Errors are silently ignored (best-effort). The server shuts
// down when ctx is cancelled.
func (this *Backoffice) ListenAndServeTCPBackground(ctx context.Context, addr string) {
	go this.ListenAndServeTCP(ctx, addr)
}
