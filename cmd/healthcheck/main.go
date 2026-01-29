// Package main provides a lightweight health check utility for Docker containers.
// This tool is statically compiled and designed to work in minimal environments
// like scratch-based Docker images where standard tools (wget, curl) are unavailable.
package main

import (
	"fmt"
	"net/http"
	"os"
	"time"
)

const (
	defaultPort    = "3001"
	requestTimeout = 5 * time.Second
	exitSuccess    = 0
	exitFailure    = 1
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = defaultPort
	}

	healthURL := fmt.Sprintf("http://localhost:%s/health", port)

	client := &http.Client{
		Timeout: requestTimeout,
	}

	resp, err := client.Get(healthURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Health check failed: %v\n", err)
		os.Exit(exitFailure)
	}
	// Close response body immediately since we exit right after checking status
	// Note: defer won't work here because os.Exit bypasses deferred calls
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Health check returned non-OK status: %d\n", resp.StatusCode)
		os.Exit(exitFailure)
	}

	os.Exit(exitSuccess)
}
