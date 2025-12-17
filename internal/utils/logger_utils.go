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

// flushWriter wraps an *os.File and ensures log entries are written immediately.
// This ensures log entries are immediately visible in debug/trace mode.
// NOTE: flushWriter is not thread-safe by itself and must be wrapped by syncWriter.
type flushWriter struct {
	file *os.File
}

func newFlushWriter(file *os.File) *flushWriter {
	return &flushWriter{
		file: file,
	}
}

func (fw *flushWriter) Write(p []byte) (n int, err error) {
	// Write directly so logs are visible immediately in debug/trace mode.
	// We intentionally avoid calling fw.file.Sync() on every write to prevent slow I/O.
	return fw.file.Write(p)
}

// SetupLogger configures the logging system based on the provided configuration.
func SetupLogger(configManager types.ConfigManager) {
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
				var fileWriter io.Writer
				// Only use flushWriter in debug mode for immediate log visibility
				// In other modes, use direct file write for better performance
				if level == logrus.DebugLevel || level == logrus.TraceLevel {
					fileWriter = newFlushWriter(logFile)
				} else {
					fileWriter = logFile
				}
				// Use syncWriter to ensure thread-safe writes to both outputs
				multiWriter := &syncWriter{
					writer: io.MultiWriter(os.Stdout, fileWriter),
				}
				logrus.SetOutput(multiWriter)
			}
		}
	}
}
