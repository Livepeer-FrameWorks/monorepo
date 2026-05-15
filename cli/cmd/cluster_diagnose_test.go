package cmd

import (
	"slices"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
)

func TestMediaDiagnosticHostServicesIncludesAliasedMediaServices(t *testing.T) {
	manifest := &inventory.Manifest{
		Services: map[string]inventory.ServiceConfig{
			"foghorn-eu":          {Enabled: true, Deploy: "foghorn", Hosts: []string{"regional-eu-1", "regional-eu-2"}},
			"chandler-us":         {Enabled: true, Deploy: "chandler", Host: "regional-us-1"},
			"livepeer-gateway-us": {Enabled: true, Deploy: "livepeer-gateway", Hosts: []string{"regional-us-1"}},
			"bridge":              {Enabled: true, Hosts: []string{"regional-eu-1"}},
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
	if _, ok := got["central-eu-1"]; ok {
		t.Fatalf("unexpected non-media diagnostic host central-eu-1: %#v", got["central-eu-1"])
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
}
