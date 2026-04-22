package provisioner

import (
	"fmt"
	"strings"

	"frameworks/cli/pkg/gitops"
)

// fetchInfraEntry fetches the release manifest for platformChannel and returns
// the infrastructure entry named name, erroring if either the manifest fetch
// or the entry lookup fails.
func fetchInfraEntry(name, platformChannel string, metadata map[string]any) (*gitops.InfrastructureEntry, string, string, error) {
	if platformChannel == "" {
		return nil, "", "", fmt.Errorf("%s provisioner requires a platform channel", name)
	}
	channel, resolved := gitops.ResolveVersion(platformChannel)
	manifest, err := fetchGitopsManifest(channel, resolved, metadata)
	if err != nil {
		return nil, channel, resolved, fmt.Errorf("fetch gitops manifest for %s artifact: %w", name, err)
	}
	infra := manifest.GetInfrastructure(name)
	if infra == nil {
		return nil, channel, resolved, fmt.Errorf("release manifest (%s/%s) has no infrastructure entry named %q", channel, resolved, name)
	}
	return infra, channel, resolved, nil
}

// artifactForArch pulls the (arch) artifact off infra and validates that its
// URL references infra.Version. The version/URL link is a cheap guard against
// operator drift (someone bumps Version but forgets to update the URL).
func artifactForArch(infra *gitops.InfrastructureEntry, arch, channel, resolved string) (*gitops.Artifact, error) {
	artifact := infra.GetArtifact(arch)
	if artifact == nil || artifact.URL == "" {
		return nil, fmt.Errorf("release manifest %s/%s %s entry has no artifact URL for arch %q", channel, resolved, infra.Name, arch)
	}
	if v := strings.TrimSpace(infra.Version); v != "" && !strings.Contains(artifact.URL, v) {
		return nil, fmt.Errorf("release manifest %s/%s %s %s URL %q does not reference version %q — version/URL drift", channel, resolved, infra.Name, arch, artifact.URL, v)
	}
	return artifact, nil
}

// resolveInfraArtifactFromChannel returns the artifact for (name, arch) in the
// release manifest pinned to platformChannel. Used by flows that have the
// channel in hand directly (edge provisioning).
func resolveInfraArtifactFromChannel(name, arch, platformChannel string, metadata map[string]any) (*gitops.Artifact, error) {
	infra, channel, resolved, err := fetchInfraEntry(name, platformChannel, metadata)
	if err != nil {
		return nil, err
	}
	return artifactForArch(infra, arch, channel, resolved)
}
