package provisioner

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
)

func alertingTestHelpers(t *testing.T, wantArtifact string) RoleBuildHelpers {
	t.Helper()
	return RoleBuildHelpers{
		DetectRemoteOS: func(context.Context, inventory.Host) (string, string, error) {
			return "linux", "amd64", nil
		},
		ResolveArtifact: func(name, arch, channel string, metadata map[string]any) (ResolvedArtifact, error) {
			if name != wantArtifact {
				t.Fatalf("ResolveArtifact name = %q, want %q", name, wantArtifact)
			}
			return ResolvedArtifact{URL: "https://example.test/" + name + ".tar.gz", Checksum: "sha256:" + name, Version: "vtest"}, nil
		},
	}
}

func TestPrometheusStackRoleVarsPassVMAgentExternalLabels(t *testing.T) {
	labels := map[string]string{"region": "us-east", "cluster": "regional-us-primary"}
	vars, err := prometheusStackRoleVars(context.Background(), inventory.Host{}, ServiceConfig{
		Metadata: map[string]any{
			"component":        "vmagent",
			"platform_channel": "stable",
			"external_labels":  labels,
		},
	}, alertingTestHelpers(t, "vmagent"))
	if err != nil {
		t.Fatalf("prometheusStackRoleVars: %v", err)
	}
	if got := vars["vmagent_external_labels"]; !reflect.DeepEqual(got, labels) {
		t.Fatalf("vmagent_external_labels = %#v, want %#v", got, labels)
	}
}

func TestPrometheusStackRoleVarsVMAlertShipsEmbeddedRulesAndEndpoints(t *testing.T) {
	vars, err := prometheusStackRoleVars(context.Background(), inventory.Host{}, ServiceConfig{
		Port: 8880,
		EnvVars: map[string]string{
			"VMALERT_DATASOURCE_URL": "http://victoriametrics.internal:8428/",
			"VMALERT_NOTIFIER_URL":   "http://alertmanager.internal:9093/, http://alertmanager-b.internal:9093",
		},
		Metadata: map[string]any{"component": "vmalert", "platform_channel": "stable"},
	}, alertingTestHelpers(t, "vmalert"))
	if err != nil {
		t.Fatalf("prometheusStackRoleVars: %v", err)
	}
	if got := vars["prometheus_stack_components"]; !reflect.DeepEqual(got, []string{"vmalert"}) {
		t.Fatalf("prometheus_stack_components = %#v", got)
	}
	if got := vars["vmalert_artifact_url"]; got != "https://example.test/vmalert.tar.gz" {
		t.Fatalf("vmalert_artifact_url = %v", got)
	}
	if got := vars["vmalert_datasource_url"]; got != "http://victoriametrics.internal:8428" {
		t.Fatalf("vmalert_datasource_url = %v", got)
	}
	wantNotifiers := []string{"http://alertmanager.internal:9093", "http://alertmanager-b.internal:9093"}
	if got := vars["vmalert_notifier_urls"]; !reflect.DeepEqual(got, wantNotifiers) {
		t.Fatalf("vmalert_notifier_urls = %#v, want %#v", got, wantNotifiers)
	}
	if got := vars["vmalert_port"]; got != 8880 {
		t.Fatalf("vmalert_port = %v, want 8880", got)
	}
	files, ok := vars["vmalert_rule_files"].([]map[string]any)
	if !ok || len(files) == 0 {
		t.Fatalf("vmalert_rule_files = %#v, want embedded rule files", vars["vmalert_rule_files"])
	}
	var found bool
	for _, file := range files {
		if file["name"] == "frameworks.yml" {
			content, _ := file["content"].(string)
			for _, alert := range []string{"RegionalTelemetryStale", "MM2ReplicationLagHigh", "SignalmanConsumerLagHigh"} {
				if !strings.Contains(content, "alert: "+alert) {
					t.Errorf("embedded frameworks.yml missing %s", alert)
				}
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("vmalert_rule_files has no frameworks.yml: %#v", files)
	}
}

func TestPrometheusStackRoleVarsAlertmanagerMapsReceiverEnv(t *testing.T) {
	vars, err := prometheusStackRoleVars(context.Background(), inventory.Host{}, ServiceConfig{
		Port: 9093,
		EnvVars: map[string]string{
			"ALERTMANAGER_LOOKOUT_URL":        "http://lookout.internal:18022/v1/alertmanager",
			"LOOKOUT_ALERTMANAGER_TOKEN":      "lookout-token",
			"ALERTMANAGER_HEARTBEAT_URL":      "https://heartbeat.example.test/ping",
			"ALERTMANAGER_EMAIL_TO":           "ops@example.test",
			"SMTP_HOST":                       "smtp.example.test",
			"SMTP_PORT":                       "587",
			"FROM_EMAIL":                      "alerts@example.test",
			"SMTP_USER":                       "alerts",
			"SMTP_PASSWORD":                   "secret",
			"ALERTMANAGER_SMTP_SMARTHOST":     "ignored.example.test:25",
			"ALERTMANAGER_SMTP_AUTH_PASSWORD": "ignored",
		},
		Metadata: map[string]any{"component": "alertmanager", "platform_channel": "stable"},
	}, alertingTestHelpers(t, "alertmanager"))
	if err != nil {
		t.Fatalf("prometheusStackRoleVars: %v", err)
	}
	want := map[string]any{
		"alertmanager_artifact_url":       "https://example.test/alertmanager.tar.gz",
		"alertmanager_lookout_url":        "http://lookout.internal:18022/v1/alertmanager",
		"alertmanager_lookout_token":      "lookout-token",
		"alertmanager_heartbeat_url":      "https://heartbeat.example.test/ping",
		"alertmanager_email_to":           "ops@example.test",
		"alertmanager_smtp_smarthost":     "smtp.example.test:587",
		"alertmanager_smtp_from":          "alerts@example.test",
		"alertmanager_smtp_auth_username": "alerts",
		"alertmanager_smtp_auth_password": "secret",
		"alertmanager_port":               9093,
	}
	for key, value := range want {
		if got := vars[key]; got != value {
			t.Errorf("%s = %#v, want %#v", key, got, value)
		}
	}
}

func TestPrometheusStackRoleRendersAlertingSafeguards(t *testing.T) {
	const role = "ansible/collections/ansible_collections/frameworks/infra/roles/prometheus_stack/"
	scrape := readRepoFile(t, role+"templates/vmagent-scrape.yml.j2")
	if !strings.Contains(scrape, "  external_labels:\n{% for key, value in vmagent_external_labels | dictsort %}") {
		t.Fatalf("vmagent scrape config must render external_labels under global:\n%s", scrape)
	}
	vmalert := readRepoFile(t, role+"tasks/vmalert.yml")
	if !strings.Contains(vmalert, `validate: "{{ vmalert_bin }} -dryRun -rule=%s"`) {
		t.Fatalf("vmalert rule files must be validated with vmalert -dryRun before replacing the running set:\n%s", vmalert)
	}
	alertmanager := readRepoFile(t, role+"tasks/alertmanager.yml")
	if !strings.Contains(alertmanager, `validate: "{{ amtool_bin }} check-config %s"`) || !strings.Contains(alertmanager, "no_log: true") {
		t.Fatalf("alertmanager config must be amtool-validated and kept out of logs:\n%s", alertmanager)
	}
	config := readRepoFile(t, role+"templates/alertmanager.yml.j2")
	for _, needle := range []string{
		"receiver: lookout\n  group_by: [alertname, region, cluster]",
		"    - receiver: heartbeat\n      matchers:\n        - alertname=\"Watchdog\"",
		"    - receiver: email-fallback\n      matchers:\n        - severity=\"critical\"\n      repeat_interval: 1h\n      continue: true\n    - receiver: lookout\n",
		"url: {{ alertmanager_lookout_url | to_json }}\n        send_resolved: true\n        http_config:\n          authorization:\n            type: Bearer\n            credentials: {{ alertmanager_lookout_token | to_json }}",
		"url: {{ alertmanager_heartbeat_url | to_json }}\n        send_resolved: false",
		"email_configs:",
	} {
		if !strings.Contains(config, needle) {
			t.Errorf("alertmanager.yml.j2 missing %q", needle)
		}
	}
}

func TestPrometheusStackRoleVarsMapsVMAUTHEdgeJWTKey(t *testing.T) {
	vars, err := prometheusStackRoleVars(context.Background(), inventory.Host{}, ServiceConfig{
		EnvVars: map[string]string{
			"EDGE_TELEMETRY_JWT_PUBLIC_KEY_PEM_B64": "PUBLIC_KEY_B64",
			"VM_HTTP_AUTH_USERNAME":                 "telemetry",
			"VM_HTTP_AUTH_PASSWORD":                 "secret",
			"VMAUTH_UPSTREAM_WRITE_URL":             "http://victoriametrics.internal:8428/api/v1/write",
		},
		Metadata: map[string]any{
			"component":        "vmauth",
			"platform_channel": "stable",
		},
	}, RoleBuildHelpers{
		DetectRemoteOS: func(context.Context, inventory.Host) (string, string, error) {
			return "linux", "amd64", nil
		},
		ResolveArtifact: func(name, arch, channel string, metadata map[string]any) (ResolvedArtifact, error) {
			return ResolvedArtifact{URL: "https://example.com/vmauth.tar.gz", Checksum: "sha256:abc", Version: "v1.138.0"}, nil
		},
	})
	if err != nil {
		t.Fatalf("prometheusStackRoleVars returned error: %v", err)
	}
	if got := vars["vmauth_edge_jwt_public_key_pem_b64"]; got != "PUBLIC_KEY_B64" {
		t.Fatalf("vmauth_edge_jwt_public_key_pem_b64 = %v, want PUBLIC_KEY_B64", got)
	}
	if got := vars["vmauth_upstream_url"]; got != "http://victoriametrics.internal:8428" {
		t.Fatalf("vmauth_upstream_url = %v, want stripped upstream", got)
	}
}

func TestPrometheusStackSystemdServiceNameIsComponentScoped(t *testing.T) {
	tests := []struct {
		name string
		cfg  ServiceConfig
		want string
	}{
		{
			name: "vmauth",
			cfg:  ServiceConfig{Metadata: map[string]any{"component": "vmauth"}},
			want: "vmauth",
		},
		{
			name: "vmagent",
			cfg:  ServiceConfig{Metadata: map[string]any{"service_name": "vmagent"}},
			want: "vmagent",
		},
		{
			name: "unknown falls back",
			cfg:  ServiceConfig{Metadata: map[string]any{"component": "telemetry"}},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := prometheusStackSystemdServiceName(tt.cfg); got != tt.want {
				t.Fatalf("prometheusStackSystemdServiceName() = %q, want %q", got, tt.want)
			}
		})
	}
}
