package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Level represents a log severity level.
type Level int

const (
	LevelVerbose Level = iota
	LevelDebug
	LevelInfo
	LevelError
)

// ANSI color codes
const (
	colorReset = "\033[0m"
	colorGray  = "\033[90m"
	colorCyan  = "\033[36m"
	colorGreen = "\033[32m"
	colorRed   = "\033[31m"
)

// Logger writes colored output to the console and plain text to a log file,
// each with independently configurable minimum levels.
type Logger struct {
	consoleLevel Level
	fileLevel    Level
	file         *os.File
	mu           sync.Mutex
}

// New creates a Logger. Log files are written to ~/Library/Logs/encore/.
func New(consoleLevel, fileLevel string) (*Logger, error) {
	cl, err := ParseLevel(consoleLevel)
	if err != nil {
		return nil, fmt.Errorf("invalid console log level: %w", err)
	}
	fl, err := ParseLevel(fileLevel)
	if err != nil {
		return nil, fmt.Errorf("invalid file log level: %w", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to determine home directory: %w", err)
	}

	logDir := filepath.Join(home, "Library", "Logs", "encore")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create log directory %s: %w", logDir, err)
	}

	logPath := filepath.Join(logDir, "encore.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file %s: %w", logPath, err)
	}

	return &Logger{
		consoleLevel: cl,
		fileLevel:    fl,
		file:         f,
	}, nil
}

// Close releases the underlying log file handle.
func (l *Logger) Close() {
	if l.file != nil {
		l.file.Close()
	}
}

// Verbose logs a message at the VERBOSE level.
func (l *Logger) Verbose(format string, args ...interface{}) {
	l.log(LevelVerbose, format, args...)
}

// Debug logs a message at the DEBUG level.
func (l *Logger) Debug(format string, args ...interface{}) {
	l.log(LevelDebug, format, args...)
}

// Info logs a message at the INFO level.
func (l *Logger) Info(format string, args ...interface{}) {
	l.log(LevelInfo, format, args...)
}

// Error logs a message at the ERROR level.
func (l *Logger) Error(format string, args ...interface{}) {
	l.log(LevelError, format, args...)
}

func (l *Logger) log(level Level, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	ts := time.Now().Format("2006-01-02 15:04:05")
	label := levelLabel(level)

	l.mu.Lock()
	defer l.mu.Unlock()

	// Console output (colored)
	if level >= l.consoleLevel {
		color := levelColor(level)
		fmt.Fprintf(os.Stdout, "%s%s [%s]%s %s\n", color, ts, label, colorReset, msg)
	}

	// File output (plain)
	if l.file != nil && level >= l.fileLevel {
		fmt.Fprintf(l.file, "%s [%s] %s\n", ts, label, msg)
	}
}

// ParseLevel converts a level name to a Level value.
func ParseLevel(s string) (Level, error) {
	switch s {
	case "verbose":
		return LevelVerbose, nil
	case "debug":
		return LevelDebug, nil
	case "info":
		return LevelInfo, nil
	case "error":
		return LevelError, nil
	default:
		return 0, fmt.Errorf("unknown log level: %q (must be verbose, debug, info, or error)", s)
	}
}

func levelLabel(l Level) string {
	switch l {
	case LevelVerbose:
		return "VERBOSE"
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelError:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

func levelColor(l Level) string {
	switch l {
	case LevelVerbose:
		return colorGray
	case LevelDebug:
		return colorCyan
	case LevelInfo:
		return colorGreen
	case LevelError:
		return colorRed
	default:
		return colorReset
	}
}
