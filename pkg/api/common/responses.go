package common

// ErrorResponse represents a standard error response used across all services
type ErrorResponse struct {
	Error   string                 `json:"error"`
	Code    string                 `json:"code,omitempty"`    // HTTP-like error code
	Service string                 `json:"service,omitempty"` // Which service generated the error
	Details map[string]interface{} `json:"details,omitempty"` // Additional error context
}

// SuccessResponse represents a standard success response used across all services
type SuccessResponse struct {
	Success bool        `json:"success"`
	Message string      `json:"message,omitempty"`
	Data    interface{} `json:"data,omitempty"`
}

// ValidationErrorResponse represents a validation error with field-specific details
type ValidationErrorResponse struct {
	Error  string            `json:"error"`
	Fields map[string]string `json:"fields,omitempty"` // field_name -> error_message
}
