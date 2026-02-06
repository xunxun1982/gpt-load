// Package main provides the entry point for the GPT-Load proxy server
package main

import (
	"context"
	"embed"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gpt-load/internal/app"
	"gpt-load/internal/commands"
	"gpt-load/internal/container"
	"gpt-load/internal/types"
	"gpt-load/internal/utils"

	"github.com/sirupsen/logrus"
)

//go:embed web/dist
var buildFS embed.FS

//go:embed web/dist/index.html
var indexPage []byte

func main() {
	if len(os.Args) > 1 {
		runCommand()
	} else {
		runServer()
	}
}

// runCommand dispatches to the appropriate command handler
func runCommand() {
	command := os.Args[1]
	args := os.Args[2:]

	switch command {
	case "migrate-keys":
		commands.RunMigrateKeys(args)
	case "help", "-h", "--help":
		printHelp()
	default:
		fmt.Printf("Unknown command: %s\n", command)
		fmt.Println("Run 'gpt-load help' for usage.")
		os.Exit(1)
	}
}

// printHelp displays the general help information
func printHelp() {
	fmt.Println("GPT-Load - Multi-channel AI proxy with intelligent key rotation.")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  gpt-load                    Start the proxy server")
	fmt.Println("  gpt-load <command> [args]   Execute a command")
	fmt.Println()
	fmt.Println("Available Commands:")
	fmt.Println("  migrate-keys    Migrate encryption keys")
	fmt.Println("  help            Display this help message")
	fmt.Println()
	fmt.Println("Use 'gpt-load <command> --help' for more information about a command.")
}

// runServer run App Server
func runServer() {
	// Build the dependency injection container
	container, err := container.BuildContainer()
	if err != nil {
		logrus.Fatalf("Failed to build container: %v", err)
	}

	// Provide UI assets to the container
	if err := container.Provide(func() embed.FS { return buildFS }); err != nil {
		logrus.Fatalf("Failed to provide buildFS: %v", err)
	}
	if err := container.Provide(func() []byte { return indexPage }); err != nil {
		logrus.Fatalf("Failed to provide indexPage: %v", err)
	}

	// Initialize global logger
	if err := container.Invoke(func(configManager types.ConfigManager) {
		utils.SetupLogger(configManager)
	}); err != nil {
		logrus.Fatalf("Failed to setup logger: %v", err)
	}
	defer utils.CloseLogger()

	// Create and run the application
	if err := container.Invoke(func(application *app.App, configManager types.ConfigManager) {
		if err := application.Start(); err != nil {
			logrus.Fatalf("Failed to start application: %v", err)
		}

		// Setup signal handling for graceful shutdown
		// Use buffered channel to avoid missing signals
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

		// Wait for first interrupt signal
		sig := <-quit
		logrus.Infof("Received signal: %v, initiating graceful shutdown...", sig)

		// Create a context with timeout for shutdown
		serverConfig := configManager.GetEffectiveServerConfig()
		shutdownTimeout := time.Duration(serverConfig.GracefulShutdownTimeout) * time.Second
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		// Start graceful shutdown in a goroutine
		done := make(chan struct{})
		go func() {
			application.Stop(shutdownCtx)
			close(done)
		}()

		// Wait for shutdown to complete or second signal for force exit
		select {
		case <-done:
			logrus.Info("Graceful shutdown completed successfully")
		case <-quit:
			logrus.Warn("Second interrupt signal received, forcing immediate exit")
			os.Exit(1)
		case <-shutdownCtx.Done():
			logrus.Warn("Shutdown timeout exceeded, forcing exit")
			os.Exit(1)
		}

	}); err != nil {
		logrus.Fatalf("Failed to run application: %v", err)
	}
}
