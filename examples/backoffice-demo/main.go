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

	"github.com/go-chi/chi/v5"
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

	// Set up backoffice with a custom debug endpoint.
	// If not running under go-run, this is a no-op.
	userRouter := chi.NewRouter()
	userRouter.Get("/debug", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"uptime":  time.Since(startTime).String(),
			"pid":     os.Getpid(),
			"version": "0.1.0",
		})
	})
	backoffice.ListenAndServeBackground(ctx, userRouter)

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
