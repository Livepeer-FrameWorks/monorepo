package auth

import (
	"crypto/subtle"
	"errors"
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
