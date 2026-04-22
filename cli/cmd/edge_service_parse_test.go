package cmd

import (
	"testing"
)

func TestParseEdgeServiceStatus_dockerAllHealthy(t *testing.T) {
	t.Parallel()
	raw := `NAME           SERVICE      STATUS
edge-caddy     caddy        Up 2 hours
edge-mist      mistserver   Up 2 hours
edge-helm      helmsman     Up 2 hours (healthy)
`
	checks := parseEdgeServiceStatus(raw, "docker")
	if len(checks) != 3 {
		t.Fatalf("expected 3 services detected, got %d: %+v", len(checks), checks)
	}
	for _, c := range checks {
		if !c.OK {
			t.Errorf("expected %s OK, got %+v", c.Name, c)
		}
	}
}

func TestParseEdgeServiceStatus_dockerUnhealthyFails(t *testing.T) {
	t.Parallel()
	raw := `NAME           SERVICE      STATUS
edge-caddy     caddy        Up 2 hours
edge-mist      mistserver   Up 2 hours (unhealthy)
edge-helm      helmsman     Exited (1) 5 minutes ago
`
	checks := parseEdgeServiceStatus(raw, "docker")
	state := map[string]bool{}
	for _, c := range checks {
		state[c.Name] = c.OK
	}
	if !state["caddy"] {
		t.Errorf("caddy should be OK")
	}
	if state["mistserver"] {
		t.Errorf("mistserver should be flagged (unhealthy)")
	}
	if state["helmsman"] {
		t.Errorf("helmsman should be flagged (exited)")
	}
}

func TestParseEdgeServiceStatus_nativeSystemctl(t *testing.T) {
	t.Parallel()
	raw := `● frameworks-caddy.service
   Active: active (running) since ...
● frameworks-helmsman.service
   Active: failed (Result: exit-code) since ...
● frameworks-mistserver.service
   Active: active (running) since ...
`
	checks := parseEdgeServiceStatus(raw, "native")
	state := map[string]bool{}
	for _, c := range checks {
		state[c.Name] = c.OK
	}
	if !state["caddy"] {
		t.Errorf("caddy should be active")
	}
	if !state["mistserver"] {
		t.Errorf("mistserver should be active")
	}
	if state["helmsman"] {
		t.Errorf("helmsman should be flagged (failed)")
	}
}

func TestParseEdgeServiceStatus_missingServiceOmittedNotFabricated(t *testing.T) {
	t.Parallel()
	// Output mentions only caddy. Parser must not emit fake "down" entries
	// for mistserver/helmsman — that would produce misleading remediation.
	raw := `NAME        SERVICE   STATUS
edge-caddy  caddy     Up 2 hours
`
	checks := parseEdgeServiceStatus(raw, "docker")
	if len(checks) != 1 {
		t.Fatalf("expected only 1 service detected, got %d: %+v", len(checks), checks)
	}
	if checks[0].Name != "caddy" {
		t.Errorf("expected caddy, got %s", checks[0].Name)
	}
}
