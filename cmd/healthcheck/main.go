// Package main provides a minimal health check utility for Docker containers.
// This tool is statically compiled and designed to work in minimal environments
// like scratch-based Docker images where standard tools (wget, curl) are unavailable.
//
// Implementation Note:
// Uses TCP connection check instead of HTTP GET to minimize binary size.
// This reduces the binary from ~5MB to ~2MB by avoiding the entire
// net/http package (which includes TLS, HTTP/2, etc.).
//
// For Docker healthcheck purposes, TCP connectivity is sufficient:
// - TCP connection success = service is listening = container is healthy
// - TCP connection failure = service is down = container needs restart
//
// For more detailed health checks (database connectivity, dependency status, etc.),
// use application-level monitoring or Kubernetes liveness/readiness probes.
package main

import (
	"fmt"
	"net"
	"os"
	"time"
)

const (
	defaultPort = "3001"
	dialTimeout = 5 * time.Second
	exitSuccess = 0
	exitFailure = 1
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = defaultPort
	}

	address := buildAddress(port)

	// Use TCP dial instead of HTTP GET to minimize binary size
	// This checks if the service is listening on the port
	conn, err := net.DialTimeout("tcp", address, dialTimeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Health check failed: %v\n", err)
		os.Exit(exitFailure)
	}

	// Close connection immediately
	// Note: defer won't work here because os.Exit bypasses deferred calls
	conn.Close()

	os.Exit(exitSuccess)
}

// buildAddress constructs the TCP address for health check.
// Uses 127.0.0.1 instead of localhost for scratch-based Docker images.
// In minimal environments without /etc/hosts, localhost may not resolve to 127.0.0.1.
func buildAddress(port string) string {
	return fmt.Sprintf("127.0.0.1:%s", port)
}
