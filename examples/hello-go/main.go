package main

import (
	"fmt"
	"net/http"
	"os"
)

func main() {
	port := os.Getenv("HELLO_PORT")
	if port == "" {
		fmt.Fprintln(os.Stderr, "error: HELLO_PORT environment variable is required")
		os.Exit(1)
	}

	greeting := os.Getenv("HELLO_GREETING")
	if greeting == "" {
		greeting = "Hello"
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s (port %s)\n", greeting, port)
	})

	fmt.Printf("hello-go listening on :%s\n", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
