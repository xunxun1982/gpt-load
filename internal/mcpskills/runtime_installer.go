// Package mcpskills provides MCP service management and tool discovery
package mcpskills

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// RuntimeType represents the type of runtime environment for MCP services
type RuntimeType string

const (
	RuntimeNodeJS RuntimeType = "nodejs" // Node.js runtime (npx, npm, node)
	RuntimePython RuntimeType = "python" // Python runtime (uvx, uv, python, pip)
	RuntimeBun    RuntimeType = "bun"    // Bun runtime (bunx, bun)
	RuntimeDeno   RuntimeType = "deno"   // Deno runtime
	RuntimeDocker RuntimeType = "docker" // Docker (must be installed on host)
	RuntimeCustom RuntimeType = "custom" // Custom command with install_command
)

// ValidRuntimeTypes contains all valid runtime types for validation
var ValidRuntimeTypes = map[RuntimeType]bool{
	RuntimeNodeJS: true,
	RuntimePython: true,
	RuntimeBun:    true,
	RuntimeDeno:   true,
	RuntimeDocker: true,
	RuntimeCustom: true,
}

// IsValidRuntimeType checks if a runtime type is valid
func IsValidRuntimeType(rt RuntimeType) bool {
	return ValidRuntimeTypes[rt]
}

// SensitiveEnvVars is a list of environment variable names that should NOT be passed to MCP processes.
// These contain authentication keys, encryption keys, and other sensitive data.
// SECURITY: This list must be kept up-to-date when new sensitive env vars are added.
var SensitiveEnvVars = []string{
	// Application authentication and encryption
	"AUTH_KEY",
	"ENCRYPTION_KEY",
	"JWT_SECRET",
	"SECRET_KEY",
	"APP_SECRET",
	"API_SECRET",
	// Database credentials
	"DB_PASSWORD",
	"DATABASE_PASSWORD",
	"MYSQL_PASSWORD",
	"POSTGRES_PASSWORD",
	"REDIS_PASSWORD",
	// Cloud provider credentials
	"AWS_SECRET_ACCESS_KEY",
	"AWS_SESSION_TOKEN",
	"AZURE_CLIENT_SECRET",
	"GOOGLE_APPLICATION_CREDENTIALS",
	// Common sensitive patterns
	"PRIVATE_KEY",
	"SSL_KEY",
	"TLS_KEY",
}

// SensitiveEnvPrefixes are prefixes that indicate sensitive environment variables.
// Any env var starting with these prefixes will be filtered out.
var SensitiveEnvPrefixes = []string{
	"GPT_LOAD_",      // Application-specific secrets
	"GPTLOAD_",       // Alternative prefix
	"INTERNAL_",      // Internal configuration
}

// FilterSensitiveEnvVars filters out sensitive environment variables from the given list.
// Returns a new slice with sensitive variables removed.
// SECURITY: This function is critical for preventing credential leakage to MCP processes.
func FilterSensitiveEnvVars(envVars []string) []string {
	filtered := make([]string, 0, len(envVars))

	for _, env := range envVars {
		// Parse KEY=VALUE format
		parts := strings.SplitN(env, "=", 2)
		if len(parts) < 1 {
			continue
		}
		key := strings.ToUpper(parts[0])

		// Check exact matches
		isSensitive := false
		for _, sensitiveKey := range SensitiveEnvVars {
			if key == sensitiveKey {
				isSensitive = true
				break
			}
		}

		// Check prefix matches
		if !isSensitive {
			for _, prefix := range SensitiveEnvPrefixes {
				if strings.HasPrefix(key, prefix) {
					isSensitive = true
					break
				}
			}
		}

		// Check if key contains sensitive patterns
		if !isSensitive {
			sensitivePatterns := []string{"_KEY", "_SECRET", "_TOKEN", "_PASSWORD", "_CREDENTIAL"}
			for _, pattern := range sensitivePatterns {
				if strings.Contains(key, pattern) {
					isSensitive = true
					break
				}
			}
		}

		if !isSensitive {
			filtered = append(filtered, env)
		}
	}

	return filtered
}

// GetSafeEnvForMCP returns environment variables safe to pass to MCP processes.
// It filters out sensitive variables and adds necessary PATH extensions.
func GetSafeEnvForMCP(additionalEnvs map[string]string) []string {
	// Start with filtered system environment
	baseEnv := FilterSensitiveEnvVars(os.Environ())

	// Add additional environment variables (these are user-specified, so we trust them)
	for key, value := range additionalEnvs {
		baseEnv = append(baseEnv, fmt.Sprintf("%s=%s", key, value))
	}

	return baseEnv
}

// RuntimeInfo contains information about a runtime installation status
type RuntimeInfo struct {
	Type          RuntimeType `json:"type"`
	Name          string      `json:"name"`
	Version       string      `json:"version,omitempty"`
	Installed     bool        `json:"installed"`
	InstallHint   string      `json:"install_hint,omitempty"`
	InstallPath   string      `json:"install_path,omitempty"`
	InstalledAt   *time.Time  `json:"installed_at,omitempty"`
	CanInstall    bool        `json:"can_install"`              // Whether this runtime can be installed in current environment
	IsHostOnly    bool        `json:"is_host_only,omitempty"`   // Whether this runtime must be installed on host (e.g., Docker)
	InContainer   bool        `json:"in_container,omitempty"`   // Whether currently running in a container
}

// RuntimeInstallState tracks installed runtimes for persistence across container restarts
type RuntimeInstallState struct {
	Runtimes    map[RuntimeType]RuntimeStateEntry `json:"runtimes"`
	Packages    map[string]PackageStateEntry      `json:"packages"`
	LastUpdated time.Time                         `json:"last_updated"`
}

// RuntimeStateEntry tracks a single runtime installation
type RuntimeStateEntry struct {
	Installed   bool      `json:"installed"`
	Version     string    `json:"version,omitempty"`
	InstallPath string    `json:"install_path,omitempty"`
	InstalledAt time.Time `json:"installed_at"`
}

// PackageStateEntry tracks a single package installation
type PackageStateEntry struct {
	Name           string      `json:"name"`
	RuntimeType    RuntimeType `json:"runtime_type"`
	Version        string      `json:"version,omitempty"`
	InstallCommand string      `json:"install_command,omitempty"`
	InstalledAt    time.Time   `json:"installed_at"`
}

// RuntimeInstaller handles on-demand installation of MCP service runtimes.
// It persists installation state to disk so runtimes survive container rebuilds
// when using Docker volume mapping.
type RuntimeInstaller struct {
	stateMu   sync.RWMutex          // Protects state read/write
	state     *RuntimeInstallState  // Installation state
	stateFile string                // Path to state file
	dataDir   string                // Persistent data directory (Docker volume)
	installMu sync.Mutex            // Serializes installation operations
}

// Global runtime installer instance (singleton pattern using sync.Once)
var (
	globalInstaller     *RuntimeInstaller
	globalInstallerOnce sync.Once
)

// GetRuntimeInstaller returns the global runtime installer singleton instance.
// Uses sync.Once to ensure thread-safe lazy initialization.
func GetRuntimeInstaller() *RuntimeInstaller {
	globalInstallerOnce.Do(func() {
		dataDir := getDataDir()
		globalInstaller = &RuntimeInstaller{
			dataDir:   dataDir,
			stateFile: filepath.Join(dataDir, "runtime_state.json"),
		}
		globalInstaller.loadState()
	})
	return globalInstaller
}

// getDataDir returns the data directory for runtime installations.
// Supports RUNTIME_DATA_DIR environment variable for customization.
func getDataDir() string {
	if dir := os.Getenv("RUNTIME_DATA_DIR"); dir != "" {
		return dir
	}
	// Default to /app/data/runtimes (should be mapped to Docker volume)
	return "/app/data/runtimes"
}

// loadState loads the installation state from disk
func (r *RuntimeInstaller) loadState() {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()

	r.state = &RuntimeInstallState{
		Runtimes: make(map[RuntimeType]RuntimeStateEntry),
		Packages: make(map[string]PackageStateEntry),
	}

	data, err := os.ReadFile(r.stateFile)
	if err != nil {
		if !os.IsNotExist(err) {
			logrus.WithError(err).Warn("Failed to load runtime state file")
		}
		return
	}

	if err := json.Unmarshal(data, r.state); err != nil {
		logrus.WithError(err).Warn("Failed to parse runtime state file")
		// Reset to empty state on parse error
		r.state = &RuntimeInstallState{
			Runtimes: make(map[RuntimeType]RuntimeStateEntry),
			Packages: make(map[string]PackageStateEntry),
		}
	}
}

// saveStateLocked saves the installation state to disk.
// Caller must hold stateMu lock.
func (r *RuntimeInstaller) saveStateLocked() error {
	if r.state == nil {
		return nil
	}

	r.state.LastUpdated = time.Now()

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(r.stateFile), 0755); err != nil {
		return fmt.Errorf("failed to create state directory: %w", err)
	}

	data, err := json.MarshalIndent(r.state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	if err := os.WriteFile(r.stateFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write state file: %w", err)
	}

	return nil
}

// DetectRuntimeType detects the runtime type from command name
func DetectRuntimeType(command string, _ []string) RuntimeType {
	switch strings.ToLower(command) {
	case "npx", "npm", "node":
		return RuntimeNodeJS
	case "uvx", "uv", "python", "python3", "pip", "pip3":
		return RuntimePython
	case "bunx", "bun":
		return RuntimeBun
	case "deno":
		return RuntimeDeno
	case "docker":
		return RuntimeDocker
	default:
		return RuntimeCustom
	}
}

// GenerateInstallCommand generates the installation command for a package.
// Returns empty string if no package installation is needed.
func GenerateInstallCommand(command string, args []string, customInstallCmd string) string {
	if customInstallCmd != "" {
		return customInstallCmd
	}

	cmd := strings.ToLower(command)
	runtimeType := DetectRuntimeType(command, args)

	switch runtimeType {
	case RuntimeNodeJS:
		if cmd == "npx" && len(args) > 0 {
			if pkg := findNpmPackageName(args); pkg != "" {
				return fmt.Sprintf("npm install -g %s", pkg)
			}
		}
	case RuntimePython:
		if cmd == "uvx" && len(args) > 0 {
			if pkg := findPythonPackageName(args); pkg != "" {
				return fmt.Sprintf("uv tool install %s", pkg)
			}
		}
	case RuntimeBun:
		if cmd == "bunx" && len(args) > 0 {
			if pkg := findNpmPackageName(args); pkg != "" {
				return fmt.Sprintf("bun install -g %s", pkg)
			}
		}
	}
	return ""
}

// findNpmPackageName extracts npm package name from command args
func findNpmPackageName(args []string) string {
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			return arg
		}
	}
	return ""
}

// findPythonPackageName extracts Python package name from command args
func findPythonPackageName(args []string) string {
	for i, arg := range args {
		if arg == "--from" && i+1 < len(args) {
			return args[i+1]
		}
		if !strings.HasPrefix(arg, "-") {
			return arg
		}
	}
	return ""
}

// getRuntimeInstallScript returns the shell script to install a runtime.
// Scripts install to persistent directory that survives container rebuilds.
func (r *RuntimeInstaller) getRuntimeInstallScript(runtimeType RuntimeType) string {
	isAlpine := isAlpineLinux()

	switch runtimeType {
	case RuntimeNodeJS:
		nodeDir := filepath.Join(r.dataDir, "nodejs")
		if isAlpine {
			return fmt.Sprintf(
				"mkdir -p %s && apk add --no-cache nodejs npm && npm config set prefix '%s/npm-global'",
				nodeDir, nodeDir)
		}
		return fmt.Sprintf(
			"mkdir -p %s && curl -fsSL https://deb.nodesource.com/setup_22.x | bash - && apt-get install -y nodejs && npm config set prefix '%s/npm-global'",
			nodeDir, nodeDir)

	case RuntimePython:
		pythonDir := filepath.Join(r.dataDir, "python")
		if isAlpine {
			return fmt.Sprintf(
				"mkdir -p %s && apk add --no-cache python3 py3-pip && pip3 install --no-cache-dir --break-system-packages uv",
				pythonDir)
		}
		return fmt.Sprintf(
			"mkdir -p %s && apt-get update && apt-get install -y python3 python3-pip && pip3 install uv",
			pythonDir)

	case RuntimeBun:
		bunDir := filepath.Join(r.dataDir, "bun")
		return fmt.Sprintf("mkdir -p %s && export BUN_INSTALL='%s' && curl -fsSL https://bun.sh/install | bash", bunDir, bunDir)

	case RuntimeDeno:
		denoDir := filepath.Join(r.dataDir, "deno")
		return fmt.Sprintf("mkdir -p %s && export DENO_INSTALL='%s' && curl -fsSL https://deno.land/install.sh | sh", denoDir, denoDir)

	default:
		return ""
	}
}

// isAlpineLinux checks if running on Alpine Linux
func isAlpineLinux() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "Alpine")
}

// isRunningInDocker checks if the current process is running inside a Docker container.
// Detection methods:
// 1. Check for /.dockerenv file (most reliable)
// 2. Check cgroup for docker/containerd signatures
func isRunningInDocker() bool {
	// Method 1: Check for /.dockerenv file
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}

	// Method 2: Check cgroup for container signatures
	data, err := os.ReadFile("/proc/1/cgroup")
	if err == nil {
		content := string(data)
		if strings.Contains(content, "docker") ||
			strings.Contains(content, "containerd") ||
			strings.Contains(content, "kubepods") {
			return true
		}
	}

	return false
}

// EnsureRuntimeInstalled ensures the required runtime is installed.
// Returns nil if already installed or installation succeeds.
// Thread-safe: uses mutex to prevent concurrent installations.
func (r *RuntimeInstaller) EnsureRuntimeInstalled(ctx context.Context, command string, args []string, customInstallCmd string) error {
	// Fast path: check if command already exists
	if CheckCommandExists(command) {
		return nil
	}

	// Serialize installation operations
	r.installMu.Lock()
	defer r.installMu.Unlock()

	// Double-check after acquiring lock (another goroutine may have installed it)
	if CheckCommandExists(command) {
		return nil
	}

	runtimeType := DetectRuntimeType(command, args)

	logrus.WithFields(logrus.Fields{
		"command":      command,
		"runtime_type": runtimeType,
	}).Info("Attempting to install missing runtime")

	// Install base runtime first (without proxy for auto-install)
	if err := r.installBaseRuntimeLocked(ctx, runtimeType, ""); err != nil {
		return fmt.Errorf("failed to install base runtime %s: %w", runtimeType, err)
	}

	// Install specific package if needed
	installCmd := GenerateInstallCommand(command, args, customInstallCmd)
	if installCmd != "" && !strings.HasPrefix(installCmd, "#") {
		if err := r.runInstallCommand(ctx, installCmd, ""); err != nil {
			return fmt.Errorf("failed to install package: %w", err)
		}
		// Record package installation
		r.stateMu.Lock()
		packageKey := fmt.Sprintf("%s:%s", runtimeType, command)
		r.state.Packages[packageKey] = PackageStateEntry{
			Name:           command,
			RuntimeType:    runtimeType,
			InstallCommand: installCmd,
			InstalledAt:    time.Now(),
		}
		_ = r.saveStateLocked()
		r.stateMu.Unlock()
	}

	// Verify installation succeeded
	if !CheckCommandExists(command) {
		return fmt.Errorf("command '%s' still not found after installation", command)
	}

	logrus.WithField("command", command).Info("Successfully installed runtime/package")
	return nil
}

// installBaseRuntimeLocked installs the base runtime (Node.js, Python, etc.).
// Caller must hold installMu lock.
// proxyURL is optional - if provided, it will be set as HTTP_PROXY/HTTPS_PROXY for downloading.
func (r *RuntimeInstaller) installBaseRuntimeLocked(ctx context.Context, runtimeType RuntimeType, proxyURL string) error {
	// Check if base runtime commands exist
	switch runtimeType {
	case RuntimeNodeJS:
		if CheckCommandExists("node") && CheckCommandExists("npm") {
			return nil
		}
	case RuntimePython:
		if CheckCommandExists("python3") && CheckCommandExists("uv") {
			return nil
		}
	case RuntimeBun:
		if CheckCommandExists("bun") {
			return nil
		}
	case RuntimeDeno:
		if CheckCommandExists("deno") {
			return nil
		}
	case RuntimeDocker:
		if CheckCommandExists("docker") {
			return nil
		}
		return fmt.Errorf("docker must be installed on the host system")
	case RuntimeCustom:
		return nil
	}

	// Check persistent state (runtime may have been installed in previous container)
	r.stateMu.RLock()
	entry, hasEntry := r.state.Runtimes[runtimeType]
	r.stateMu.RUnlock()

	if hasEntry && entry.Installed {
		// Runtime was installed before, update PATH and check again
		r.updatePathForRuntime(runtimeType)
		if r.isRuntimeAvailable(runtimeType) {
			return nil
		}
	}

	// Install the runtime
	script := r.getRuntimeInstallScript(runtimeType)
	if script == "" {
		return fmt.Errorf("no installation script available for runtime %s", runtimeType)
	}

	if err := r.runInstallCommand(ctx, script, proxyURL); err != nil {
		return err
	}

	// Record installation in state
	r.stateMu.Lock()
	r.state.Runtimes[runtimeType] = RuntimeStateEntry{
		Installed:   true,
		InstallPath: filepath.Join(r.dataDir, string(runtimeType)),
		InstalledAt: time.Now(),
		Version:     r.getVersionLocked(runtimeType),
	}
	_ = r.saveStateLocked()
	r.stateMu.Unlock()

	// Update PATH for newly installed runtime
	r.updatePathForRuntime(runtimeType)

	return nil
}

// isRuntimeAvailable checks if a runtime's commands are available
func (r *RuntimeInstaller) isRuntimeAvailable(runtimeType RuntimeType) bool {
	switch runtimeType {
	case RuntimeNodeJS:
		return CheckCommandExists("node") && CheckCommandExists("npm")
	case RuntimePython:
		return CheckCommandExists("python3") && CheckCommandExists("uv")
	case RuntimeBun:
		return CheckCommandExists("bun")
	case RuntimeDeno:
		return CheckCommandExists("deno")
	default:
		return false
	}
}

// updatePathForRuntime updates PATH environment variable for a runtime
func (r *RuntimeInstaller) updatePathForRuntime(runtimeType RuntimeType) {
	var newPaths []string

	switch runtimeType {
	case RuntimeNodeJS:
		newPaths = []string{
			filepath.Join(r.dataDir, "nodejs", "npm-global", "bin"),
			"/usr/local/bin",
		}
	case RuntimePython:
		newPaths = []string{
			filepath.Join(r.dataDir, "python", "bin"),
			"/root/.local/bin",
		}
	case RuntimeBun:
		newPaths = []string{filepath.Join(r.dataDir, "bun", "bin")}
	case RuntimeDeno:
		newPaths = []string{filepath.Join(r.dataDir, "deno", "bin")}
	}

	if len(newPaths) == 0 {
		return
	}

	currentPath := os.Getenv("PATH")
	modified := false
	for _, p := range newPaths {
		if !strings.Contains(currentPath, p) {
			currentPath = p + ":" + currentPath
			modified = true
		}
	}
	if modified {
		_ = os.Setenv("PATH", currentPath)
	}
}

// runInstallCommand executes an installation command with timeout.
// SECURITY: Uses filtered environment variables and process isolation to prevent:
// - Credential leakage via environment variables
// - Signal injection to parent process
// - Resource exhaustion
// Supports cross-platform execution: Unix (sh -c) and Windows (PowerShell)
// proxyURL is optional - if provided, it will be set as HTTP_PROXY/HTTPS_PROXY for downloading.
func (r *RuntimeInstaller) runInstallCommand(ctx context.Context, installCmd string, proxyURL string) error {
	logrus.WithFields(logrus.Fields{
		"command":   truncateOutput(installCmd, 200),
		"platform":  runtime.GOOS,
		"use_proxy": proxyURL != "",
	}).Info("Running installation command")

	// Use security config for installation
	config := InstallSecurityConfig()

	timeoutCtx, cancel := context.WithTimeout(ctx, config.MaxExecutionTime)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		// Windows: use PowerShell for command execution
		cmd = exec.CommandContext(timeoutCtx, "powershell", "-NoProfile", "-NonInteractive", "-Command", installCmd)
	} else {
		// Unix-like systems: use sh -c
		cmd = exec.CommandContext(timeoutCtx, "sh", "-c", installCmd)
	}

	// SECURITY: Apply process isolation to prevent signal injection to parent
	applyProcessIsolation(cmd, config)

	// SECURITY: Filter sensitive env vars before passing to install command
	safeEnv := FilterSensitiveEnvVars(os.Environ())
	safeEnv = append(safeEnv, "PATH="+r.getExtendedPath())

	// Add proxy environment variables if proxy URL is provided
	if proxyURL != "" {
		proxyURL = strings.TrimSpace(proxyURL)
		safeEnv = append(safeEnv,
			"HTTP_PROXY="+proxyURL,
			"HTTPS_PROXY="+proxyURL,
			"http_proxy="+proxyURL,
			"https_proxy="+proxyURL,
		)
		logrus.WithField("proxy", truncateOutput(proxyURL, 50)).Debug("Using proxy for installation")
	}

	cmd.Env = safeEnv

	output, err := cmd.CombinedOutput()

	// Log output for debugging
	if len(output) > 0 {
		logrus.WithFields(logrus.Fields{
			"command": truncateOutput(installCmd, 100),
			"output":  truncateOutput(string(output), 500),
		}).Debug("Installation command output")
	}

	// Check for timeout and kill process group if needed
	if timeoutCtx.Err() == context.DeadlineExceeded {
		_ = KillProcessGroup(cmd)
		return fmt.Errorf("installation timed out after %v", config.MaxExecutionTime)
	}

	if err != nil {
		logrus.WithFields(logrus.Fields{
			"command": truncateOutput(installCmd, 200),
			"output":  truncateOutput(string(output), 1000),
			"error":   err,
		}).Error("Installation command failed")
		return fmt.Errorf("installation failed: %s - %w", truncateOutput(string(output), 500), err)
	}

	logrus.WithFields(logrus.Fields{
		"command": truncateOutput(installCmd, 100),
	}).Info("Installation command completed successfully")

	return nil
}

// getExtendedPath returns PATH with all runtime directories included
func (r *RuntimeInstaller) getExtendedPath() string {
	paths := []string{
		filepath.Join(r.dataDir, "nodejs", "npm-global", "bin"),
		filepath.Join(r.dataDir, "python", "bin"),
		filepath.Join(r.dataDir, "bun", "bin"),
		filepath.Join(r.dataDir, "deno", "bin"),
		"/root/.npm-global/bin",
		"/root/.local/bin",
		"/usr/local/bin",
		"/usr/bin",
		"/bin",
	}
	return strings.Join(paths, ":") + ":" + os.Getenv("PATH")
}

// GetRuntimeStatus returns the installation status of all supported runtimes
func (r *RuntimeInstaller) GetRuntimeStatus() []RuntimeInfo {
	inContainer := isRunningInDocker()

	runtimes := []RuntimeInfo{
		{
			Type:        RuntimeNodeJS,
			Name:        "Node.js",
			Installed:   CheckCommandExists("node"),
			InstallHint: "Required for npx-based MCP servers",
			InstallPath: filepath.Join(r.dataDir, "nodejs"),
			CanInstall:  true,
			InContainer: inContainer,
		},
		{
			Type:        RuntimePython,
			Name:        "Python + uv",
			Installed:   CheckCommandExists("python3") && CheckCommandExists("uv"),
			InstallHint: "Required for uvx-based MCP servers",
			InstallPath: filepath.Join(r.dataDir, "python"),
			CanInstall:  true,
			InContainer: inContainer,
		},
		{
			Type:        RuntimeBun,
			Name:        "Bun",
			Installed:   CheckCommandExists("bun"),
			InstallHint: "Alternative to Node.js, faster startup",
			InstallPath: filepath.Join(r.dataDir, "bun"),
			CanInstall:  true,
			InContainer: inContainer,
		},
		{
			Type:        RuntimeDeno,
			Name:        "Deno",
			Installed:   CheckCommandExists("deno"),
			InstallHint: "Secure runtime for TypeScript/JavaScript",
			InstallPath: filepath.Join(r.dataDir, "deno"),
			CanInstall:  true,
			InContainer: inContainer,
		},
		{
			Type:        RuntimeDocker,
			Name:        "Docker",
			Installed:   CheckCommandExists("docker"),
			InstallHint: "Required for Docker-based MCP servers. Must be installed on host system.",
			InstallPath: "",
			CanInstall:  false, // Docker cannot be installed inside container
			IsHostOnly:  true,
			InContainer: inContainer,
		},
	}

	r.stateMu.RLock()
	defer r.stateMu.RUnlock()

	for i := range runtimes {
		if runtimes[i].Installed {
			runtimes[i].Version = r.getVersionLocked(runtimes[i].Type)
		}
		if entry, ok := r.state.Runtimes[runtimes[i].Type]; ok {
			installedAt := entry.InstalledAt
			runtimes[i].InstalledAt = &installedAt
		}
	}

	return runtimes
}

// getVersionLocked gets the version of an installed runtime.
// Does not require lock as it only executes external commands.
// SECURITY: Only executes whitelisted commands with --version flag
func (r *RuntimeInstaller) getVersionLocked(runtimeType RuntimeType) string {
	var command string

	switch runtimeType {
	case RuntimeNodeJS:
		command = "node"
	case RuntimePython:
		command = "python3"
	case RuntimeBun:
		command = "bun"
	case RuntimeDeno:
		command = "deno"
	default:
		return ""
	}

	// Create command with timeout to prevent hanging
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, command, "--version")
	// SECURITY: Use filtered environment and process isolation
	cmd.Env = FilterSensitiveEnvVars(os.Environ())
	applyProcessIsolation(cmd, &ProcessSecurityConfig{IsolateProcessGroup: true})

	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// UninstallRuntime removes a runtime installation
func (r *RuntimeInstaller) UninstallRuntime(_ context.Context, runtimeType RuntimeType) error {
	r.installMu.Lock()
	defer r.installMu.Unlock()

	installPath := filepath.Join(r.dataDir, string(runtimeType))

	// Remove installation directory
	if err := os.RemoveAll(installPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove runtime directory: %w", err)
	}

	// Update state
	r.stateMu.Lock()
	delete(r.state.Runtimes, runtimeType)
	// Remove related packages
	for key, pkg := range r.state.Packages {
		if pkg.RuntimeType == runtimeType {
			delete(r.state.Packages, key)
		}
	}
	if err := r.saveStateLocked(); err != nil {
		logrus.WithError(err).Warn("Failed to save state after uninstall")
	}
	r.stateMu.Unlock()

	logrus.WithField("runtime", runtimeType).Info("Runtime uninstalled")
	return nil
}

// UpgradeRuntime upgrades a runtime to the latest version.
// proxyURL is optional - if provided, it will be used for downloading.
func (r *RuntimeInstaller) UpgradeRuntime(ctx context.Context, runtimeType RuntimeType, proxyURL string) error {
	// Uninstall first (this acquires installMu)
	if err := r.UninstallRuntime(ctx, runtimeType); err != nil {
		logrus.WithError(err).Warn("Failed to uninstall before upgrade, continuing anyway")
	}

	// Reinstall (this also acquires installMu, but UninstallRuntime has released it)
	r.installMu.Lock()
	defer r.installMu.Unlock()
	return r.installBaseRuntimeLocked(ctx, runtimeType, proxyURL)
}

// GetInstalledPackages returns list of installed packages
func (r *RuntimeInstaller) GetInstalledPackages() []PackageStateEntry {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()

	packages := make([]PackageStateEntry, 0, len(r.state.Packages))
	for _, pkg := range r.state.Packages {
		packages = append(packages, pkg)
	}
	return packages
}

// UninstallPackage removes a specific package
func (r *RuntimeInstaller) UninstallPackage(ctx context.Context, packageName string, runtimeType RuntimeType) error {
	var uninstallCmd string

	switch runtimeType {
	case RuntimeNodeJS:
		uninstallCmd = fmt.Sprintf("npm uninstall -g %s", packageName)
	case RuntimePython:
		uninstallCmd = fmt.Sprintf("uv tool uninstall %s", packageName)
	case RuntimeBun:
		uninstallCmd = fmt.Sprintf("bun remove -g %s", packageName)
	default:
		return fmt.Errorf("unsupported runtime type for package uninstall: %s", runtimeType)
	}

	if err := r.runInstallCommand(ctx, uninstallCmd, ""); err != nil {
		return err
	}

	// Update state
	r.stateMu.Lock()
	packageKey := fmt.Sprintf("%s:%s", runtimeType, packageName)
	delete(r.state.Packages, packageKey)
	_ = r.saveStateLocked()
	r.stateMu.Unlock()

	return nil
}

// GetDataDir returns the data directory path
func (r *RuntimeInstaller) GetDataDir() string {
	return r.dataDir
}

// InstallRuntime installs a specific runtime on-demand (public API).
// proxyURL is optional - if provided, it will be used for downloading.
func (r *RuntimeInstaller) InstallRuntime(ctx context.Context, runtimeType RuntimeType, proxyURL string) error {
	r.installMu.Lock()
	defer r.installMu.Unlock()
	return r.installBaseRuntimeLocked(ctx, runtimeType, proxyURL)
}

// InstallCustomPackage installs a custom package using the provided install command.
// This is useful for installing global CLI tools like ace-tool, mcp servers, etc.
// Example: packageName="ace-tool", installCommand="npm install -g ace-tool@latest", runtimeType=RuntimeNodeJS
func (r *RuntimeInstaller) InstallCustomPackage(ctx context.Context, packageName, installCommand string, runtimeType RuntimeType, proxyURL string) error {
	logrus.WithFields(logrus.Fields{
		"package":         packageName,
		"install_command": truncateOutput(installCommand, 200),
		"runtime_type":    runtimeType,
		"use_proxy":       proxyURL != "",
	}).Info("Installing custom package")

	r.installMu.Lock()
	defer r.installMu.Unlock()

	// First ensure the base runtime is installed (e.g., Node.js for npm commands)
	if runtimeType != RuntimeCustom {
		if err := r.installBaseRuntimeLocked(ctx, runtimeType, proxyURL); err != nil {
			return fmt.Errorf("failed to install base runtime %s: %w", runtimeType, err)
		}
	}

	// Run the custom install command
	if err := r.runInstallCommand(ctx, installCommand, proxyURL); err != nil {
		return fmt.Errorf("failed to install package %s: %w", packageName, err)
	}

	// Record package installation in state
	r.stateMu.Lock()
	packageKey := fmt.Sprintf("%s:%s", runtimeType, packageName)
	r.state.Packages[packageKey] = PackageStateEntry{
		Name:           packageName,
		RuntimeType:    runtimeType,
		InstallCommand: installCommand,
		InstalledAt:    time.Now(),
	}
	_ = r.saveStateLocked()
	r.stateMu.Unlock()

	logrus.WithFields(logrus.Fields{
		"package":      packageName,
		"runtime_type": runtimeType,
	}).Info("Custom package installed successfully")

	return nil
}
