package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gur-shatz/go-run/pkg/backoffice"
	"github.com/gur-shatz/go-run/pkg/backoffice/logviewer"
)

func main() {
	port := os.Getenv("DEMO_PORT")
	if port == "" {
		fmt.Fprintln(os.Stderr, "error: DEMO_PORT environment variable is required")
		os.Exit(1)
	}
	backofficeAddr := os.Getenv("BACKOFFICE_ADDR")
	if backofficeAddr == "" {
		backofficeAddr = ":9090"
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// Set up backoffice with custom endpoints.
	// If not running under go-run, ListenAndServeBackground is a no-op.
	bo := backoffice.New()
	app := bo.Folder()

	logRoot, err := ensureDemoLogs()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error creating demo logs: %v\n", err)
		os.Exit(1)
	}
	if _, err := logviewer.Mount(app, "logs", logviewer.Options{Root: logRoot}); err != nil {
		fmt.Fprintf(os.Stderr, "error creating log viewer: %v\n", err)
		os.Exit(1)
	}

	app.GetDesc("/debug", "Application debug information", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"uptime":  time.Since(startTime).String(),
			"pid":     os.Getpid(),
			"version": "0.1.0",
		})
	})
	app.GetDesc("/config", "Application configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"port":      port,
			"log_level": "info",
			"cache_ttl": "5m",
		})
	})

	app.GetDesc("/connections", "Active connections and pools", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"database": map[string]any{"host": "localhost:5432", "pool_size": 10, "active": 3},
			"cache":    map[string]any{"host": "localhost:6379", "connected": true},
		})
	})
	bo.SetAuth("admin", "admin123")
	bo.ListenAndServeBackground(ctx)
	bo.ListenAndServeTCPBackground(ctx, backofficeAddr)
	fmt.Printf("backoffice-demo: backoffice TCP on %s (user: admin)\n", backofficeAddr)
	fmt.Printf("backoffice-demo: generated logs in %s\n", logRoot)

	// Main HTTP server
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "backoffice-demo running on port %s\n", port)
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	srv := &http.Server{Addr: ":" + port, Handler: mux}
	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background())
	}()

	fmt.Printf("backoffice-demo listening on :%s\n", port)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

var startTime = time.Now()

func ensureDemoLogs() (string, error) {
	root := filepath.Join(".", "generated-logs")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}

	files := map[string][]string{
		"gateway.log.2": {
			`2026-07-10T09:58:01.104Z [34mINFO[0m boot gateway/gateway_main.go:211 starting gateway listener address=:8080 component=gateway`,
			`2026-07-10T09:58:05.218Z [34mINFO[0m mcp gateway/mcp.go:88 registered MCP route path=/mcp tools=12`,
			`2026-07-10T09:59:43.612Z [33mWARN[0m auth gateway/auth.go:144 missing authorization method=POST path=/mcp status=401 user_agent="Demo Client"`,
		},
		"gateway.log.1": {
			`2026-07-10T10:00:06.310Z [34mINFO[0m boot gateway/gateway_main.go:558 Gateway front-door request method=POST host=gateway.firegatenetworks.com path=/mcp route="/mcp" status=401 bytes=280 duration=238.122us has_authorization=false user_agent="Claude-User"`,
			`2026-07-10T10:01:12.454Z [34mINFO[0m auth gateway/auth.go:203 accepted service token method=POST path=/mcp status=200 account=acme duration=3.441ms`,
			`2026-07-10T10:02:44.921Z [31mERROR[0m proxy gateway/proxy.go:77 upstream timeout method=GET path=/api/search status=504 upstream=search duration=5.001s`,
		},
		"gateway.log": {
			`2026-07-10T10:03:00.003Z [34mINFO[0m proxy gateway/proxy.go:111 upstream response method=GET path=/health status=200 bytes=17 duration=1.102ms`,
			`2026-07-10T10:03:18.777Z [33mWARN[0m auth gateway/auth.go:155 invalid bearer token method=POST path=/mcp status=403 account=unknown`,
			`2026-07-10T10:04:02.019Z [34mINFO[0m mcp gateway/mcp.go:141 tool invocation tool=deploy_component account=acme status=200 duration=842.551ms`,
		},
		"worker.log.1": {
			`2026-07-10T09:57:04.010Z [34mINFO[0m queue worker/main.go:44 worker started queue=deployments concurrency=4`,
			`2026-07-10T09:59:38.411Z [34mINFO[0m queue worker/job.go:96 job completed job=build-481 status=success duration=19.4s`,
		},
		"worker.log": {
			`2026-07-10T10:01:07.120Z [34mINFO[0m queue worker/job.go:61 job started job=deploy-208 account=acme`,
			`2026-07-10T10:02:02.810Z [33mWARN[0m queue worker/retry.go:39 retrying job job=deploy-208 attempt=2 reason="temporary network error"`,
			`2026-07-10T10:03:09.034Z [34mINFO[0m queue worker/job.go:96 job completed job=deploy-208 status=success duration=121.9s`,
		},
	}

	for name, lines := range files {
		content := ""
		for _, line := range lines {
			content += line + "\n"
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			return "", err
		}
	}
	return root, nil
}
