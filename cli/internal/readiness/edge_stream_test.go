package readiness

import (
	"strings"
	"testing"
)

func TestEdgeReadinessStream_DomainlessStillGetsCmd(t *testing.T) {
	t.Parallel()
	r := EdgeReadiness(EdgeInputs{
		HasEnv:       true,
		HTTPSStatus:  200,
		StreamChecks: []EdgeCheck{{Name: "live+abc123", OK: false, Detail: "HLS FAIL"}},
	})
	if len(r.Warnings) != 1 {
		t.Fatalf("expected exactly one warning, got %d: %+v", len(r.Warnings), r.Warnings)
	}
	rem := r.Warnings[0].Remediation
	if rem.Cmd == "" {
		t.Fatalf("expected a runnable Cmd on domain-less stream failure, got empty (Why=%q)", rem.Why)
	}
	if !strings.Contains(rem.Cmd, "edge diagnose media --stream live+abc123") {
		t.Errorf("Cmd should diagnose the failing stream, got %q", rem.Cmd)
	}
	if rem.Why == "" {
		t.Error("expected a Why explanation even when Domain is empty")
	}
}

func TestEdgeReadinessStream_WithDomainMentionsDomainInWhy(t *testing.T) {
	t.Parallel()
	r := EdgeReadiness(EdgeInputs{
		HasEnv:       true,
		HTTPSStatus:  200,
		Domain:       "edge.example.com",
		StreamChecks: []EdgeCheck{{Name: "live+xyz", OK: false, Detail: "HLS FAIL"}},
	})
	if len(r.Warnings) != 1 {
		t.Fatalf("expected exactly one warning, got %+v", r.Warnings)
	}
	rem := r.Warnings[0].Remediation
	if !strings.Contains(rem.Cmd, "edge diagnose media --stream live+xyz") {
		t.Errorf("Cmd should diagnose the failing stream, got %q", rem.Cmd)
	}
	if !strings.Contains(rem.Why, "edge.example.com") {
		t.Errorf("Why should surface the domain as context, got %q", rem.Why)
	}
}
