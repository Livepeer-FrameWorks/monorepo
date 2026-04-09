package provisioner

import (
	"encoding/base64"
	"testing"
)

func TestGatewayKeystoreSpecDefaultsAndDecode(t *testing.T) {
	t.Parallel()

	spec, err := gatewayKeystoreSpec(map[string]string{
		"LIVEPEER_ETH_KEYSTORE_B64":      base64.StdEncoding.EncodeToString([]byte(`{"address":"abc"}`)),
		"LIVEPEER_ETH_KEYSTORE_PASSWORD": "super-secret",
	})
	if err != nil {
		t.Fatalf("gatewayKeystoreSpec returned error: %v", err)
	}

	if !spec.Enabled {
		t.Fatalf("expected spec to be enabled")
	}
	if spec.Path != "/etc/frameworks/livepeer-gateway-keystore" {
		t.Fatalf("unexpected keystore path: %q", spec.Path)
	}
	if spec.PasswordFile != "/etc/frameworks/.livepeer_gateway_keystore_password" {
		t.Fatalf("unexpected password file: %q", spec.PasswordFile)
	}
	if spec.Filename != "UTC--shared-livepeer-gateway-key.json" {
		t.Fatalf("unexpected filename: %q", spec.Filename)
	}
	if string(spec.KeyJSON) != `{"address":"abc"}` {
		t.Fatalf("unexpected key json: %q", string(spec.KeyJSON))
	}
}

func TestGatewayKeystoreSpecRequiresPair(t *testing.T) {
	t.Parallel()

	_, err := gatewayKeystoreSpec(map[string]string{
		"LIVEPEER_ETH_KEYSTORE_B64": base64.StdEncoding.EncodeToString([]byte(`{"address":"abc"}`)),
	})
	if err == nil {
		t.Fatal("expected error when password is missing")
	}
}

func TestLivepeerGatewayBuildFlagsIncludesLocalWalletSettings(t *testing.T) {
	t.Parallel()

	p := NewLivepeerGatewayProvisioner(nil)
	flags := p.buildFlags(ServiceConfig{
		EnvVars: map[string]string{
			"network":          "arbitrum-one-mainnet",
			"eth_url":          "https://arb.example",
			"keystore_path":    "/etc/frameworks/livepeer-gateway-keystore",
			"eth_password":     "/etc/frameworks/.livepeer_gateway_keystore_password",
			"eth_acct_addr":    "0xabc123",
			"gateway_host":     "livepeer.example",
			"auth_webhook_url": "https://bridge.example/auth",
		},
	})

	if got := flags["ethKeystorePath"]; got != "/etc/frameworks/livepeer-gateway-keystore" {
		t.Fatalf("expected ethKeystorePath flag, got %q", got)
	}
	if got := flags["ethPassword"]; got != "/etc/frameworks/.livepeer_gateway_keystore_password" {
		t.Fatalf("expected ethPassword flag, got %q", got)
	}
	if got := flags["ethAcctAddr"]; got != "0xabc123" {
		t.Fatalf("expected ethAcctAddr flag, got %q", got)
	}
}

func TestGatewayDockerVolumesMountKeystoreArtifacts(t *testing.T) {
	t.Parallel()

	volumes := gatewayDockerVolumes(ServiceConfig{
		EnvVars: map[string]string{
			"keystore_path": "/etc/frameworks/livepeer-gateway-keystore",
			"eth_password":  "/etc/frameworks/.livepeer_gateway_keystore_password",
		},
	})

	if len(volumes) != 2 {
		t.Fatalf("expected 2 volumes, got %d (%v)", len(volumes), volumes)
	}
	if volumes[0] != "/etc/frameworks/livepeer-gateway-keystore:/etc/frameworks/livepeer-gateway-keystore:ro" {
		t.Fatalf("unexpected keystore volume: %q", volumes[0])
	}
	if volumes[1] != "/etc/frameworks/.livepeer_gateway_keystore_password:/etc/frameworks/.livepeer_gateway_keystore_password:ro" {
		t.Fatalf("unexpected password volume: %q", volumes[1])
	}
}
