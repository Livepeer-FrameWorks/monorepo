package controlplane

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/pkg/inventory"
)

func TestResolveGRPCLocalUsesSavedEndpoint(t *testing.T) {
	t.Parallel()
	ctx := fwcfg.Context{
		Name:       "local",
		AccessMode: fwcfg.AccessModeLocal,
		Endpoints:  fwcfg.DefaultEndpoints(),
	}

	ep, err := ResolveGRPC(context.Background(), ctx, "quartermaster")
	if err != nil {
		t.Fatalf("ResolveGRPC: %v", err)
	}
	if ep.Address != "localhost:19002" {
		t.Fatalf("Address = %q, want localhost:19002", ep.Address)
	}
}

func TestResolveGRPCMeshUsesManifestWireGuardAddress(t *testing.T) {
	withEmptyConfig(t)
	manifestPath := writeManifest(t, `
version: v1
type: cluster
profile: development
root_domain: example.test
hosts:
  core-1:
    external_ip: 203.0.113.10
    user: root
    wireguard_ip: 10.88.0.10
services:
  quartermaster:
    enabled: true
    mode: native
    host: core-1
    grpc_port: 19902
`)
	ctx := fwcfg.Context{
		Name:       "platform",
		Persona:    fwcfg.PersonaPlatform,
		AccessMode: fwcfg.AccessModeMesh,
		Gitops: &fwcfg.Gitops{
			Source:       fwcfg.GitopsManifest,
			ManifestPath: manifestPath,
		},
	}

	ep, err := ResolveGRPC(context.Background(), ctx, "quartermaster")
	if err != nil {
		t.Fatalf("ResolveGRPC: %v", err)
	}
	if ep.Address != "10.88.0.10:19902" {
		t.Fatalf("Address = %q, want 10.88.0.10:19902", ep.Address)
	}
}

func TestResolveGRPCMeshRequiresWireGuardAddress(t *testing.T) {
	withEmptyConfig(t)
	manifestPath := writeManifest(t, `
version: v1
type: cluster
profile: development
root_domain: example.test
hosts:
  core-1:
    external_ip: 203.0.113.10
    user: root
services:
  quartermaster:
    enabled: true
    mode: native
    host: core-1
    grpc_port: 19902
`)
	ctx := fwcfg.Context{
		Name:       "platform",
		Persona:    fwcfg.PersonaPlatform,
		AccessMode: fwcfg.AccessModeMesh,
		Gitops: &fwcfg.Gitops{
			Source:       fwcfg.GitopsManifest,
			ManifestPath: manifestPath,
		},
	}

	_, err := ResolveGRPC(context.Background(), ctx, "quartermaster")
	if err == nil {
		t.Fatal("ResolveGRPC succeeded without wireguard_ip; want mesh address error")
	}
}

func TestResolveGRPCSSHRejectsNonPlatformPersona(t *testing.T) {
	t.Parallel()
	ctx := fwcfg.Context{
		Name:       "selfhosted",
		Persona:    fwcfg.PersonaSelfHosted,
		AccessMode: fwcfg.AccessModeSSH,
	}

	_, err := ResolveGRPC(context.Background(), ctx, "quartermaster")
	if err == nil {
		t.Fatal("ResolveGRPC succeeded; want persona error")
	}
}

func TestResolveGRPCSSHOverrideRequiresExplicitTransport(t *testing.T) {
	t.Parallel()

	for _, addr := range []string{
		"quartermaster.internal:19002",
		"10.88.0.10:19002",
		"quartermaster:19002",
		"203.0.113.10:19002",
	} {
		addr := addr
		t.Run(addr, func(t *testing.T) {
			t.Parallel()
			ctx := fwcfg.Context{
				Name:       "platform",
				Persona:    fwcfg.PersonaPlatform,
				AccessMode: fwcfg.AccessModeSSH,
				Endpoints: fwcfg.Endpoints{
					QuartermasterGRPCAddr: addr,
				},
			}

			_, err := ResolveGRPC(context.Background(), ctx, "quartermaster")
			if err == nil {
				t.Fatal("ResolveGRPC succeeded without explicit transport; want error")
			}
			if !strings.Contains(err.Error(), "--tls") || !strings.Contains(err.Error(), "--allow-insecure") {
				t.Fatalf("error = %q, want transport guidance", err)
			}
		})
	}
}

func TestResolveGRPCSSHOverrideHonorsUseTLSForServiceName(t *testing.T) {
	t.Parallel()
	ctx := fwcfg.Context{
		Name:       "platform",
		Persona:    fwcfg.PersonaPlatform,
		AccessMode: fwcfg.AccessModeSSH,
		Endpoints: fwcfg.Endpoints{
			QuartermasterGRPCAddr: "quartermaster:19002",
			UseTLS:                true,
		},
	}

	ep, err := ResolveGRPC(context.Background(), ctx, "quartermaster")
	if err != nil {
		t.Fatalf("ResolveGRPC: %v", err)
	}
	if ep.AllowInsecure {
		t.Fatal("AllowInsecure = true, want false when use_tls is explicit")
	}
}

func TestSavedEndpointTLSPropagatesCustomCA(t *testing.T) {
	t.Parallel()
	ctx := fwcfg.Context{
		Endpoints: fwcfg.Endpoints{
			UseTLS:    true,
			TLSCACert: "/tmp/frameworks-ca.crt",
		},
	}
	ep, err := savedEndpoint(ctx, "quartermaster.internal:19002")
	if err != nil {
		t.Fatalf("savedEndpoint: %v", err)
	}
	if ep.AllowInsecure {
		t.Fatal("AllowInsecure = true, want verified TLS")
	}
	if ep.CACertFile != "/tmp/frameworks-ca.crt" {
		t.Fatalf("CACertFile = %q", ep.CACertFile)
	}
}

func TestManifestTransportPolicy(t *testing.T) {
	t.Parallel()
	production := &inventory.Manifest{Profile: "production"}
	development := &inventory.Manifest{Profile: "development"}
	if manifestAllowsInsecureGRPC(fwcfg.Context{}, production) {
		t.Fatal("production manifest selected plaintext gRPC")
	}
	if !manifestAllowsInsecureGRPC(fwcfg.Context{}, development) {
		t.Fatal("development manifest did not select plaintext gRPC")
	}
	if manifestAllowsInsecureGRPC(fwcfg.Context{Endpoints: fwcfg.Endpoints{UseTLS: true}}, development) {
		t.Fatal("explicit TLS did not override development plaintext default")
	}
	if !manifestAllowsInsecureGRPC(fwcfg.Context{Endpoints: fwcfg.Endpoints{AllowInsecure: true}}, production) {
		t.Fatal("explicit plaintext did not override production TLS default")
	}
}

func TestManifestTLSRootsLoadsInternalCAFromGitOpsEnv(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	encode := func(value string) string {
		return base64.StdEncoding.EncodeToString([]byte(value))
	}
	env := strings.Join([]string{
		"NAVIGATOR_INTERNAL_CA_ROOT_CERT_PEM_B64=" + encode("root"),
		"NAVIGATOR_INTERNAL_CA_INTERMEDIATE_CERT_PEM_B64=" + encode("intermediate"),
		"NAVIGATOR_INTERNAL_CA_INTERMEDIATE_KEY_PEM_B64=" + encode("key"),
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "shared.env"), []byte(env), 0o600); err != nil {
		t.Fatalf("write shared env: %v", err)
	}
	r := &Resolver{
		ctxCfg: fwcfg.Context{},
		manifest: &inventory.Manifest{
			Profile:  "production",
			EnvFiles: []string{"shared.env"},
		},
		manifestPath: filepath.Join(dir, "cluster.yaml"),
	}
	caFile, caPEM, err := r.manifestTLSRoots()
	if err != nil {
		t.Fatalf("manifestTLSRoots: %v", err)
	}
	if caFile != "" {
		t.Fatalf("CACertFile = %q, want inline GitOps CA", caFile)
	}
	if caPEM != "root\nintermediate\n" {
		t.Fatalf("CACertPEM = %q", caPEM)
	}
}

func TestResolveGRPCSSHOverrideHonorsUseTLSForFQDN(t *testing.T) {
	t.Parallel()
	ctx := fwcfg.Context{
		Name:       "platform",
		Persona:    fwcfg.PersonaPlatform,
		AccessMode: fwcfg.AccessModeSSH,
		Endpoints: fwcfg.Endpoints{
			QuartermasterGRPCAddr: "quartermaster.internal:19002",
			UseTLS:                true,
		},
	}

	ep, err := ResolveGRPC(context.Background(), ctx, "quartermaster")
	if err != nil {
		t.Fatalf("ResolveGRPC: %v", err)
	}
	if ep.AllowInsecure {
		t.Fatal("AllowInsecure = true, want false when use_tls is explicit")
	}
}

func TestResolveGRPCSSHOverrideHonorsAllowInsecureForFQDN(t *testing.T) {
	t.Parallel()
	ctx := fwcfg.Context{
		Name:       "platform",
		Persona:    fwcfg.PersonaPlatform,
		AccessMode: fwcfg.AccessModeSSH,
		Endpoints: fwcfg.Endpoints{
			QuartermasterGRPCAddr: "quartermaster.internal:19002",
			AllowInsecure:         true,
		},
	}

	ep, err := ResolveGRPC(context.Background(), ctx, "quartermaster")
	if err != nil {
		t.Fatalf("ResolveGRPC: %v", err)
	}
	if ep.Address != "quartermaster.internal:19002" {
		t.Fatalf("Address = %q, want override", ep.Address)
	}
	if !ep.AllowInsecure {
		t.Fatal("AllowInsecure = false, want true when allow_insecure is explicit")
	}
}

func withEmptyConfig(t *testing.T) {
	t.Helper()
	prev := fwcfg.GetRuntimeOverrides()
	path := filepath.Join(t.TempDir(), "config.yaml")
	fwcfg.SetRuntimeOverrides(fwcfg.RuntimeOverrides{ConfigPath: path, ConfigPathExplicit: true})
	t.Cleanup(func() { fwcfg.SetRuntimeOverrides(prev) })
}

func writeManifest(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cluster.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return path
}
