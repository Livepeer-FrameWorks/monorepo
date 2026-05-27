package provisioner

import (
	"context"
	"testing"

	"frameworks/cli/pkg/inventory"
)

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
