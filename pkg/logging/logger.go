package logging

import (
	"github.com/sirupsen/logrus"

	"frameworks/pkg/config"
)

// Logger represents a logger instance
type Logger = *logrus.Logger

// Fields represents structured logging fields
type Fields = logrus.Fields

// Level represents a log level
type Level = logrus.Level

// Log levels
const (
	DebugLevel = logrus.DebugLevel
	InfoLevel  = logrus.InfoLevel
	WarnLevel  = logrus.WarnLevel
	ErrorLevel = logrus.ErrorLevel
)

// NewLogger creates a new configured logger instance
func NewLogger() *logrus.Logger {
	logger := logrus.New()
	logger.SetFormatter(&logrus.JSONFormatter{})
	logger.SetLevel(config.GetLogLevel())
	return logger
}

// NewLoggerWithService creates a logger with a service field
func NewLoggerWithService(serviceName string) *logrus.Logger {
	logger := NewLogger()

	// Add service name to all log entries
	logger = logger.WithField("service", serviceName).Logger

	return logger
}
