//go:build !windows

// Package mcpskills provides MCP service management and tool discovery
package mcpskills

import (
	"os/exec"
	"syscall"
)

// applyProcessIsolationUnix applies Unix-specific process isolation
func applyProcessIsolationUnix(cmd *exec.Cmd, config *ProcessSecurityConfig) {
	if config == nil || !config.IsolateProcessGroup {
		return
	}

	// Create new process group to isolate from parent
	// This prevents the child from sending signals to the parent process
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true, // Create new process group
		Pgid:    0,    // Use the new process's PID as PGID
	}
}

// killProcessGroupImpl kills a process and all its children on Unix
func killProcessGroupImpl(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	pid := cmd.Process.Pid

	// On Unix, kill the process group
	// Negative PID means kill the entire process group
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		// Fallback to killing just the process
		return cmd.Process.Kill()
	}

	// Kill the entire process group
	return syscall.Kill(-pgid, syscall.SIGKILL)
}
