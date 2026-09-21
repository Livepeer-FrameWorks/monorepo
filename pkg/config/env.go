package config

import (
	"os"
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

// GetEnv gets an environment variable with a default value. Service startup
// configuration uses Load instead; this serves shared packages that read a
// process-wide variable at use time.
func GetEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// IsProduction reports whether the current process is running with production
// runtime settings. BUILD_ENV is the repo-wide runtime selector.
func IsProduction() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("BUILD_ENV"))) {
	case "production", "prod":
		return true
	default:
		return false
	}
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
