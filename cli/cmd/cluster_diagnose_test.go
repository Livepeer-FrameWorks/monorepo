package cmd

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

func TestKafkaDiagnosticCommandsUseNativeRuntimeByDefault(t *testing.T) {
	checks := kafkaDiagnosticCommands("native", 19092)
	if len(checks) != 4 {
		t.Fatalf("checks = %d, want 4", len(checks))
	}
	for _, check := range checks {
		if !strings.HasPrefix(check.Command, "/opt/kafka/bin/") {
			t.Fatalf("native diagnostic uses retired container path: %q", check.Command)
		}
		if !strings.Contains(check.Command, "--bootstrap-server localhost:19092") {
			t.Fatalf("diagnostic ignores configured broker port: %q", check.Command)
		}
	}
	if !strings.Contains(checks[2].Command, "--describe --all-groups") {
		t.Fatalf("consumer lag diagnostic missing: %q", checks[2].Command)
	}
}

func TestKafkaDiagnosticCommandsRetainExplicitDockerCompatibility(t *testing.T) {
	checks := kafkaDiagnosticCommands("docker", 9092)
	for _, check := range checks {
		if !strings.HasPrefix(check.Command, "docker compose -f /opt/frameworks/kafka/docker-compose.yml") {
			t.Fatalf("docker diagnostic command = %q", check.Command)
		}
	}
}

func TestDiagnosticCommandErrorPrefersStderrOverExpandedCommandError(t *testing.T) {
	result := &ssh.CommandResult{ExitCode: 255, Stderr: "ssh: connect: operation not permitted"}
	got := diagnosticCommandError(result, errors.New("ssh host: \"very large generated probe\" exited 255"))
	if got != "exit 255 (ssh: connect: operation not permitted)" {
		t.Fatalf("diagnosticCommandError() = %q", got)
	}
}

func TestMediaDiagnosticHostServicesIncludesAliasedMediaServices(t *testing.T) {
	manifest := &inventory.Manifest{
		Services: map[string]inventory.ServiceConfig{
			"foghorn-eu":          {Enabled: true, Deploy: "foghorn", Hosts: []string{"regional-eu-1", "regional-eu-2"}},
			"chandler-us":         {Enabled: true, Deploy: "chandler", Host: "regional-us-1"},
			"livepeer-gateway-us": {Enabled: true, Deploy: "livepeer-gateway", Hosts: []string{"regional-us-1"}},
			"bridge":              {Enabled: true, Hosts: []string{"regional-eu-1"}},
			"periscope-metering":  {Enabled: true, Host: "central-eu-1"},
			"grafana":             {Enabled: true, Host: "central-eu-1"},
		},
	}

	got := mediaDiagnosticHostServices(manifest)
	for host, service := range map[string]string{
		"regional-eu-1": "foghorn",
		"regional-eu-2": "foghorn",
		"regional-us-1": "chandler",
	} {
		if !slices.Contains(got[host], service) {
			t.Fatalf("expected %s on %s, got %#v", service, host, got[host])
		}
	}
	if !slices.Contains(got["regional-us-1"], "livepeer-gateway") {
		t.Fatalf("expected livepeer-gateway on regional-us-1, got %#v", got["regional-us-1"])
	}
	if !slices.Contains(got["central-eu-1"], "periscope-metering") {
		t.Fatalf("expected periscope-metering on central-eu-1, got %#v", got["central-eu-1"])
	}
	if slices.Contains(got["central-eu-1"], "grafana") {
		t.Fatalf("unexpected grafana diagnostic on central-eu-1: %#v", got["central-eu-1"])
	}
}

func TestMediaDiagnosticPortsIncludesPeriscopeMetering(t *testing.T) {
	if !slices.Contains(mediaDiagnosticPorts(), 18021) {
		t.Fatalf("diagnostic ports missing periscope-metering: %v", mediaDiagnosticPorts())
	}
}

func TestCommodoreStreamDiagnosticSQLRequiresSafeStreamID(t *testing.T) {
	if got := commodoreStreamDiagnosticSQL(safeDiagnosticValue("bad'; DROP TABLE streams; --"), "tenant"); got != "" {
		t.Fatalf("expected unsafe stream id to disable SQL probe, got %q", got)
	}
	got := commodoreStreamDiagnosticSQL(
		safeDiagnosticValue("9310e633-1612-4cfa-9752-597bca644405"),
		safeDiagnosticValue("ae0e8171-58bc-4c02-9a5c-0412137d8707"),
	)
	if got == "" {
		t.Fatal("expected SQL probe for safe stream and tenant IDs")
	}
	if !strings.Contains(got, "9310e633-1612-4cfa-9752-597bca644405") || !strings.Contains(got, "ae0e8171-58bc-4c02-9a5c-0412137d8707") {
		t.Fatalf("SQL probe missing filter values: %s", got)
	}
}

func TestMediaDiagnosticScriptRunsDatabaseProbesForExactServices(t *testing.T) {
	script := mediaDiagnosticScript(
		[]string{"commodore", "quartermaster"},
		[]int{18002, 18004},
		diagnoseOptions{StreamID: "9310e633-1612-4cfa-9752-597bca644405", TenantID: "ae0e8171-58bc-4c02-9a5c-0412137d8707"},
	)

	for _, want := range []string{
		"printf '%s\\n' 'commodore' 'quartermaster' | grep -qx quartermaster",
		"printf '%s\\n' 'commodore' 'quartermaster' | grep -qx commodore",
		"== quartermaster media placement snapshot ==",
		"== commodore stream row ==",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("diagnostic script missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "|nan|inf|") {
		t.Fatal("bare inf log filter also matches every info-level record")
	}
	if strings.Contains(script, "|signalman|decklog|") {
		t.Fatal("service names in the filter match every journal prefix for that unit")
	}
	if !strings.Contains(script, "(nan|inf)([^[:alnum:]_]|$)") {
		t.Fatal("diagnostic script must retain boundary-aware NaN/Inf detection")
	}
}
