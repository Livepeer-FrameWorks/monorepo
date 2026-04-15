package auth

import (
	"crypto/subtle"
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

	if subtle.ConstantTimeCompare([]byte(token), []byte(expectedToken)) != 1 {
		return ErrInvalidServiceToken
	}

	return nil
}

// GetServiceToken gets the service token from environment
func GetServiceToken() string {
	return os.Getenv("SERVICE_TOKEN")
}
