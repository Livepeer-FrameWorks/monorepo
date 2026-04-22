package readiness

import (
	"strings"
	"testing"
)

func TestEdgeReadiness_missingEnv_signalsNotEnrolled(t *testing.T) {
	r := EdgeReadiness(EdgeInputs{HasEnv: false})
	if len(r.Warnings) != 1 {
		t.Fatalf("expected one warning, got %d", len(r.Warnings))
	}
	w := r.Warnings[0]
	if w.Subject != "edge.not-enrolled" {
		t.Fatalf("unexpected subject: %s", w.Subject)
	}
	if !strings.Contains(w.Remediation.Cmd, "edge deploy") {
		t.Fatalf("expected enroll hint, got %q", w.Remediation.Cmd)
	}
}

func TestEdgeReadiness_httpsFailure_suggestsCaddyLogs(t *testing.T) {
	r := EdgeReadiness(EdgeInputs{HasEnv: true, HTTPSError: "i/o timeout"})
	if r.OK() {
		t.Fatal("expected warnings")
	}
	found := false
	for _, w := range r.Warnings {
		if w.Subject == "edge.https" && strings.Contains(w.Remediation.Cmd, "caddy") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing caddy-logs remediation: %+v", r.Warnings)
	}
}

func TestEdgeReadiness_ulimitFailure_suggestsEdgeTune(t *testing.T) {
	r := EdgeReadiness(EdgeInputs{
		HasEnv: true, HTTPSStatus: 200,
		HostChecks: []EdgeCheck{{Name: "Ulimit nofile", OK: false, Detail: "too low"}},
	})
	found := false
	for _, w := range r.Warnings {
		if strings.HasPrefix(w.Subject, "edge.host.") && strings.Contains(w.Remediation.Cmd, "edge tune") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing edge-tune remediation: %+v", r.Warnings)
	}
}

func TestEdgeReadiness_allHealthy_isOK(t *testing.T) {
	r := EdgeReadiness(EdgeInputs{HasEnv: true, HTTPSStatus: 200})
	if !r.OK() {
		t.Fatalf("expected OK, got warnings: %+v", r.Warnings)
	}
}

func TestEdgeReadiness_serviceDown_adaptsToServiceName(t *testing.T) {
	r := EdgeReadiness(EdgeInputs{
		HasEnv: true, HTTPSStatus: 200,
		ServiceChecks: []EdgeCheck{
			{Name: "mistserver", OK: false, Detail: "exited 1"},
		},
	})
	found := false
	for _, w := range r.Warnings {
		if w.Subject == "edge.service.mistserver" && strings.Contains(w.Remediation.Cmd, "edge logs mistserver") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing service-logs remediation: %+v", r.Warnings)
	}
}
