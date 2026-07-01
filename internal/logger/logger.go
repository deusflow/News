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

	// Use JSONHandler for structured logging
	Logger = slog.New(slog.NewJSONHandler(os.Stdout, opts))
	slog.SetDefault(Logger)
}

func Info(msg string, args ...any) {
	if Logger == nil {
		slog.Info(msg, args...)
		return
	}
	Logger.Info(msg, args...)
}

func Error(msg string, args ...any) {
	if Logger == nil {
		slog.Error(msg, args...)
		return
	}
	Logger.Error(msg, args...)
}

func Debug(msg string, args ...any) {
	if Logger == nil {
		slog.Debug(msg, args...)
		return
	}
	Logger.Debug(msg, args...)
}

func Warn(msg string, args ...any) {
	if Logger == nil {
		slog.Warn(msg, args...)
		return
	}
	Logger.Warn(msg, args...)
}

