package logger

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// New creates a zap.Logger with the given level (debug/info/warn/error).
func New(level string) *zap.Logger {
	var lvl zapcore.Level
	switch level {
	case "debug":
		lvl = zapcore.DebugLevel
	case "warn":
		lvl = zapcore.WarnLevel
	case "error":
		lvl = zapcore.ErrorLevel
	default:
		lvl = zapcore.InfoLevel
	}

	cfg := zap.NewProductionConfig()
	cfg.Level.SetLevel(lvl)
	cfg.Development = false

	log, _ := cfg.Build()
	return log
}
