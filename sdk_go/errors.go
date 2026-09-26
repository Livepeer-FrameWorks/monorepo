package frameworks

import (
	"encoding/json"
	"fmt"
)

// ErrorInfo is the part every SDK error shares. The concrete error types
// embed it; errors.As on the concrete type says what failed. The type names
// are shared with the TypeScript and Python SDKs.
type ErrorInfo struct {
	Message string
	// Status is the HTTP status of the response, or nil when the failure was
	// not an HTTP status.
	Status *int
	// Code is extensions.code of a GraphQL error or the code field of an HTTP
	// error body; empty when there is none.
	Code string
	// RetryAfterSeconds is the wait the server asked for, from Retry-After or
	// the error itself.
	RetryAfterSeconds *int
	// Body is the parsed response body, when there was one.
	Body any
	// Cause is the underlying error, when there is one.
	Cause error
}

func (e *ErrorInfo) Error() string { return e.Message }

// Unwrap returns the underlying error.
func (e *ErrorInfo) Unwrap() error { return e.Cause }

// Info returns the shared fields of the error.
func (e *ErrorInfo) Info() *ErrorInfo { return e }

// Error is implemented by every error the SDK returns for a failed call.
type Error interface {
	error
	// Kind is the error type's name, e.g. "RateLimitError".
	Kind() string
	Info() *ErrorInfo
}

// NetworkError means the request never produced a response: DNS, TCP, TLS,
// or a reset connection.
type NetworkError struct{ ErrorInfo }

func (*NetworkError) Kind() string { return "NetworkError" }

// HTTPError is a non-2xx response without a GraphQL body that no more
// specific type covers.
type HTTPError struct{ ErrorInfo }

func (*HTTPError) Kind() string { return "HTTPError" }

// AuthenticationError means the credentials were missing, invalid, or
// expired: HTTP 401, a GraphQL UNAUTHORIZED error, or a subscription socket
// closed before it was acknowledged.
type AuthenticationError struct{ ErrorInfo }

func (*AuthenticationError) Kind() string { return "AuthenticationError" }

// PaymentRequiredError is HTTP 402: the call needs a balance, a payment
// method, or an x402 payment (see Body).
type PaymentRequiredError struct{ ErrorInfo }

func (*PaymentRequiredError) Kind() string { return "PaymentRequiredError" }

// RateLimitError is HTTP 429 or a GraphQL RATE_LIMITED error.
// RetryAfterSeconds says when to try again.
type RateLimitError struct{ ErrorInfo }

func (*RateLimitError) Kind() string { return "RateLimitError" }

// ServerError is an HTTP 5xx response.
type ServerError struct{ ErrorInfo }

func (*ServerError) Kind() string { return "ServerError" }

// ProtocolError means the response was not a GraphQL response: not JSON, or
// neither data nor errors.
type ProtocolError struct{ ErrorInfo }

func (*ProtocolError) Kind() string { return "ProtocolError" }

// GraphQLErrorEntry is one entry of a GraphQL errors array.
type GraphQLErrorEntry struct {
	Message    string         `json:"message"`
	Path       []any          `json:"path,omitempty"`
	Locations  []any          `json:"locations,omitempty"`
	Extensions map[string]any `json:"extensions,omitempty"`
}

// GraphQLError means the server answered with GraphQL errors. Errors holds
// every entry; Data holds any partial result.
type GraphQLError struct {
	ErrorInfo
	Errors []GraphQLErrorEntry
	// Path is the path of the first entry.
	Path []any
	Data json.RawMessage
}

func (*GraphQLError) Kind() string { return "GraphQLError" }

// PartialErrors are the GraphQL errors of a call that still returned its
// data: each failed a field below a root field that came back, which the
// server set to null (for example Stream.metrics for an API token without
// analytics:read). The call returns its data and no error; these reach the
// handler set by WithPartialErrors or ClientOptions.OnPartialErrors. Errors
// without a path, errors that null a root field, and UNAUTHORIZED,
// RATE_LIMITED, and document errors fail the call instead.
type PartialErrors struct {
	// Operation is the operation name.
	Operation string
	Errors    []GraphQLErrorEntry
}

// SchemaMismatchError means the server rejected the document itself
// (GRAPHQL_VALIDATION_FAILED or GRAPHQL_PARSE_FAILED): it does not know a
// field or argument this SDK sends. errors.As also matches it as a
// *GraphQLError.
type SchemaMismatchError struct{ GraphQLError }

func (*SchemaMismatchError) Kind() string { return "SchemaMismatchError" }

// Unwrap returns the embedded *GraphQLError.
func (e *SchemaMismatchError) Unwrap() error { return &e.GraphQLError }

// ResultError is an error member of a result union, returned by ExpectResult.
type ResultError struct {
	ErrorInfo
	Typename string
	// Field is the input field a ValidationError names.
	Field        string
	Constraint   string
	ResourceType string
	ResourceID   string
	// Result is the union member as the server returned it.
	Result map[string]any
}

func (*ResultError) Kind() string { return "ResultError" }

// ServerTooOldError means the server is a stable release older than the
// oldest release this SDK line supports.
type ServerTooOldError struct {
	ErrorInfo
	// ServerVersion is nil when the server predates serverInfo.
	ServerVersion  *string
	MinimumVersion string
}

func (*ServerTooOldError) Kind() string { return "ServerTooOldError" }

func newServerTooOldError(version *string, minimum string) *ServerTooOldError {
	msg := fmt.Sprintf("FrameWorks server predates serverInfo; this client needs %s or later", minimum)
	if version != nil {
		msg = fmt.Sprintf("FrameWorks server %s is older than %s, the oldest release this client can use", *version, minimum)
	}
	return &ServerTooOldError{ErrorInfo: ErrorInfo{Message: msg}, ServerVersion: version, MinimumVersion: minimum}
}

// UnsupportedOperationError means the operation was added in a release newer
// than the server.
type UnsupportedOperationError struct {
	ErrorInfo
	Operation     string
	Since         string
	ServerVersion string
}

func (*UnsupportedOperationError) Kind() string { return "UnsupportedOperationError" }

// UploadError means a VOD upload failed while sending its parts. The upload
// was aborted.
type UploadError struct {
	ErrorInfo
	UploadID string
	// PartNumber is 0 when the failure was not a part.
	PartNumber int
}

func (*UploadError) Kind() string { return "UploadError" }

// WebhookVerificationError means the signature, timestamp, or headers of a
// webhook request did not verify.
type WebhookVerificationError struct{ ErrorInfo }

func (*WebhookVerificationError) Kind() string { return "WebhookVerificationError" }

func intPtr(v int) *int { return &v }
