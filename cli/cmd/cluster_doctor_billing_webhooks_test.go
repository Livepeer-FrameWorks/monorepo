package cmd

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"

	"github.com/spf13/cobra"
)

func TestDoctorRegistersBillingWebhookCheckForPurserAlias(t *testing.T) {
	rc := &resolvedCluster{
		ManifestPath: filepath.Join(t.TempDir(), "cluster.yaml"),
		Manifest: &inventory.Manifest{
			Hosts:    map[string]inventory.Host{"core": {Name: "core", ExternalIP: "192.0.2.1"}},
			Services: map[string]inventory.ServiceConfig{"billing": {Enabled: true, Deploy: "purser", Host: "core"}},
		},
	}
	for _, section := range doctorSections(rc, nil, nil, "", "", false) {
		for _, check := range section.checks {
			if strings.HasPrefix(check.name, "Stripe billing webhooks") {
				if outcome := check.run(context.Background()); outcome.passed || len(outcome.steps) == 0 {
					t.Fatal("unverified subscriptions passed without a remediation")
				}
				return
			}
		}
	}
	t.Fatal("Purser webhook subscription check missing from cluster doctor")
}

func TestDoctorBillingWebhookEndpointFailsClosed(t *testing.T) {
	for _, status := range []string{"200", "404", "503"} {
		t.Run(status, func(t *testing.T) {
			var commands []string
			runner, err := replyRunnerFor(func(_, _ string) (string, int) { return status, 0 }, &commands)(inventory.Host{Name: "core"})
			if err != nil {
				t.Fatal(err)
			}
			result := checkServiceHTTPWithRunner(context.Background(), runner, "127.0.0.1", 18003, "/health/billing-webhooks", &health.CheckResult{Metadata: map[string]string{}})
			if result.OK != (status == "200") || len(commands) != 1 || !strings.Contains(commands[0], "/health/billing-webhooks") {
				t.Fatalf("webhook probe status=%s result=%+v commands=%v", status, result, commands)
			}
		})
	}
}

func TestDoctorBillingWebhookFailLineNamesMissingEvent(t *testing.T) {
	body := `{"status":"unhealthy","read_only":true,"reason":"missing_events","missing_events":["charge.updated"],` +
		`"message":"Stripe webhook endpoint https://gateway.example/webhooks/billing/stripe is missing required events: charge.updated"}`
	var commands []string
	runner, err := replyRunnerFor(func(_, command string) (string, int) {
		if strings.Contains(command, "/dev/null") {
			return "503", 0
		}
		return body + "\n503", 0
	}, &commands)(inventory.Host{Name: "core"})
	if err != nil {
		t.Fatal(err)
	}
	result := checkServiceHTTPWithRunner(context.Background(), runner, "127.0.0.1", 18003, "/health/billing-webhooks", &health.CheckResult{Metadata: map[string]string{}})
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	printHealthResult(cmd, "Stripe billing webhooks (purser)", result)
	line := out.String()
	if result.OK || !strings.HasPrefix(line, "[FAIL]") || !strings.Contains(line, "returned 503") || !strings.Contains(line, "charge.updated") || strings.Contains(line, "{") {
		t.Fatalf("doctor FAIL line does not name the missing event: %q", line)
	}
}
