package utils

import (
	"gpt-load/internal/types"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/sirupsen/logrus"
)

// syncWriter wraps an io.Writer with synchronization to ensure thread-safe writes.
// This prevents log entries from being interleaved when multiple goroutines write concurrently.
type syncWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

func (sw *syncWriter) Write(p []byte) (n int, err error) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	return sw.writer.Write(p)
}

var (
	loggerFileMu sync.Mutex
	loggerFile   *os.File
	exitHandler  sync.Once
)

func CloseLogger() {
	loggerFileMu.Lock()
	file := loggerFile
	loggerFile = nil
	loggerFileMu.Unlock()

	if file == nil {
		return
	}

	logrus.SetOutput(os.Stdout)
	_ = file.Sync()
	_ = file.Close()
}

// SetupLogger configures the logging system based on the provided configuration.
func SetupLogger(configManager types.ConfigManager) {
	CloseLogger()
	exitHandler.Do(func() {
		logrus.RegisterExitHandler(CloseLogger)
	})
	logConfig := configManager.GetLogConfig()

	// Set log level
	level, err := logrus.ParseLevel(logConfig.Level)
	if err != nil {
		logrus.Warn("Invalid log level, using info")
		level = logrus.InfoLevel
	}
	logrus.SetLevel(level)

	// Set log format
	if logConfig.Format == "json" {
		logrus.SetFormatter(&logrus.JSONFormatter{
			TimestampFormat: "2006-01-02T15:04:05.000Z07:00", // ISO 8601 format
		})
	} else {
		logrus.SetFormatter(&logrus.TextFormatter{
			FullTimestamp:   true,
			TimestampFormat: "2006-01-02 15:04:05",
		})
	}

	// Setup file logging if enabled
	if logConfig.EnableFile {
		logDir := filepath.Dir(logConfig.FilePath)
		if err := os.MkdirAll(logDir, 0755); err != nil {
			logrus.Warnf("Failed to create log directory: %v", err)
		} else {
			logFile, err := os.OpenFile(logConfig.FilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
			if err != nil {
				logrus.Warnf("Failed to open log file: %v", err)
			} else {
				loggerFileMu.Lock()
				oldFile := loggerFile
				loggerFile = logFile
				loggerFileMu.Unlock()
				if oldFile != nil && oldFile != logFile {
					_ = oldFile.Close()
				}
				// NOTE: We intentionally avoid calling Sync() on every write, even in debug/trace,
				// because fsync is extremely slow and can significantly degrade performance.
				// Use syncWriter to ensure thread-safe writes to both outputs
				multiWriter := &syncWriter{
					writer: io.MultiWriter(os.Stdout, logFile),
				}
				logrus.SetOutput(multiWriter)
			}
		}
	}
}
