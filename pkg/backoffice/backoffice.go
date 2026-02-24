package backoffice

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

// EnvSockPath is the environment variable that holds the UDS path
// for the backoffice server. Set by execrun on the child process.
const EnvSockPath = "GORUN_BACKOFFICE_SOCK"

// StatusInfo is the JSON-serializable status returned by GET /status.
type StatusInfo struct {
	Ready    bool              `json:"ready"`
	Status   string            `json:"status,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

var startTime = time.Now()

// singleton state
var (
	mu       sync.RWMutex
	ready    bool
	status   string
	metadata map[string]string
)

// SetReady sets the ready flag.
func SetReady(r bool) {
	mu.Lock()
	ready = r
	mu.Unlock()
}

// SetStatus sets the status string.
func SetStatus(s string) {
	mu.Lock()
	status = s
	mu.Unlock()
}

// SetMetadata replaces the metadata map.
func SetMetadata(m map[string]string) {
	mu.Lock()
	metadata = m
	mu.Unlock()
}

// SetMetadataKey sets a single metadata key.
func SetMetadataKey(key, value string) {
	mu.Lock()
	if metadata == nil {
		metadata = make(map[string]string)
	}
	metadata[key] = value
	mu.Unlock()
}

// GetStatus returns a snapshot of the current status.
func GetStatus() StatusInfo {
	mu.RLock()
	defer mu.RUnlock()

	var md map[string]string
	if len(metadata) > 0 {
		md = make(map[string]string, len(metadata))
		for k, v := range metadata {
			md[k] = v
		}
	}

	return StatusInfo{
		Ready:    ready,
		Status:   status,
		Metadata: md,
	}
}

// resetState resets the singleton state. Used in tests.
func resetState() {
	mu.Lock()
	ready = false
	status = ""
	metadata = nil
	mu.Unlock()
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(GetStatus())
}

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

var builtinRoutes = []struct {
	Path string
	Desc string
}{
	{"status", "Health / readiness status"},
	{"env", "Environment variables (sensitive values masked)"},
	{"info", "Process and runtime information"},
	{"debug/pprof/", "Go profiling (pprof)"},
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Backoffice</title></head><body>`)
	fmt.Fprint(w, `<h1>Backoffice</h1><ul>`)

	// built-in routes
	for _, rt := range builtinRoutes {
		fmt.Fprintf(w, `<li><a href="%s">%s</a> — %s</li>`, rt.Path, rt.Path, rt.Desc)
	}

	// user routes
	sorted := make([]string, 0, len(userRoutePaths))
	for _, p := range userRoutePaths {
		sorted = append(sorted, p)
	}
	sort.Strings(sorted)
	for _, p := range sorted {
		rel := strings.TrimPrefix(p, "/")
		fmt.Fprintf(w, `<li><a href="%s">%s</a> (custom)</li>`, rel, p)
	}

	fmt.Fprint(w, `</ul></body></html>`)
}

// userRoutePaths tracks paths registered by the user router for the index page.
var userRoutePaths []string

// ListenAndServe starts the backoffice HTTP server on the UDS specified
// by the GORUN_BACKOFFICE_SOCK environment variable. If the env var is
// not set, it returns nil immediately (safe to call outside go-run).
// userRouter is mounted at / for custom endpoints; pass nil for no custom routes.
// Blocks until ctx is cancelled or an error occurs.
func ListenAndServe(ctx context.Context, userRouter chi.Router) error {
	sockPath := os.Getenv(EnvSockPath)
	if sockPath == "" {
		return nil
	}

	r := chi.NewRouter()

	// Built-in routes
	r.Get("/", handleIndex)
	r.Get("/status", handleStatus)
	r.Get("/env", handleEnv)
	r.Get("/info", handleInfo)
	r.HandleFunc("/debug/pprof/*", pprof.Index)
	r.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	r.HandleFunc("/debug/pprof/profile", pprof.Profile)
	r.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	r.HandleFunc("/debug/pprof/trace", pprof.Trace)

	// User-provided routes
	if userRouter != nil {
		// Collect user route paths for the index page
		userRoutePaths = nil
		chi.Walk(userRouter, func(method, route string, handler http.Handler, middlewares ...func(http.Handler) http.Handler) error {
			if method == "GET" {
				userRoutePaths = append(userRoutePaths, route)
			}
			return nil
		})
		r.Mount("/", userRouter)
	}

	// Remove stale socket file if it exists
	os.Remove(sockPath)

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return fmt.Errorf("backoffice listen: %w", err)
	}

	srv := &http.Server{Handler: r}

	go func() {
		<-ctx.Done()
		srv.Close()
	}()

	err = srv.Serve(ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// ListenAndServeBackground starts the backoffice server in a background
// goroutine. Errors are silently ignored (best-effort). The server shuts
// down when ctx is cancelled.
func ListenAndServeBackground(ctx context.Context, userRouter chi.Router) {
	go ListenAndServe(ctx, userRouter)
}
