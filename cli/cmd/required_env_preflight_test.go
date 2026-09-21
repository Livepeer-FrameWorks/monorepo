package cmd

import (
	"context"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
)

func alertingManifest() *inventory.Manifest {
	return &inventory.Manifest{
		Profile: "dev",
		Hosts: map[string]inventory.Host{
			"central-eu-1": {Name: "central-eu-1", ExternalIP: "10.0.0.1"},
		},
		Services: map[string]inventory.ServiceConfig{
			"lookout": {Enabled: true, Host: "central-eu-1"},
			"bridge":  {Enabled: true, Host: "central-eu-1"},
		},
		Observability: map[string]inventory.ServiceConfig{
			"vmalert":      {Enabled: true, Host: "central-eu-1"},
			"alertmanager": {Enabled: true, Host: "central-eu-1"},
		},
	}
}

func TestMissingRequiredExternalEnvTreatsBlankAsMissing(t *testing.T) {
	missing := missingRequiredExternalEnv("lookout", map[string]string{"LOOKOUT_ALERTMANAGER_TOKEN": "  "})
	if len(missing) != 1 || missing[0].Key != "LOOKOUT_ALERTMANAGER_TOKEN" {
		t.Fatalf("missing = %+v, want LOOKOUT_ALERTMANAGER_TOKEN", missing)
	}
	if missing := missingRequiredExternalEnv("lookout", map[string]string{"LOOKOUT_ALERTMANAGER_TOKEN": "token"}); len(missing) != 0 {
		t.Fatalf("missing = %+v, want none", missing)
	}
}

func TestAlertmanagerInternalOnlyRequiresLookoutButNotExternalDestinations(t *testing.T) {
	env := map[string]string{
		"ALERTMANAGER_EXTERNAL_NOTIFICATIONS_ENABLED": "false",
		"LOOKOUT_ALERTMANAGER_TOKEN":                  "token",
	}
	if missing := missingRequiredExternalEnv("alertmanager", env); len(missing) != 0 {
		t.Fatalf("missing = %+v, want none for internal-only Alertmanager", missing)
	}

	delete(env, "LOOKOUT_ALERTMANAGER_TOKEN")
	missing := missingRequiredExternalEnv("alertmanager", env)
	if len(missing) != 1 || missing[0].Key != "LOOKOUT_ALERTMANAGER_TOKEN" {
		t.Fatalf("missing = %+v, want only LOOKOUT_ALERTMANAGER_TOKEN", missing)
	}
}

// Provision checks every planned alerting input before the first batch, so a
// missing SMTP or heartbeat value fails the run instead of the Ansible assert
// after vmagent and vmalert already changed.
func TestProvisionPlanPreflightReportsAlertmanagerInputs(t *testing.T) {
	manifest := alertingManifest()
	plan, err := orchestrator.NewPlanner(manifest).Plan(context.Background(), orchestrator.ProvisionOptions{Phase: orchestrator.PhaseInterfaces})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	var rendered []string
	gaps, err := collectPlanRequiredEnvGaps(plan, func(task *orchestrator.Task) (map[string]string, error) {
		rendered = append(rendered, task.Type)
		return map[string]string{
			"LOOKOUT_ALERTMANAGER_TOKEN": "token",
			"ALERTMANAGER_EMAIL_TO":      "ops@example.com",
			"SMTP_PORT":                  "587",
		}, nil
	})
	if err != nil {
		t.Fatalf("collect gaps: %v", err)
	}
	if len(rendered) != 1 || rendered[0] != "alertmanager" {
		t.Fatalf("rendered %v, want only the service that declares operator inputs", rendered)
	}
	msg := requiredEnvPreflightError(gaps).Error()
	for _, key := range []string{"ALERTMANAGER_HEARTBEAT_URL", "SMTP_HOST", "FROM_EMAIL"} {
		if !strings.Contains(msg, "alertmanager on central-eu-1: "+key) {
			t.Fatalf("preflight error missing %s:\n%s", key, msg)
		}
	}
	for _, present := range []string{"LOOKOUT_ALERTMANAGER_TOKEN", "ALERTMANAGER_EMAIL_TO", "SMTP_PORT"} {
		if strings.Contains(msg, ": "+present+" ") {
			t.Fatalf("preflight error lists present key %s:\n%s", present, msg)
		}
	}
}

// release apply renders each replica it will install or upgrade and fails
// before any mutation when Lookout lacks its webhook token.
func TestReleasePreflightReportsMissingLookoutToken(t *testing.T) {
	manifest := alertingManifest()
	var rendered []string
	gaps, err := collectReleaseRequiredEnvGaps(manifest, []string{"bridge", "lookout"}, func(serviceName, deployName string, host inventory.Host) (map[string]string, error) {
		rendered = append(rendered, serviceName+"@"+host.Name)
		return map[string]string{}, nil
	})
	if err != nil {
		t.Fatalf("collect gaps: %v", err)
	}
	if len(rendered) != 1 || rendered[0] != "lookout@central-eu-1" {
		t.Fatalf("rendered %v, want only lookout's replica", rendered)
	}
	err = requiredEnvPreflightError(gaps)
	if err == nil || !strings.Contains(err.Error(), "lookout on central-eu-1: LOOKOUT_ALERTMANAGER_TOKEN") {
		t.Fatalf("preflight error = %v, want the missing Lookout token", err)
	}

	gaps, err = collectReleaseRequiredEnvGaps(manifest, []string{"lookout"}, func(string, string, inventory.Host) (map[string]string, error) {
		return map[string]string{"LOOKOUT_ALERTMANAGER_TOKEN": "token"}, nil
	})
	if err != nil || requiredEnvPreflightError(gaps) != nil {
		t.Fatalf("gaps = %+v err = %v, want none when the token is set", gaps, err)
	}
}
