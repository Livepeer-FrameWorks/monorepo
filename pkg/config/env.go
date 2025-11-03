package config

import (
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
	"github.com/sirupsen/logrus"
)

// LoadEnv loads environment variables from .env file
func LoadEnv(logger *logrus.Logger) {
	files := []string{".env", ".env.dev"}
	loaded := make([]string, 0, len(files))
	for _, file := range files {
		if _, err := os.Stat(file); err != nil {
			continue
		}
		if err := godotenv.Overload(file); err != nil {
			if logger != nil {
				logger.WithError(err).Warnf("Failed to load %s", file)
			}
			continue
		}
		loaded = append(loaded, file)
	}
	if len(loaded) == 0 {
		if logger != nil {
			logger.Debug("No local env files loaded; relying on process environment")
		}
	} else {
		if logger != nil {
			logger.Debugf("Loaded env files: %s", strings.Join(loaded, ", "))
		}
	}
}

// GetEnv gets an environment variable with a default value
func GetEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// GetEnvInt gets an integer environment variable with a default value
func GetEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			return parsed
		}
	}
	return defaultValue
}

// GetEnvBool gets a boolean environment variable with a default value
func GetEnvBool(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		if parsed, err := strconv.ParseBool(value); err == nil {
			return parsed
		}
	}
	return defaultValue
}

// GetLogLevel gets the log level from environment
func GetLogLevel() logrus.Level {
	switch os.Getenv("LOG_LEVEL") {
	case "debug":
		return logrus.DebugLevel
	case "warn":
		return logrus.WarnLevel
	case "error":
		return logrus.ErrorLevel
	default:
		return logrus.InfoLevel
	}
}

// RequireEnv fetches a variable and exits the process if it is empty.
func RequireEnv(key string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		logrus.Fatalf("environment variable %s is required but not set", key)
	}
	return value
}
