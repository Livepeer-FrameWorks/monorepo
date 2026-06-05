package handlers

import (
	"net/http"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// mapGRPCErrorToHTTP is the trust boundary for the public node-enrollment
// endpoint: it must translate each control-plane gRPC code to a specific HTTP
// status AND a safe, caller-facing message. In particular auth failures must
// not leak why (both collapse to "bootstrap token rejected").
func TestMapGRPCErrorToHTTP(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
		wantMsg  string
	}{
		{"invalid argument passes message through", status.Error(codes.InvalidArgument, "bad node id"), http.StatusBadRequest, "bad node id"},
		{"unauthenticated is redacted", status.Error(codes.Unauthenticated, "token expired at ..."), http.StatusUnauthorized, "bootstrap token rejected"},
		{"permission denied is redacted same as unauthenticated", status.Error(codes.PermissionDenied, "tenant X not allowed"), http.StatusForbidden, "bootstrap token rejected"},
		{"not found passes message through", status.Error(codes.NotFound, "cluster missing"), http.StatusNotFound, "cluster missing"},
		{"failed precondition", status.Error(codes.FailedPrecondition, "cluster not ready"), http.StatusPreconditionFailed, "cluster not ready"},
		{"resource exhausted maps to 503 with operator hint", status.Error(codes.ResourceExhausted, "no /30 left"), http.StatusServiceUnavailable, "mesh address space exhausted; contact the operator"},
		{"unavailable maps to 503", status.Error(codes.Unavailable, "conn refused"), http.StatusServiceUnavailable, "control plane unavailable, retry later"},
		{"deadline exceeded maps to 504", status.Error(codes.DeadlineExceeded, "ctx deadline"), http.StatusGatewayTimeout, "control plane timed out, retry later"},
		{"already exists maps to 409", status.Error(codes.AlreadyExists, "node already enrolled"), http.StatusConflict, "node already enrolled"},
		{"unknown code falls through to 500 with generic message", status.Error(codes.Internal, "stack trace details"), http.StatusInternalServerError, "internal server error"},
	}
	for _, tt := range tests {
		gotCode, gotMsg := mapGRPCErrorToHTTP(tt.err)
		if gotCode != tt.wantCode {
			t.Errorf("%s: status = %d, want %d", tt.name, gotCode, tt.wantCode)
		}
		if gotMsg != tt.wantMsg {
			t.Errorf("%s: message = %q, want %q", tt.name, gotMsg, tt.wantMsg)
		}
	}
}
