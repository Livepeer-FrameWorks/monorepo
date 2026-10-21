package provisioner

import (
	"encoding/json"
	"fmt"

	"frameworks/cli/pkg/gitops"
)

// edgeRenderFixtureVersion stands in for every release selector and version
// in the render fixture.
const edgeRenderFixtureVersion = "v0.0.0-render-fixture"

// edgeRenderFixtureCases are the install shapes the render fixture derives
// role variables for: native Linux, native macOS (system domain, CoreML
// profile by default), and the container stack on both host flavors.
var edgeRenderFixtureCases = []struct {
	Name, Mode, OS, Arch string
}{
	{"native-linux-amd64", "native", "linux", "amd64"},
	{"native-darwin-arm64", "native", "darwin", "arm64"},
	{"container-linux-amd64", "container", "linux", "amd64"},
	{"container-darwin-arm64", "container", "darwin", "arm64"},
}

// edgeRenderVarsFixture serializes the role variables deriveVars produces for
// each fixture case. Node identity, endpoints, credentials, release versions
// and artifact pins are fixed placeholders, and the CLI release is replaced
// by one, so the result changes only when the derivation (or CLI-shipped data
// it embeds, such as the config schema and image env) does. encoding/json
// writes map keys sorted, which keeps the serialization canonical.
func edgeRenderVarsFixture(deriveVars edgeRoleVarsFunc) ([]byte, error) {
	src := edgeRoleSources{
		manifest: func(string) (*gitops.Manifest, error) { return edgeRenderFixtureManifest(), nil },
		infraArtifact: func(name, arch, _ string) (*gitops.Artifact, error) {
			return &gitops.Artifact{
				Arch:     arch,
				File:     name + "-" + arch + ".tar.gz",
				URL:      "https://artifacts.example.invalid/" + name + "-" + arch + ".tar.gz",
				Checksum: "sha256:" + name + "-" + arch,
			}, nil
		},
	}
	out := map[string]map[string]any{}
	for _, c := range edgeRenderFixtureCases {
		cfg := edgeRenderFixtureConfig(c.Mode)
		vars, err := deriveVars(cfg, c.OS, c.Arch, src)
		if err != nil {
			return nil, fmt.Errorf("edge render digest: derive %s role vars: %w", c.Name, err)
		}
		vars["edge_cli_version"] = edgeRenderFixtureVersion
		out[c.Name] = vars
	}
	data, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("edge render digest: serialize role vars: %w", err)
	}
	return data, nil
}

func edgeRenderFixtureConfig(mode string) *EdgeProvisionConfig {
	return &EdgeProvisionConfig{
		Mode:                     mode,
		NodeName:                 "edge-fixture",
		NodeDomain:               "edge.example.invalid",
		PoolDomain:               "pool.example.invalid",
		ClusterID:                "cluster-fixture",
		Region:                   "region-fixture",
		Email:                    "acme@example.invalid",
		EnrollmentToken:          "enrollment-token-fixture",
		FoghornGRPCAddr:          "foghorn.example.invalid:18019",
		FoghornGRPCTLSServerName: "foghorn.example.invalid",
		NodeID:                   "node-fixture",
		CertPEM:                  "cert-pem-fixture",
		KeyPEM:                   "key-pem-fixture",
		CABundlePEM:              "ca-bundle-pem-fixture",
		TelemetryURL:             "https://telemetry.example.invalid/api/v1/write",
		TelemetryToken:           "telemetry-token-fixture",
		TelemetryAddress:         "192.0.2.10",
		Capabilities:             []string{"ingest", "edge", "storage", "processing"},
		BandwidthMbps:            1000,
		MaxTranscodes:            4,
		StorageBytes:             1 << 40,
		Version:                  edgeRenderFixtureVersion,
		DarwinDomain:             DomainSystem,
		mistPassword:             "mist-password-fixture",
	}
}

func edgeRenderFixtureManifest() *gitops.Manifest {
	artifact := func(name, arch string) gitops.Artifact {
		return gitops.Artifact{
			Arch:     arch,
			File:     name + "-" + arch + ".tar.gz",
			URL:      "https://artifacts.example.invalid/" + name + "-" + arch + ".tar.gz",
			Checksum: "sha256:" + name + "-" + arch,
		}
	}
	mist := func(profile, platform string) gitops.ExternalPlatform {
		return gitops.ExternalPlatform{
			Host: gitops.ExternalHost{PackageManager: "pkg-fixture", SystemPackages: []string{"package-fixture"}},
			Artifact: &gitops.ExternalBinary{
				Name:     "mistserver-" + profile + "-" + platform + ".tar.gz",
				URL:      "https://artifacts.example.invalid/mistserver-" + profile + ".tar.gz",
				Checksum: "sha256:mistserver-" + profile,
			},
		}
	}
	return &gitops.Manifest{
		PlatformVersion: edgeRenderFixtureVersion,
		Services: []gitops.ServiceEntry{
			{Name: "edge", ServiceVersion: edgeRenderFixtureVersion, Image: "registry.example.invalid/edge", Digest: "sha256:edge-fixture"},
			{Name: "helmsman", ServiceVersion: edgeRenderFixtureVersion, Image: "registry.example.invalid/helmsman", Digest: "sha256:helmsman-fixture"},
		},
		NativeBinaries: []gitops.NativeBinary{
			{Name: "helmsman", Artifacts: []gitops.Artifact{artifact("helmsman", "linux-amd64"), artifact("helmsman", "darwin-arm64")}},
		},
		ExternalDependencies: []gitops.ExternalDependency{{
			Name:       "mistserver",
			ReleaseTag: edgeRenderFixtureVersion,
			ReleaseIndex: &gitops.ExternalReleaseIndex{
				Schema:     "mistserver.release/v1",
				ReleaseTag: edgeRenderFixtureVersion,
				Profiles: map[string]gitops.ExternalProfile{
					"cpu":    {Platforms: map[string]gitops.ExternalPlatform{"linux/amd64": mist("cpu", "linux-amd64"), "darwin/arm64": mist("cpu", "darwin-arm64")}},
					"coreml": {Platforms: map[string]gitops.ExternalPlatform{"darwin/arm64": mist("coreml", "darwin-arm64")}},
				},
			},
			Binaries: []gitops.ExternalBinary{
				{Name: "mistserver-linux-amd64-debug-fixture.tar.gz", URL: "https://artifacts.example.invalid/mistserver-debug.tar.gz", Checksum: "sha256:mistserver-debug"},
			},
		}},
		Infrastructure: []gitops.InfrastructureEntry{
			{Name: "caddy", Version: edgeRenderFixtureVersion, Artifacts: []gitops.Artifact{artifact("caddy", "linux-amd64"), artifact("caddy", "darwin-arm64")}},
		},
	}
}
