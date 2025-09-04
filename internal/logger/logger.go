package logger

import (
	"log/slog"
	"os"
)

var Logger *slog.Logger

func Init() {
	level := slog.LevelInfo
	if os.Getenv("DEBUG") == "true" {
		level = slog.LevelDebug
	}

	opts := &slog.HandlerOptions{
		Level: level,
	}

	Logger = slog.New(slog.NewTextHandler(os.Stdout, opts))
	slog.SetDefault(Logger)
}

func Info(msg string, args ...any) {
	Logger.Info(msg, args...)
}

func Error(msg string, args ...any) {
	Logger.Error(msg, args...)
}

func Debug(msg string, args ...any) {
	Logger.Debug(msg, args...)
}

func Warn(msg string, args ...any) {
	Logger.Warn(msg, args...)
}
