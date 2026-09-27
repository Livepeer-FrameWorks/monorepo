package templates

import (
	"strings"
	"testing"
)

func renderedEdgeFiles(t *testing.T, vars EdgeVars) map[string]string {
	t.Helper()
	files, err := RenderEdgeTemplates(vars)
	if err != nil {
		t.Fatalf("RenderEdgeTemplates: %v", err)
	}
	out := map[string]string{}
	for _, f := range files {
		out[f.Path] = string(f.Content)
	}
	return out
}

// A deployed stack rendered by an older CLI comes back with this CLI's
// compose file and renderer-owned env values, while its identity, pinned
// image, flavor, telemetry and operator edits survive.
func TestRerenderEdgeStackRefreshesRendererOwnedConfig(t *testing.T) {
	vars := EdgeVars{
		NodeID:          "edge-self-1",
		EdgeDomain:      "edge-self-1.example.com",
		AcmeEmail:       "ops@example.com",
		FoghornGRPCAddr: "foghorn.example.com:18019",
		GRPCTLSCAPath:   "/etc/frameworks/pki/ca.crt",
		Mode:            "container",
		EdgeOS:          "darwin",
		EdgeImage:       "livepeerframeworks/frameworks-edge@sha256:abc",
		TelemetryURL:    "https://telemetry.example.com/api/v1/write",
		TelemetryToken:  "tok",
		ClusterID:       "media-eu-1",
		Region:          "eu-west",
	}
	fresh := renderedEdgeFiles(t, vars)

	staleCompose := strings.Replace(fresh["docker-compose.edge.yml"], `shm_size: "1g"`, `shm_size: "256m"`, 1)
	staleEnv := strings.Replace(fresh[".edge.env"], "HELMSMAN_MANAGEMENT_PORT=18017", "HELMSMAN_MANAGEMENT_PORT=19999", 1)
	staleEnv = strings.Replace(staleEnv, "RESTREAM_ALLOWED_PRIVATE_CIDRS=", "RESTREAM_ALLOWED_PRIVATE_CIDRS=10.1.0.0/16", 1)
	staleEnv += "OPERATOR_EXTRA=1\n"

	compose, env, err := RerenderEdgeStack(EdgeDeployedStack{
		Env:            staleEnv,
		Compose:        staleCompose,
		VMAgentConfig:  fresh["vmagent-edge.yml"],
		TelemetryToken: "tok\n",
	})
	if err != nil {
		t.Fatalf("RerenderEdgeStack: %v", err)
	}
	if compose != fresh["docker-compose.edge.yml"] {
		t.Fatalf("re-rendered compose differs from this CLI's render:\n%s", compose)
	}
	for _, want := range []string{
		"HELMSMAN_MANAGEMENT_PORT=18017",
		"RESTREAM_ALLOWED_PRIVATE_CIDRS=10.1.0.0/16",
		"NODE_ID=edge-self-1",
		"FOGHORN_CONTROL_ADDR=foghorn.example.com:18019",
		"GRPC_TLS_CA_PATH=/etc/frameworks/pki/ca.crt",
		"TELEMETRY_URL=https://telemetry.example.com/api/v1/write",
		"OPERATOR_EXTRA=1",
	} {
		if !strings.Contains(env, want+"\n") {
			t.Fatalf("re-rendered .edge.env missing %q:\n%s", want, env)
		}
	}
	if strings.Contains(env, "19999") {
		t.Fatalf("stale renderer-owned value survived:\n%s", env)
	}
	if !strings.Contains(compose, "livepeerframeworks/frameworks-edge@sha256:abc") || !strings.Contains(compose, "\n    ports:\n") {
		t.Fatalf("pinned image or darwin flavor lost:\n%s", compose)
	}
}

func TestRerenderEdgeStackRejectsNativeAndUninitialized(t *testing.T) {
	if _, _, err := RerenderEdgeStack(EdgeDeployedStack{Env: "NODE_ID=n\nDEPLOY_MODE=native\n"}); err == nil {
		t.Fatal("native stack must be rejected")
	}
	if _, _, err := RerenderEdgeStack(EdgeDeployedStack{Env: "DEPLOY_MODE=container\n"}); err == nil {
		t.Fatal("stack without NODE_ID must be rejected")
	}
}
