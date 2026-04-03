package logger

import (
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorYellow = "\033[33m"
	colorCyan   = "\033[36m"
	colorGray   = "\033[37m"
)

func coloredLevel(level zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
	var color string
	switch level {
	case zapcore.DebugLevel:
		color = colorGray
	case zapcore.InfoLevel:
		color = colorCyan
	case zapcore.WarnLevel:
		color = colorYellow
	default: // error, fatal, panic
		color = colorRed
	}
	enc.AppendString(color + level.CapitalString() + colorReset)
}

// New creates a zap.Logger with colored console output and the given level.
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

	encCfg := zapcore.EncoderConfig{
		TimeKey:        "T",
		LevelKey:       "L",
		MessageKey:     "M",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    coloredLevel,
		EncodeTime:     zapcore.TimeEncoderOfLayout("15:04:05.000"),
		EncodeDuration: zapcore.StringDurationEncoder,
	}

	core := zapcore.NewCore(
		zapcore.NewConsoleEncoder(encCfg),
		zapcore.AddSync(os.Stdout),
		lvl,
	)

	return zap.New(core)
}
