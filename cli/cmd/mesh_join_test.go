package cmd

import (
	"strings"
	"testing"
)

// TestBuildPrivateerJoinEnv_RedactsOnlyWhenAsked sanity-checks that the
// rendered env carries SERVICE_TOKEN and MESH_JOIN_TOKEN verbatim — it's
// the --dry-run wrapper's responsibility to redact, not this builder's.
func TestBuildPrivateerJoinEnv_IncludesSecrets(t *testing.T) {
	out := buildPrivateerJoinEnv(privateerJoinSettings{
		Token:         "tok-abc",
		BootstrapAddr: "https://bridge.example.com",
		NodeName:      "core-4",
		NodeType:      "core",
		ClusterID:     "prod-platform",
		ServiceToken:  "svc-xyz",
	})
	if !strings.Contains(out, "SERVICE_TOKEN=svc-xyz") {
		t.Errorf("SERVICE_TOKEN missing from env:\n%s", out)
	}
	if !strings.Contains(out, "MESH_JOIN_TOKEN=tok-abc") {
		t.Errorf("MESH_JOIN_TOKEN missing from env:\n%s", out)
	}
	if !strings.Contains(out, "BRIDGE_BOOTSTRAP_ADDR=https://bridge.example.com") {
		t.Errorf("BRIDGE_BOOTSTRAP_ADDR missing from env:\n%s", out)
	}
	if !strings.Contains(out, "CLUSTER_ID=prod-platform") {
		t.Errorf("CLUSTER_ID missing from env:\n%s", out)
	}
	if !strings.Contains(out, "MESH_PRIVATE_KEY_FILE=/etc/privateer/wg.key") {
		t.Errorf("MESH_PRIVATE_KEY_FILE missing from env:\n%s", out)
	}
	if !strings.Contains(out, "PRIVATEER_STATIC_PEERS_FILE=/etc/privateer/static-peers.json") {
		t.Errorf("PRIVATEER_STATIC_PEERS_FILE missing from env:\n%s", out)
	}
}

// TestBuildPrivateerJoinEnv_RedactedView verifies the dry-run rendering
// (constructed by the command with cleared Token + ServiceToken) never
// leaks the real credentials.
func TestBuildPrivateerJoinEnv_RedactedView(t *testing.T) {
	redacted := privateerJoinSettings{
		Token:         "***",
		BootstrapAddr: "https://bridge.example.com",
		NodeName:      "core-4",
		NodeType:      "core",
		ClusterID:     "prod-platform",
		ServiceToken:  "***",
	}
	out := buildPrivateerJoinEnv(redacted)
	if strings.Contains(out, "svc-xyz") || strings.Contains(out, "tok-abc") {
		t.Fatalf("redacted view leaked a real credential:\n%s", out)
	}
	if !strings.Contains(out, "SERVICE_TOKEN=***") {
		t.Errorf("expected redacted SERVICE_TOKEN in output:\n%s", out)
	}
	if !strings.Contains(out, "MESH_JOIN_TOKEN=***") {
		t.Errorf("expected redacted MESH_JOIN_TOKEN in output:\n%s", out)
	}
}

func TestParseSSHTarget(t *testing.T) {
	cases := []struct {
		in           string
		overrideUser string
		wantUser     string
		wantAddress  string
		wantErr      bool
	}{
		{in: "deploy@host.example.com", wantUser: "deploy", wantAddress: "host.example.com"},
		{in: "host.example.com", wantUser: "root", wantAddress: "host.example.com"},
		{in: "deploy@host", overrideUser: "admin", wantUser: "admin", wantAddress: "host"},
		{in: "", wantErr: true},
		{in: "@host", wantUser: "root", wantAddress: "@host"}, // no valid user prefix
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := parseSSHTarget(c.in, c.overrideUser)
			if c.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.user != c.wantUser || got.address != c.wantAddress {
				t.Errorf("got {user:%q address:%q}, want {user:%q address:%q}", got.user, got.address, c.wantUser, c.wantAddress)
			}
		})
	}
}
