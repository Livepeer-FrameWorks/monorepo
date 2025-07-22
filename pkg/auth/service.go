package auth

import (
	"errors"
	"os"
)

var (
	ErrMissingServiceToken = errors.New("service token not provided")
	ErrInvalidServiceToken = errors.New("invalid service token")
)

// ValidateServiceToken validates a service-to-service auth token
func ValidateServiceToken(token string, expectedToken string) error {
	if token == "" {
		return ErrMissingServiceToken
	}

	if token != expectedToken {
		return ErrInvalidServiceToken
	}

	return nil
}

// GetServiceToken gets the service token from environment
func GetServiceToken() string {
	return os.Getenv("SERVICE_TOKEN")
}
