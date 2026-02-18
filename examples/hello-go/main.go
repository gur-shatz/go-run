package main

import (
	"fmt"
	"net/http"
	"os"
)

func main() {
	port := os.Getenv("HELLO_GO_PORT")
	if port == "" {
		port = "15080"
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "Hello from gorun! (port %s)\n", port)
	})

	fmt.Printf("hello-go listening on :%s\n", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
