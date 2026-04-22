package cmd

import (
	"strings"
	"testing"
)

func TestStatusControlPlaneFallbacks_leadsWithDoctorDeep(t *testing.T) {
	t.Parallel()
	fb := statusControlPlaneFallbacks()
	if len(fb) == 0 {
		t.Fatal("expected at least one fallback next-step")
	}
	if !strings.HasPrefix(fb[0].Cmd, "frameworks cluster doctor --deep") {
		t.Errorf("first fallback = %q, want 'frameworks cluster doctor --deep'", fb[0].Cmd)
	}
}

func TestStatusControlPlaneFallbacks_allEntriesAreRunnable(t *testing.T) {
	t.Parallel()
	for i, s := range statusControlPlaneFallbacks() {
		if s.Cmd == "" {
			t.Errorf("fallback[%d]: expected a runnable Cmd, got empty (Why=%q)", i, s.Why)
		}
	}
}
