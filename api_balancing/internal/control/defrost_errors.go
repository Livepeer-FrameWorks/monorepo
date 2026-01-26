package control

import "fmt"

// DefrostingError indicates an asset is being warmed from S3 and should be retried later.
type DefrostingError struct {
	RetryAfterSeconds int
	Message           string
}

func (e *DefrostingError) Error() string {
	if e == nil {
		return "defrosting"
	}
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("defrosting (retry after %ds)", e.RetryAfterSeconds)
}

// NewDefrostingError creates a defrosting error with a retry hint.
func NewDefrostingError(retryAfterSeconds int, message string) *DefrostingError {
	return &DefrostingError{
		RetryAfterSeconds: retryAfterSeconds,
		Message:           message,
	}
}
