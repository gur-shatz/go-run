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
	"sort"
	"strings"
	"sync"
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

// ServiceLevel represents the severity level of a service.
type ServiceLevel int

const (
	OK                ServiceLevel = 0
	RunningWithErrors ServiceLevel = 1
	Degraded          ServiceLevel = 2
	Down              ServiceLevel = 3
)

var serviceLevelNames = map[ServiceLevel]string{
	OK:                "OK",
	RunningWithErrors: "RUNNING_WITH_ERRORS",
	Degraded:          "DEGRADED",
	Down:              "DOWN",
}

var serviceLevelByName = map[string]ServiceLevel{
	"OK":                  OK,
	"RUNNING_WITH_ERRORS": RunningWithErrors,
	"DEGRADED":            Degraded,
	"DOWN":                Down,
}

func (this ServiceLevel) String() string {
	if name, ok := serviceLevelNames[this]; ok {
		return name
	}
	return fmt.Sprintf("ServiceLevel(%d)", int(this))
}

func (this ServiceLevel) MarshalJSON() ([]byte, error) {
	if name, ok := serviceLevelNames[this]; ok {
		return json.Marshal(name)
	}
	return nil, fmt.Errorf("unknown ServiceLevel: %d", int(this))
}

func (this *ServiceLevel) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	if level, ok := serviceLevelByName[s]; ok {
		*this = level
		return nil
	}
	return fmt.Errorf("unknown ServiceLevel: %q", s)
}

// HistoryEntry records a level change for a service.
type HistoryEntry struct {
	Timestamp time.Time    `json:"timestamp"`
	Level     ServiceLevel `json:"level"`
	Data      any          `json:"data,omitempty"`
}

// ServiceStatusInfo is the per-service status in the JSON response.
type ServiceStatusInfo struct {
	Name        string         `json:"name"`
	Level       ServiceLevel   `json:"level"`
	Critical    bool           `json:"critical"`
	TimeInState string         `json:"time_in_state"`
	Data        any            `json:"data,omitempty"`
	History     []HistoryEntry `json:"history"`
	UptimePct   float64        `json:"uptime_pct"`
}

// StatusInfo is the JSON-serializable status returned by GET /status.
type StatusInfo struct {
	GlobalLevel ServiceLevel        `json:"global_level"`
	CausedBy    string              `json:"caused_by"`
	Services    []ServiceStatusInfo `json:"services"`
}

var startTime = time.Now()

// singleton state
var (
	mu       sync.RWMutex
	services = map[string]*serviceState{}
	// timeNow is used for testing; defaults to time.Now
	timeNow = time.Now
)

const maxHistory = 10

type serviceState struct {
	name       string
	critical   bool
	level      ServiceLevel
	data       any
	lastChange time.Time
	history    []HistoryEntry
	created    time.Time
	errorTime  time.Duration // accumulated Degraded/Down time
	lastTick   time.Time     // last time errorTime was updated
}

// ServiceHandle is a handle returned by CreateServiceStatus for updating a service's status.
type ServiceHandle struct {
	name string
}

// SetStatus updates the service's level and data.
func (this *ServiceHandle) SetStatus(level ServiceLevel, data any) {
	mu.Lock()
	defer mu.Unlock()

	svc, ok := services[this.name]
	if !ok {
		return
	}
	setServiceStatus(svc, level, data)
}

func setServiceStatus(svc *serviceState, level ServiceLevel, data any) {
	now := timeNow()

	// Accumulate error time if old level was Degraded or Down
	if svc.level >= Degraded {
		svc.errorTime += now.Sub(svc.lastTick)
	}

	// If level changed, append to history
	if level != svc.level {
		entry := HistoryEntry{
			Timestamp: now,
			Level:     level,
			Data:      data,
		}
		svc.history = append(svc.history, entry)
		if len(svc.history) > maxHistory {
			svc.history = svc.history[len(svc.history)-maxHistory:]
		}
		svc.lastChange = now
	}

	svc.level = level
	svc.data = data
	svc.lastTick = now
}

// CreateServiceStatus registers a new service. Starts at OK.
// critical=true means it can push global level to any severity.
// critical=false caps its contribution to global at RunningWithErrors.
func CreateServiceStatus(name string, critical bool) *ServiceHandle {
	mu.Lock()
	defer mu.Unlock()

	now := timeNow()
	svc := &serviceState{
		name:       name,
		critical:   critical,
		level:      OK,
		lastChange: now,
		created:    now,
		lastTick:   now,
		history:    []HistoryEntry{{Timestamp: now, Level: OK}},
	}
	services[name] = svc

	return &ServiceHandle{name: name}
}

// GetStatus returns a snapshot of the current status.
func GetStatus() StatusInfo {
	mu.RLock()
	defer mu.RUnlock()

	now := timeNow()

	// Sort service names for deterministic output
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)

	svcInfos := make([]ServiceStatusInfo, 0, len(services))
	globalLevel := OK
	causedBy := ""

	for _, name := range names {
		svc := services[name]

		// Compute uptime
		totalTime := now.Sub(svc.created)
		errorTime := svc.errorTime
		if svc.level >= Degraded {
			errorTime += now.Sub(svc.lastTick)
		}
		var uptimePct float64
		if totalTime > 0 {
			uptimePct = 100.0 * (1.0 - float64(errorTime)/float64(totalTime))
			if uptimePct < 0 {
				uptimePct = 0
			}
		} else {
			uptimePct = 100.0
		}

		// Time in state
		timeInState := now.Sub(svc.lastChange).Round(time.Second).String()

		// Copy history
		history := make([]HistoryEntry, len(svc.history))
		copy(history, svc.history)

		svcInfos = append(svcInfos, ServiceStatusInfo{
			Name:        svc.name,
			Level:       svc.level,
			Critical:    svc.critical,
			TimeInState: timeInState,
			Data:        svc.data,
			History:     history,
			UptimePct:   uptimePct,
		})

		// Compute contribution to global level
		contribution := svc.level
		if !svc.critical && contribution > RunningWithErrors {
			contribution = RunningWithErrors
		}
		if contribution > globalLevel {
			globalLevel = contribution
			causedBy = name
		}
	}

	return StatusInfo{
		GlobalLevel: globalLevel,
		CausedBy:    causedBy,
		Services:    svcInfos,
	}
}

// ResetStateForTest resets the singleton state. Used in tests.
func ResetStateForTest() {
	mu.Lock()
	services = map[string]*serviceState{}
	timeNow = time.Now
	mu.Unlock()
}

// SetTimeNowForTest overrides the time function. Used in tests.
func SetTimeNowForTest(fn func() time.Time) {
	mu.Lock()
	timeNow = fn
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
// (status, env, info, debug/pprof) already registered.
func New() *Backoffice {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	folder := chiutil.NewRouteFolderOn(r, "/")
	folder.ServiceName("Backoffice")

	// Built-in routes
	folder.GetDesc("/status", "Health / readiness status", handleStatus)
	folder.GetDesc("/env", "Environment variables (sensitive values masked)", handleEnv)
	folder.GetDesc("/info", "Process and runtime information", handleInfo)

	// pprof is registered on the top-level router because pprof.Index
	// expects to see the full /debug/pprof/ path in the request.
	folder.Link("/debug/pprof", "Go profiling (pprof)")
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
