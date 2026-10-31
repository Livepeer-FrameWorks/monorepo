package mollie

import "fmt"

// APIError preserves the HTTP outcome so callers can distinguish definite rejection from ambiguity.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("mollie status %d: %s", e.StatusCode, e.Message)
}
