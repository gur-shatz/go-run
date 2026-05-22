// hello is a tiny example component for the supervisor demo. It uses statekit
// for its /state (YAML) and /metrics (Prometheus) surfaces so the supervisor
// can scrape it the same way it would any other statekit-instrumented
// component. /healthz and /readyz remain plain HTTP for the supervisor's
// liveness probe.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gur-shatz/statekit"
)

func main() {
	port := flag.Int("port", 0, "monitor port (overrides OP_MONITOR_PORT)")
	flag.Parse()

	if *port == 0 {
		if v := os.Getenv("OP_MONITOR_PORT"); v != "" {
			n, _ := strconv.Atoi(v)
			*port = n
		}
	}
	if *port == 0 {
		log.Fatal("hello: monitor port is required (--port or OP_MONITOR_PORT)")
	}

	version := os.Getenv("OP_VERSION")
	if version == "" {
		version = "unknown"
	}

	// Greeting is loaded from ./greeting.txt (we run with cwd = version
	// dir, so it sits right next to the binary). The supervisor renders it
	// from greeting.txt.tmpl every launch, with supervisor.yml `vars:`
	// overriding defaults.yml.
	greeting := "Hello (no greeting.txt — render didn't run?)"
	if data, err := os.ReadFile("greeting.txt"); err == nil {
		greeting = strings.TrimRight(string(data), "\n\r ")
	}
	log.Printf("hello: greeting=%q", greeting)

	// statekit registry — one ManualState reflecting the component's view of
	// itself plus two metrics that exercise both counter and gauge paths.
	reg := statekit.NewRegistry(
		statekit.WithLabel("component", "hello"),
		statekit.WithLabel("version", version),
	)

	appState := statekit.NewManualState("hello")
	appState.Pass("started", map[string]any{"version": version})
	_ = reg.Register(appState)

	requests := statekit.NewCounter("hello_requests_total", "HTTP requests served by /healthz and /readyz.")
	startedAt := statekit.NewGauge("hello_started_at_unix", "Process start time as a Unix timestamp.")
	startedAt.Set(time.Now().Unix())
	_ = reg.RegisterCollectors(requests, startedAt)

	mux := http.NewServeMux()

	// statekit owns /state (YAML) and /metrics (Prometheus text).
	reg.Mount(mux, "/")

	// /healthz and /readyz stay as tiny plain handlers — the supervisor's
	// liveness probe targets /healthz directly.
	var requestCounter atomic.Int64
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		requests.Inc()
		requestCounter.Add(1)
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		requests.Inc()
		fmt.Fprintln(w, "ready")
	})
	mux.HandleFunc("/greet", func(w http.ResponseWriter, _ *http.Request) {
		requests.Inc()
		fmt.Fprintln(w, greeting)
	})

	addr := net.JoinHostPort("0.0.0.0", strconv.Itoa(*port))
	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		log.Printf("hello: listening on %s (version=%s)", addr, version)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("hello: serve: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	appState.Down("shutdown signal received", nil)
	log.Print("hello: shutting down")
	_ = srv.Close()
}
