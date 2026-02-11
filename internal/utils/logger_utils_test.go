package utils

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gpt-load/internal/types"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// saveLoggerState saves the current logger state for restoration in tests
type loggerState struct {
	output    io.Writer
	level     logrus.Level
	formatter logrus.Formatter
}

// saveLoggerState captures current logger configuration
func saveLoggerState() *loggerState {
	return &loggerState{
		output:    logrus.StandardLogger().Out,
		level:     logrus.GetLevel(),
		formatter: logrus.StandardLogger().Formatter,
	}
}

// restore restores the logger to the saved state
func (s *loggerState) restore() {
	CloseLogger()
	logrus.SetOutput(s.output)
	logrus.SetLevel(s.level)
	logrus.SetFormatter(s.formatter)
}

// mockConfigManager implements types.ConfigManager for testing
type mockConfigManager struct {
	logConfig types.LogConfig
}

func (m *mockConfigManager) IsMaster() bool {
	return true
}

func (m *mockConfigManager) GetLogConfig() types.LogConfig {
	return m.logConfig
}

func (m *mockConfigManager) GetAuthConfig() types.AuthConfig {
	return types.AuthConfig{}
}

func (m *mockConfigManager) GetCORSConfig() types.CORSConfig {
	return types.CORSConfig{}
}

func (m *mockConfigManager) GetPerformanceConfig() types.PerformanceConfig {
	return types.PerformanceConfig{}
}

func (m *mockConfigManager) GetDatabaseConfig() types.DatabaseConfig {
	return types.DatabaseConfig{}
}

func (m *mockConfigManager) GetEncryptionKey() string {
	return ""
}

func (m *mockConfigManager) GetEffectiveServerConfig() types.ServerConfig {
	return types.ServerConfig{}
}

func (m *mockConfigManager) GetRedisDSN() string {
	return ""
}

func (m *mockConfigManager) IsDebugMode() bool {
	return false
}

func (m *mockConfigManager) Validate() error {
	return nil
}

func (m *mockConfigManager) DisplayServerConfig() {
	// No-op for testing
}

func (m *mockConfigManager) ReloadConfig() error {
	return nil
}

func TestSyncWriter(t *testing.T) {
	var buf bytes.Buffer
	sw := &syncWriter{writer: &buf}

	// Test concurrent writes
	var wg sync.WaitGroup
	numGoroutines := 10
	numWrites := 100

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numWrites; j++ {
				_, err := sw.Write([]byte("test\n"))
				assert.NoError(t, err)
			}
		}(i)
	}

	wg.Wait()

	// Verify all writes completed
	lines := strings.Split(buf.String(), "\n")
	// -1 because last line is empty after final \n
	assert.Equal(t, numGoroutines*numWrites, len(lines)-1)
}

func TestCloseLogger(t *testing.T) {
	// Save original state
	originalOutput := logrus.StandardLogger().Out
	defer logrus.SetOutput(originalOutput)

	// Create a temporary log file
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "test.log")

	// Open a log file
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	require.NoError(t, err)

	loggerFileMu.Lock()
	loggerFile = logFile
	loggerFileMu.Unlock()

	// Close logger
	CloseLogger()

	// Verify file is closed
	loggerFileMu.Lock()
	assert.Nil(t, loggerFile)
	loggerFileMu.Unlock()

	// Verify output is reset to stdout
	assert.Equal(t, os.Stdout, logrus.StandardLogger().Out)

	// Calling CloseLogger again should be safe
	CloseLogger()
}

func TestSetupLogger_TextFormat(t *testing.T) {
	saved := saveLoggerState()
	defer saved.restore()

	cfg := &mockConfigManager{
		logConfig: types.LogConfig{
			Level:      "debug",
			Format:     "text",
			EnableFile: false,
		},
	}

	SetupLogger(cfg)

	// Verify level
	assert.Equal(t, logrus.DebugLevel, logrus.GetLevel())

	// Verify formatter
	_, ok := logrus.StandardLogger().Formatter.(*logrus.TextFormatter)
	assert.True(t, ok)
}

func TestSetupLogger_JSONFormat(t *testing.T) {
	saved := saveLoggerState()
	defer saved.restore()

	cfg := &mockConfigManager{
		logConfig: types.LogConfig{
			Level:      "info",
			Format:     "json",
			EnableFile: false,
		},
	}

	SetupLogger(cfg)

	// Verify level
	assert.Equal(t, logrus.InfoLevel, logrus.GetLevel())

	// Verify formatter is JSON
	_, ok := logrus.StandardLogger().Formatter.(*logrus.JSONFormatter)
	assert.True(t, ok, "Expected JSONFormatter")
}

func TestSetupLogger_InvalidLevel(t *testing.T) {
	saved := saveLoggerState()
	defer saved.restore()

	cfg := &mockConfigManager{
		logConfig: types.LogConfig{
			Level:      "invalid",
			Format:     "text",
			EnableFile: false,
		},
	}

	SetupLogger(cfg)

	// Should default to info level
	assert.Equal(t, logrus.InfoLevel, logrus.GetLevel())
}

func TestSetupLogger_FileLogging(t *testing.T) {
	saved := saveLoggerState()
	defer saved.restore()

	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "logs", "test.log")

	cfg := &mockConfigManager{
		logConfig: types.LogConfig{
			Level:      "info",
			Format:     "text",
			EnableFile: true,
			FilePath:   logPath,
		},
	}

	SetupLogger(cfg)

	// Write a log message
	testMsg := "test log message"
	logrus.Info(testMsg)

	// Close logger to flush
	// Note: CloseLogger is called explicitly here and again in saved.restore() defer.
	// This is safe because CloseLogger is idempotent.
	CloseLogger()

	// Verify log file was created
	assert.FileExists(t, logPath)

	// Verify log content
	content, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(content), testMsg)
}

func TestSetupLogger_FileLoggingError(t *testing.T) {
	saved := saveLoggerState()
	defer saved.restore()

	// Create a file (not a directory) and try to use it as a log directory
	// This will fail when trying to create the log file
	tmpFile, err := os.CreateTemp("", "test-*.txt")
	require.NoError(t, err)
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	// Try to create a log file inside a file (not a directory) - this should fail
	invalidPath := filepath.Join(tmpFile.Name(), "test.log")

	cfg := &mockConfigManager{
		logConfig: types.LogConfig{
			Level:      "info",
			Format:     "text",
			EnableFile: true,
			FilePath:   invalidPath,
		},
	}

	// Should not panic, just log a warning
	SetupLogger(cfg)

	// Verify the invalid file doesn't exist
	_, err = os.Stat(invalidPath)
	assert.True(t, os.IsNotExist(err), "Invalid log file should not be created")
}

func TestSetupLogger_MultipleSetups(t *testing.T) {
	saved := saveLoggerState()
	defer saved.restore()

	tmpDir := t.TempDir()
	logPath1 := filepath.Join(tmpDir, "log1.log")
	logPath2 := filepath.Join(tmpDir, "log2.log")

	// First setup
	cfg1 := &mockConfigManager{
		logConfig: types.LogConfig{
			Level:      "info",
			Format:     "text",
			EnableFile: true,
			FilePath:   logPath1,
		},
	}
	SetupLogger(cfg1)
	logrus.Info("message1")

	// Second setup (should close first file)
	cfg2 := &mockConfigManager{
		logConfig: types.LogConfig{
			Level:      "debug",
			Format:     "json",
			EnableFile: true,
			FilePath:   logPath2,
		},
	}
	SetupLogger(cfg2)
	logrus.Info("message2")

	CloseLogger()

	// Verify both files exist
	assert.FileExists(t, logPath1)
	assert.FileExists(t, logPath2)

	// Verify content
	content1, err := os.ReadFile(logPath1)
	require.NoError(t, err)
	assert.Contains(t, string(content1), "message1")

	content2, err := os.ReadFile(logPath2)
	require.NoError(t, err)
	assert.Contains(t, string(content2), "message2")
}

func TestSetupLogger_AllLevels(t *testing.T) {
	levels := []string{"trace", "debug", "info", "warn", "error", "fatal", "panic"}
	expectedLevels := []logrus.Level{
		logrus.TraceLevel,
		logrus.DebugLevel,
		logrus.InfoLevel,
		logrus.WarnLevel,
		logrus.ErrorLevel,
		logrus.FatalLevel,
		logrus.PanicLevel,
	}

	for i, level := range levels {
		t.Run(level, func(t *testing.T) {
			saved := saveLoggerState()
			defer saved.restore()

			cfg := &mockConfigManager{
				logConfig: types.LogConfig{
					Level:      level,
					Format:     "text",
					EnableFile: false,
				},
			}

			SetupLogger(cfg)
			assert.Equal(t, expectedLevels[i], logrus.GetLevel())
		})
	}
}
