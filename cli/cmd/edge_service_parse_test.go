package cmd

import (
	"testing"
)

func TestParseEdgeServiceStatus_containerHealthy(t *testing.T) {
	t.Parallel()
	raw := `NAME              SERVICE   STATUS
frameworks-edge   edge      Up 2 hours (healthy)
`
	checks := parseEdgeServiceStatus(raw, "container")
	if len(checks) != 1 {
		t.Fatalf("expected 1 service detected, got %d: %+v", len(checks), checks)
	}
	if checks[0].Name != "edge" || !checks[0].OK {
		t.Errorf("expected edge OK, got %+v", checks[0])
	}
}

func TestParseEdgeServiceStatus_containerUnhealthyFails(t *testing.T) {
	t.Parallel()
	raw := `NAME              SERVICE   STATUS
frameworks-edge   edge      Up 2 hours (unhealthy)
`
	checks := parseEdgeServiceStatus(raw, "container")
	if len(checks) != 1 {
		t.Fatalf("expected 1 service detected, got %d: %+v", len(checks), checks)
	}
	if checks[0].OK {
		t.Errorf("edge should be flagged (unhealthy), got %+v", checks[0])
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
	raw := `NAME              SERVICE   STATUS
something-else    other     Up 2 hours
`
	checks := parseEdgeServiceStatus(raw, "container")
	if len(checks) != 0 {
		t.Fatalf("expected no services detected, got %d: %+v", len(checks), checks)
	}
}

// "(health: starting)" is warm-up, not health — the s6 stack may still be
// seeding. Reporting green before the healthcheck passes would let a drift
// CI gate pass on a node that never reaches (healthy).
func TestParseEdgeServiceStatus_healthStartingIsNotHealthy(t *testing.T) {
	t.Parallel()
	raw := `NAME              SERVICE   STATUS
frameworks-edge   edge      Up 5 seconds (health: starting)
`
	checks := parseEdgeServiceStatus(raw, "container")
	if len(checks) != 1 {
		t.Fatalf("expected 1 check, got %d: %+v", len(checks), checks)
	}
	if checks[0].OK {
		t.Fatalf("health: starting must not report OK: %+v", checks[0])
	}
}

// A dead frameworks-edge container is absent from default compose ps
// output; its sibling containers (vmagent, tuning) must never satisfy the
// "edge" match — that would report a healthy stack during a full outage.
func TestParseEdgeServiceStatus_siblingContainersDoNotMaskDeadEdge(t *testing.T) {
	t.Parallel()
	raw := `NAME                      SERVICE       STATUS
frameworks-edge-vmagent   vmagent       Up 2 hours
frameworks-edge-tuning    edge-tuning   Exited (0) 2 hours ago
`
	checks := parseEdgeServiceStatus(raw, "container")
	if len(checks) != 0 {
		t.Fatalf("sibling containers must not stand in for the edge service; got %+v", checks)
	}
}

func TestEdgeFoghornUsesInternalCA(t *testing.T) {
	t.Parallel()
	cases := []struct {
		addr string
		want bool
	}{
		{addr: "foghorn.internal:18019", want: true},
		{addr: "foghorn.media-eu-1.frameworks.network:18019", want: false},
		{addr: "foghorn.media-eu-1.frameworks.network:18029", want: false},
		{addr: "foghorn.frameworks.network:18019", want: false},
		{addr: "foghorn.frameworks.network:18029", want: false},
		{addr: "foghorn.frameworks.network:443", want: false},
		{addr: "api.frameworks.network:18019", want: false},
	}
	for _, tt := range cases {
		if got := edgeFoghornUsesInternalCA(tt.addr); got != tt.want {
			t.Fatalf("edgeFoghornUsesInternalCA(%q) = %v, want %v", tt.addr, got, tt.want)
		}
	}
}

func TestEdgeManifestFoghornGRPCAddrUsesExternalListener(t *testing.T) {
	t.Parallel()

	got := edgeManifestFoghornGRPCAddr("frameworks.network", "media-eu-1")
	if got != "foghorn.media-eu-1.frameworks.network:18029" {
		t.Fatalf("edgeManifestFoghornGRPCAddr() = %q, want external listener", got)
	}
}
