//go:build windows

// Package mcpskills provides MCP service management and tool discovery
package mcpskills

import (
	"fmt"
	"os/exec"
)

// applyProcessIsolationUnix is a no-op on Windows
// Windows uses different mechanisms for process isolation
func applyProcessIsolationUnix(_ *exec.Cmd, _ *ProcessSecurityConfig) {
	// Process group isolation is not available on Windows in the same way
	// Windows processes are isolated by default in different ways
}

// killProcessGroupImpl kills a process and all its children on Windows
func killProcessGroupImpl(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	pid := cmd.Process.Pid

	// On Windows, use taskkill to kill process tree
	killCmd := exec.Command("taskkill", "/F", "/T", "/PID", fmt.Sprintf("%d", pid))
	return killCmd.Run()
}
