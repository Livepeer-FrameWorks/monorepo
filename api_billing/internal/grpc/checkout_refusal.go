package grpc

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// checkoutPolicyError retains the domain cause when exposing a caller-visible status.
// The status code alone cannot distinguish a purchase refusal from repairable setup.
type checkoutPolicyError struct {
	cause  error
	status *status.Status
}

func (e *checkoutPolicyError) Error() string              { return e.status.Err().Error() }
func (e *checkoutPolicyError) Unwrap() error              { return e.cause }
func (e *checkoutPolicyError) GRPCStatus() *status.Status { return e.status }

func checkoutPolicyStatus(code codes.Code, message string, cause error) error {
	return &checkoutPolicyError{cause: cause, status: status.New(code, message)}
}
