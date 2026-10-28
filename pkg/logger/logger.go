// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package logger

import (
	"fmt"
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Logger wraps zap.Logger for consistent logging.
type Logger struct {
	*zap.SugaredLogger
}

// New creates a new logger.
func New(level, format, outputPath string) (*Logger, error) {
	// Parse log level
	var zapLevel zapcore.Level

	switch level {
	case "debug":
		zapLevel = zapcore.DebugLevel
	case "info":
		zapLevel = zapcore.InfoLevel
	case "warn":
		zapLevel = zapcore.WarnLevel
	case "error":
		zapLevel = zapcore.ErrorLevel
	default:
		zapLevel = zapcore.InfoLevel
	}

	// Create encoder config
	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "timestamp",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		FunctionKey:    zapcore.OmitKey,
		MessageKey:     "message",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	// Create encoder based on format
	var encoder zapcore.Encoder
	if format == "json" {
		encoder = zapcore.NewJSONEncoder(encoderConfig)
	} else {
		encoder = zapcore.NewConsoleEncoder(encoderConfig)
	}

	// Create output writer
	var writer zapcore.WriteSyncer

	switch outputPath {
	case "stdout":
		writer = zapcore.AddSync(os.Stdout)
	case "stderr":
		writer = zapcore.AddSync(os.Stderr)
	default:
		file, err := os.OpenFile(outputPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return nil, fmt.Errorf("failed to open log file: %w", err)
		}

		writer = zapcore.AddSync(file)
	}

	// Create core
	core := zapcore.NewCore(encoder, writer, zapLevel)

	// Create logger
	zapLogger := zap.New(core, zap.AddCaller())
	sugaredLogger := zapLogger.Sugar()

	return &Logger{sugaredLogger}, nil
}

// Tee returns a logger that writes every entry to l's output and to core. core
// receives only the entries l writes (l's level applies to it as well); l is
// unchanged.
func (l *Logger) Tee(core zapcore.Core) (*Logger, error) {
	base := l.Desugar()

	leveled, err := zapcore.NewIncreaseLevelCore(core, zapcore.LevelOf(base.Core()))
	if err != nil {
		return nil, fmt.Errorf("tee logger: %w", err)
	}

	tee := zap.WrapCore(func(c zapcore.Core) zapcore.Core { return zapcore.NewTee(c, leveled) })

	return &Logger{base.WithOptions(tee).Sugar()}, nil
}

// Sync flushes any buffered log entries.
func (l *Logger) Sync() error {
	return l.SugaredLogger.Sync()
}
