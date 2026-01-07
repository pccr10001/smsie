package logger

import (
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var Log *zap.SugaredLogger

func InitLogger(levelStr string) {
	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	encoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder

	// Default to INFO if invalid or empty
	if levelStr == "" {
		levelStr = "info"
	}
	level, err := zapcore.ParseLevel(levelStr)
	if err != nil {
		level = zap.InfoLevel
	}

	var core zapcore.Core

	// For simplicity: If level is debug, log to console. Otherwise log to file?
	// Actually, let's keep it simple: always log to console if debug level is chosen,
	// or maybe stick to the previous logic but controlled by level.
	// Previous logic: debug bool -> console vs file.
	// New logic: Let's log to console for development convenience, or file for production.
	// The user asked for "debug level", implying they want to see debug logs.

	// Let's use console encoder for standard output which is docker friendly.
	consoleEncoder := zapcore.NewConsoleEncoder(encoderConfig)
	core = zapcore.NewCore(consoleEncoder, zapcore.AddSync(os.Stdout), level)

	logger := zap.New(core, zap.AddCaller())
	Log = logger.Sugar()
	Log.Infof("Logger initialized at level: %s", level.String())
}
