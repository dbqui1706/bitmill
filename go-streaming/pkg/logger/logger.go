package logger

import (
	"os"
	"path/filepath"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

// Logger wraps zap's SugaredLogger with automatic log rotation via Lumberjack.
type Logger struct {
	sugar  *zap.SugaredLogger
	zapLog *zap.Logger
}

// Config specifies logger initialization options.
type Config struct {
	Filename   string // Log file path (e.g. "logs/app.log")
	MaxSizeMB  int    // Max megabytes before rotation
	MaxBackups int    // Number of retained rotated files
	MaxAgeDays int    // Days to retain old log files
	Compress   bool   // Whether to gzip rotated logs
	Level      string // "debug", "info", "warn", "error"
}

// DefaultConfig provides production-ready defaults.
func DefaultConfig() Config {
	return Config{
		Filename:   "logs/app.log",
		MaxSizeMB:  100,
		MaxBackups: 5,
		MaxAgeDays: 30,
		Compress:   true,
		Level:      "info",
	}
}

// New initializes a Logger with default production settings and Lumberjack log rotation.
func New() *Logger {
	return NewWithConfig(DefaultConfig())
}

// NewWithConfig initializes a Logger with explicit configuration.
func NewWithConfig(cfg Config) *Logger {
	// Parse log level
	var level zapcore.Level
	if err := level.UnmarshalText([]byte(cfg.Level)); err != nil {
		level = zapcore.InfoLevel
	}

	// 1. Console Encoder (Colorized, Human-readable for development)
	consoleEncoderConfig := zap.NewDevelopmentEncoderConfig()
	consoleEncoderConfig.EncodeTime = zapcore.TimeEncoderOfLayout("2006-01-02 15:04:05")
	consoleEncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	consoleEncoder := zapcore.NewConsoleEncoder(consoleEncoderConfig)

	// 2. File Encoder (Structured JSON for indexing and production monitoring)
	fileEncoderConfig := zap.NewProductionEncoderConfig()
	fileEncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	fileEncoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder
	fileEncoder := zapcore.NewJSONEncoder(fileEncoderConfig)

	// 3. Lumberjack Log Rotator
	if cfg.Filename == "" {
		cfg.Filename = "logs/app.log"
	}
	_ = os.MkdirAll(filepath.Dir(cfg.Filename), 0755)

	lumberJackLogger := &lumberjack.Logger{
		Filename:   cfg.Filename,
		MaxSize:    cfg.MaxSizeMB,
		MaxBackups: cfg.MaxBackups,
		MaxAge:     cfg.MaxAgeDays,
		Compress:   cfg.Compress,
	}

	// 4. Combine into Tee Core (Console + Rotated File)
	core := zapcore.NewTee(
		zapcore.NewCore(consoleEncoder, zapcore.AddSync(os.Stdout), level),
		zapcore.NewCore(fileEncoder, zapcore.AddSync(lumberJackLogger), level),
	)

	zapLog := zap.New(core, zap.AddCaller(), zap.AddCallerSkip(1))
	return &Logger{
		sugar:  zapLog.Sugar(),
		zapLog: zapLog,
	}
}

// Info logs formatted informational messages.
func (l *Logger) Info(format string, args ...interface{}) {
	if len(args) == 0 {
		l.sugar.Info(format)
		return
	}
	l.sugar.Infof(format, args...)
}

// Warn logs formatted warning messages.
func (l *Logger) Warn(format string, args ...interface{}) {
	if len(args) == 0 {
		l.sugar.Warn(format)
		return
	}
	l.sugar.Warnf(format, args...)
}

// Error logs formatted error messages.
func (l *Logger) Error(format string, args ...interface{}) {
	if len(args) == 0 {
		l.sugar.Error(format)
		return
	}
	l.sugar.Errorf(format, args...)
}

// Debug logs formatted debug messages.
func (l *Logger) Debug(format string, args ...interface{}) {
	if len(args) == 0 {
		l.sugar.Debug(format)
		return
	}
	l.sugar.Debugf(format, args...)
}

// With adds structured context key-value pairs to the logger.
func (l *Logger) With(args ...interface{}) *Logger {
	return &Logger{
		sugar:  l.sugar.With(args...),
		zapLog: l.zapLog,
	}
}

// Sync flushes any buffered log entries.
func (l *Logger) Sync() error {
	_ = l.sugar.Sync()
	return l.zapLog.Sync()
}
