package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gur-shatz/go-run/pkg/backoffice"
)

func main() {
	port := os.Getenv("DEMO_PORT")
	if port == "" {
		fmt.Fprintln(os.Stderr, "error: DEMO_PORT environment variable is required")
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// Register services with backoffice
	dbSvc := backoffice.CreateServiceStatus("database", true)
	cacheSvc := backoffice.CreateServiceStatus("cache", false)

	// Set up backoffice with custom endpoints.
	// If not running under go-run, ListenAndServeBackground is a no-op.
	bo := backoffice.New()
	app := bo.Folder()
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
	bo.ListenAndServeTCPBackground(ctx, ":9090")
	fmt.Println("backoffice-demo: backoffice TCP on :9090 (user: admin)")

	// Simulate startup and intermittent cache failure
	go func() {
		time.Sleep(500 * time.Millisecond)
		dbSvc.SetStatus(backoffice.OK, map[string]string{"version": "0.1.0", "port": port})
		cacheSvc.SetStatus(backoffice.OK, nil)
		fmt.Println("backoffice-demo: services marked OK")

		time.Sleep(5 * time.Second)
		cacheSvc.SetStatus(backoffice.Down, map[string]string{"error": "connection refused"})
		fmt.Println("backoffice-demo: cache DOWN")

		time.Sleep(3 * time.Second)
		cacheSvc.SetStatus(backoffice.OK, nil)
		fmt.Println("backoffice-demo: cache recovered")
	}()

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
