package cmd

import (
	"reflect"
	"strings"
	"testing"
)

func TestControlPlaneDomainFinalizeOnly(t *testing.T) {
	tests := []struct {
		domain string
		want   string
	}{
		{domain: clusterControlPlaneDomainAll, want: clusterFinalizeOnlyAll},
		{domain: clusterControlPlaneDomainQuartermaster, want: clusterFinalizeOnlyQuartermaster},
		{domain: clusterControlPlaneDomainBilling, want: clusterFinalizeOnlyPurser},
		{domain: clusterControlPlaneDomainAccounts, want: clusterFinalizeOnlyCommodore},
		{domain: clusterControlPlaneDomainAssignments, want: clusterFinalizeOnlyAssignments},
		{domain: clusterControlPlaneDomainValidation, want: clusterFinalizeOnlyValidation},
	}

	for _, tt := range tests {
		t.Run(tt.domain, func(t *testing.T) {
			got, err := controlPlaneDomainFinalizeOnly(tt.domain)
			if err != nil {
				t.Fatalf("controlPlaneDomainFinalizeOnly returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("controlPlaneDomainFinalizeOnly(%q) = %q, want %q", tt.domain, got, tt.want)
			}
		})
	}
}

func TestControlPlaneDomainRejectsUnknownDomain(t *testing.T) {
	if _, err := controlPlaneDomainFinalizeOnly("purser"); err == nil {
		t.Fatal("expected legacy service name to be rejected as a control-plane domain")
	}
}

func TestControlPlaneBillingPlanUsesExistingPurserSteps(t *testing.T) {
	only, err := controlPlaneDomainFinalizeOnly(clusterControlPlaneDomainBilling)
	if err != nil {
		t.Fatalf("map billing domain: %v", err)
	}
	got, err := clusterFinalizePlan(only, false)
	if err != nil {
		t.Fatalf("build billing plan: %v", err)
	}
	want := []clusterFinalizeStep{
		clusterFinalizeStepPurserBootstrap,
		clusterFinalizeStepPurserValidate,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("billing plan = %#v, want %#v", got, want)
	}
}

func TestClusterCommandIncludesControlPlaneSurface(t *testing.T) {
	cmd := newClusterCmd()
	controlPlane, _, err := cmd.Find([]string{"control-plane"})
	if err != nil {
		t.Fatalf("find control-plane command: %v", err)
	}
	if controlPlane == nil || controlPlane.Use != "control-plane" {
		t.Fatalf("control-plane command not registered: %#v", controlPlane)
	}

	for _, name := range []string{"plan", "reconcile"} {
		subcommand, _, findErr := controlPlane.Find([]string{name})
		if findErr != nil {
			t.Fatalf("find control-plane %s: %v", name, findErr)
		}
		if subcommand == nil || subcommand.Name() != name {
			t.Fatalf("control-plane %s command not registered: %#v", name, subcommand)
		}
		if subcommand.Flags().Lookup("domain") == nil {
			t.Fatalf("control-plane %s missing --domain flag", name)
		}
	}
}

func TestClusterFinalizeIsDeprecatedCompatibilityAlias(t *testing.T) {
	cmd := newClusterFinalizeCmd()
	if !strings.Contains(cmd.Deprecated, "control-plane reconcile") {
		t.Fatalf("finalize deprecation = %q", cmd.Deprecated)
	}
	if cmd.Flags().Lookup("only") == nil {
		t.Fatal("deprecated finalize command must retain --only")
	}
	if cmd.Flags().Lookup("bootstrap-reset-credentials") == nil {
		t.Fatal("deprecated finalize command must retain bootstrap flags")
	}
}

func TestControlPlaneStepDescriptionsAreOperatorFacing(t *testing.T) {
	for _, step := range []clusterFinalizeStep{
		clusterFinalizeStepQuartermaster,
		clusterFinalizeStepPurserBootstrap,
		clusterFinalizeStepPurserValidate,
		clusterFinalizeStepCommodore,
		clusterFinalizeStepAssignments,
		clusterFinalizeStepControlPlane,
	} {
		got := controlPlaneStepDescription(step)
		if got == string(step) || !strings.Contains(got, ":") {
			t.Fatalf("step %q has non-descriptive plan output %q", step, got)
		}
	}
}
