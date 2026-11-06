package cmd

import (
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestLegacyDVRDiagnosticReadUnavailableAudit(t *testing.T) {
	if err := diagnosticAccessError("dvr", status.Error(codes.Unimplemented, "old Commodore")); !errors.Is(err, errDiagnosticAuditUnsupported) {
		t.Fatalf("legacy DVR read must remain usable with an explicit audit warning: %v", err)
	}
	for _, kind := range []string{"dvr", "stream", "billing"} {
		for _, code := range []codes.Code{codes.PermissionDenied, codes.Unauthenticated, codes.Unavailable} {
			if err := diagnosticAccessError(kind, status.Error(code, "rejected")); status.Code(err) != code || errors.Is(err, errDiagnosticAuditUnsupported) {
				t.Fatalf("%s diagnostic swallowed %s: %v", kind, code, err)
			}
		}
	}
	if err := diagnosticAccessError("billing", status.Error(codes.Unimplemented, "old Commodore")); errors.Is(err, errDiagnosticAuditUnsupported) || status.Code(err) != codes.Unimplemented {
		t.Fatalf("new diagnostics must retain their audit requirement: %v", err)
	}
}
