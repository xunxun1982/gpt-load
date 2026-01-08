// Package mcpskills provides MCP service management and tool discovery
package mcpskills

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

// ProcessSecurityConfig contains security settings for subprocess execution
type ProcessSecurityConfig struct {
	// MaxExecutionTime is the maximum time a process can run
	MaxExecutionTime time.Duration
	// MaxMemoryMB is the maximum memory in MB (0 = no limit, only works on Linux)
	MaxMemoryMB int64
	// MaxCPUPercent is the maximum CPU percentage (0 = no limit, only works on Linux)
	MaxCPUPercent int
	// AllowedCommands is a whitelist of allowed commands (empty = allow all)
	AllowedCommands []string
	// BlockedCommands is a blacklist of blocked commands
	BlockedCommands []string
	// AllowedPaths is a whitelist of paths the process can access
	AllowedPaths []string
	// BlockNetworkAccess blocks network access (only works with additional setup)
	BlockNetworkAccess bool
	// RunAsUser specifies a user to run the process as (Linux only)
	RunAsUser string
	// WorkingDir specifies the working directory
	WorkingDir string
	// IsolateProcessGroup creates a new process group to prevent signal propagation
	IsolateProcessGroup bool
}

// DefaultSecurityConfig returns a secure default configuration
func DefaultSecurityConfig() *ProcessSecurityConfig {
	return &ProcessSecurityConfig{
		MaxExecutionTime:    5 * time.Minute,
		MaxMemoryMB:         512,
		IsolateProcessGroup: true,
		BlockedCommands: []string{
			// System control commands
			"shutdown", "reboot", "poweroff", "halt", "init",
			// Process manipulation
			"kill", "killall", "pkill",
			// Dangerous file operations
			"rm", "rmdir", "dd", "mkfs", "fdisk", "parted",
			// Network tools that could be abused
			"nc", "netcat", "ncat",
			// Shell spawning
			"bash", "sh", "zsh", "fish", "csh", "tcsh", "ksh",
			// Privilege escalation
			"sudo", "su", "doas",
			// System modification
			"chmod", "chown", "chgrp",
			// Cron/scheduling
			"crontab", "at",
		},
	}
}

// MCPSecurityConfig returns security config optimized for MCP processes
func MCPSecurityConfig() *ProcessSecurityConfig {
	config := DefaultSecurityConfig()
	config.MaxExecutionTime = 30 * time.Second // MCP operations should be quick
	config.MaxMemoryMB = 256                   // MCP servers typically don't need much memory
	return config
}

// InstallSecurityConfig returns security config for runtime installation
func InstallSecurityConfig() *ProcessSecurityConfig {
	return &ProcessSecurityConfig{
		MaxExecutionTime:    10 * time.Minute, // Installation can take time
		MaxMemoryMB:         1024,             // Installation may need more memory
		IsolateProcessGroup: true,
		BlockedCommands: []string{
			// Only block the most dangerous commands during installation
			"shutdown", "reboot", "poweroff", "halt", "init",
			"kill", "killall", "pkill",
			"dd", "mkfs", "fdisk", "parted",
		},
	}
}

// DangerousPatterns contains regex patterns for dangerous command arguments
var DangerousPatterns = []*regexp.Regexp{
	// Command injection patterns
	regexp.MustCompile(`[;&|` + "`" + `$()]`),
	// Path traversal
	regexp.MustCompile(`\.\.\/`),
	regexp.MustCompile(`\.\.\\`),
	// Null byte injection
	regexp.MustCompile(`\x00`),
	// Environment variable expansion that could leak secrets
	regexp.MustCompile(`\$\{?[A-Z_]+\}?`),
}

// SensitiveFilePaths contains paths that should never be accessed
var SensitiveFilePaths = []string{
	"/etc/passwd",
	"/etc/shadow",
	"/etc/sudoers",
	"/root/.ssh",
	"/home/*/.ssh",
	"~/.ssh",
	"/proc/*/environ",
	"/proc/*/cmdline",
	"/proc/*/fd",
}

// ValidateCommand checks if a command is safe to execute
func ValidateCommand(command string, args []string, config *ProcessSecurityConfig) error {
	// Check blocked commands
	cmdBase := filepath.Base(command)
	for _, blocked := range config.BlockedCommands {
		if strings.EqualFold(cmdBase, blocked) {
			return fmt.Errorf("command '%s' is blocked for security reasons", command)
		}
	}

	// Check allowed commands if whitelist is set
	if len(config.AllowedCommands) > 0 {
		allowed := false
		for _, allowedCmd := range config.AllowedCommands {
			if strings.EqualFold(cmdBase, allowedCmd) || strings.EqualFold(command, allowedCmd) {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("command '%s' is not in the allowed list", command)
		}
	}

	// Check for dangerous patterns in arguments
	allArgs := append([]string{command}, args...)
	for _, arg := range allArgs {
		for _, pattern := range DangerousPatterns {
			if pattern.MatchString(arg) {
				return fmt.Errorf("argument contains potentially dangerous pattern: %s", arg)
			}
		}
	}

	// Check for sensitive file paths in arguments
	for _, arg := range args {
		for _, sensitivePath := range SensitiveFilePaths {
			// Simple glob matching
			if matchesGlob(arg, sensitivePath) {
				return fmt.Errorf("access to sensitive path is blocked: %s", arg)
			}
		}
	}

	return nil
}

// matchesGlob performs simple glob matching
func matchesGlob(path, pattern string) bool {
	// Expand ~ to home directory
	if strings.HasPrefix(pattern, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			pattern = strings.Replace(pattern, "~", home, 1)
		}
	}

	// Simple wildcard matching
	if strings.Contains(pattern, "*") {
		parts := strings.Split(pattern, "*")
		if len(parts) == 2 {
			return strings.HasPrefix(path, parts[0]) && strings.HasSuffix(path, parts[1])
		}
	}

	return strings.Contains(path, pattern) || strings.HasPrefix(path, pattern)
}

// SecureCommand creates a command with security settings applied
func SecureCommand(ctx context.Context, config *ProcessSecurityConfig, command string, args ...string) (*exec.Cmd, error) {
	// Validate command first
	if err := ValidateCommand(command, args, config); err != nil {
		return nil, err
	}

	// Create command with timeout context
	var cmd *exec.Cmd
	if config.MaxExecutionTime > 0 {
		timeoutCtx, cancel := context.WithTimeout(ctx, config.MaxExecutionTime)
		// Note: caller should call cancel() when done
		_ = cancel // Suppress unused warning, caller manages this
		cmd = exec.CommandContext(timeoutCtx, command, args...)
	} else {
		cmd = exec.CommandContext(ctx, command, args...)
	}

	// Set working directory
	if config.WorkingDir != "" {
		cmd.Dir = config.WorkingDir
	}

	// Apply process isolation settings
	applyProcessIsolation(cmd, config)

	// Set safe environment
	cmd.Env = FilterSensitiveEnvVars(os.Environ())

	return cmd, nil
}

// applyProcessIsolation applies OS-specific process isolation
func applyProcessIsolation(cmd *exec.Cmd, config *ProcessSecurityConfig) {
	// Process isolation is only available on Unix-like systems
	// On Windows, we rely on other security measures
	applyProcessIsolationUnix(cmd, config)
}

// KillProcessGroup kills a process and all its children
func KillProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	return killProcessGroupImpl(cmd)
}

// SafeCommandRunner provides a safe way to run commands with security controls
type SafeCommandRunner struct {
	config *ProcessSecurityConfig
	logger *logrus.Entry
}

// NewSafeCommandRunner creates a new safe command runner
func NewSafeCommandRunner(config *ProcessSecurityConfig) *SafeCommandRunner {
	if config == nil {
		config = DefaultSecurityConfig()
	}
	return &SafeCommandRunner{
		config: config,
		logger: logrus.WithField("component", "safe_command_runner"),
	}
}

// Run executes a command safely and returns the output
func (r *SafeCommandRunner) Run(ctx context.Context, command string, args ...string) ([]byte, error) {
	// Log the command being executed (without sensitive data)
	r.logger.WithFields(logrus.Fields{
		"command": command,
		"args":    sanitizeArgsForLogging(args),
	}).Debug("Executing command")

	// Create secure command
	cmd, err := SecureCommand(ctx, r.config, command, args...)
	if err != nil {
		r.logger.WithError(err).Warn("Command validation failed")
		return nil, err
	}

	// Set up timeout
	timeoutCtx, cancel := context.WithTimeout(ctx, r.config.MaxExecutionTime)
	defer cancel()

	// Update command context
	cmd = exec.CommandContext(timeoutCtx, command, args...)
	applyProcessIsolation(cmd, r.config)
	cmd.Env = FilterSensitiveEnvVars(os.Environ())

	// Run and capture output
	output, err := cmd.CombinedOutput()

	// Check for timeout
	if timeoutCtx.Err() == context.DeadlineExceeded {
		// Kill the process group if it's still running
		_ = KillProcessGroup(cmd)
		return output, fmt.Errorf("command timed out after %v", r.config.MaxExecutionTime)
	}

	if err != nil {
		r.logger.WithFields(logrus.Fields{
			"command": command,
			"error":   err.Error(),
			"output":  truncateOutput(string(output), 500),
		}).Warn("Command execution failed")
	}

	return output, err
}

// sanitizeArgsForLogging removes potentially sensitive data from args for logging
func sanitizeArgsForLogging(args []string) []string {
	sanitized := make([]string, len(args))
	for i, arg := range args {
		// Mask anything that looks like a key or token
		if strings.Contains(strings.ToLower(arg), "key") ||
			strings.Contains(strings.ToLower(arg), "token") ||
			strings.Contains(strings.ToLower(arg), "secret") ||
			strings.Contains(strings.ToLower(arg), "password") {
			sanitized[i] = "[REDACTED]"
		} else if len(arg) > 100 {
			sanitized[i] = arg[:50] + "...[truncated]"
		} else {
			sanitized[i] = arg
		}
	}
	return sanitized
}

// truncateOutput truncates output for logging
func truncateOutput(output string, maxLen int) string {
	if len(output) <= maxLen {
		return output
	}
	return output[:maxLen] + "...[truncated]"
}

// ValidateMCPServerCommand validates a command specifically for MCP server execution
func ValidateMCPServerCommand(command string, args []string) error {
	// Allowed MCP server commands
	allowedCommands := []string{
		"npx", "npm", "node",
		"uvx", "uv", "python", "python3",
		"bunx", "bun",
		"deno",
	}

	cmdBase := filepath.Base(command)
	allowed := false
	for _, allowedCmd := range allowedCommands {
		if strings.EqualFold(cmdBase, allowedCmd) {
			allowed = true
			break
		}
	}

	if !allowed {
		return fmt.Errorf("command '%s' is not an allowed MCP server command", command)
	}

	// Check for dangerous patterns in arguments
	for _, arg := range args {
		// Block shell metacharacters
		if strings.ContainsAny(arg, ";&|`$(){}[]<>") {
			return fmt.Errorf("argument contains shell metacharacters: %s", arg)
		}
		// Block path traversal
		if strings.Contains(arg, "..") {
			return fmt.Errorf("argument contains path traversal: %s", arg)
		}
	}

	return nil
}
