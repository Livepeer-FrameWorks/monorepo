package cmd

import (
	"testing"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
)

// One manifest switch renders the private-destination setting for Bosun and
// for both playback-auth webhook services; a service's config map still wins.
func TestBuildServiceEnvVarsWebhooksAllowPrivateDestinations(t *testing.T) {
	build := func(t *testing.T, manifest *inventory.Manifest, service string) map[string]string {
		t.Helper()
		env, err := buildServiceEnvVars(&orchestrator.Task{Name: service, Type: service, ServiceID: service},
			manifest, map[string]any{}, "", "", map[string]string{}, nil, "native")
		if err != nil {
			t.Fatalf("buildServiceEnvVars(%s): %v", service, err)
		}
		return env
	}
	manifest := &inventory.Manifest{
		Profile:  "dev",
		Webhooks: &inventory.WebhooksConfig{AllowPrivateDestinations: true},
		Services: map[string]inventory.ServiceConfig{
			"bosun":     {Enabled: true},
			"commodore": {Enabled: true},
			"foghorn":   {Enabled: true},
		},
	}
	want := map[string]string{
		"bosun":     "BOSUN_ALLOW_PRIVATE_DESTINATIONS",
		"commodore": "PLAYBACK_WEBHOOK_ALLOW_PRIVATE_DESTINATIONS",
		"foghorn":   "PLAYBACK_WEBHOOK_ALLOW_PRIVATE_DESTINATIONS",
	}
	for service, key := range want {
		if got := build(t, manifest, service)[key]; got != "true" {
			t.Errorf("%s %s = %q, want true", service, key, got)
		}
	}
	if _, ok := build(t, manifest, "commodore")["BOSUN_ALLOW_PRIVATE_DESTINATIONS"]; ok {
		t.Error("commodore received Bosun's setting")
	}

	manifest.Services["bosun"] = inventory.ServiceConfig{Enabled: true, Config: map[string]string{"BOSUN_ALLOW_PRIVATE_DESTINATIONS": "false"}}
	if got := build(t, manifest, "bosun")["BOSUN_ALLOW_PRIVATE_DESTINATIONS"]; got != "false" {
		t.Errorf("service config override lost: BOSUN_ALLOW_PRIVATE_DESTINATIONS = %q, want false", got)
	}

	manifest.Webhooks = nil
	for service, key := range want {
		if service == "bosun" {
			continue
		}
		if got, ok := build(t, manifest, service)[key]; ok {
			t.Errorf("without the manifest switch %s %s = %q, want unset", service, key, got)
		}
	}
}
