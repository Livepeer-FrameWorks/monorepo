package provisioner

import (
	"errors"
	"fmt"
	"strings"

	"frameworks/cli/pkg/gitops"
)

// releaseNativeInfrastructure lists the infrastructure entries whose native
// tarballs the role provisioners download from the release manifest on every
// Linux host. A tagged manifest missing one of them fails provisioning of that
// role with "no artifact URL", so the release pipeline rejects it up front.
var releaseNativeInfrastructure = []string{
	"yugabyte",
	"kafka",
	"victoriametrics",
	"vmauth",
	"vmagent",
	"vmalert",
	"alertmanager",
	jmxExporterArtifact,
	NodeExporterArtifact,
	redisExporterArtifact,
	postgresExporterArtifact,
}

var releaseNativeInfrastructureArches = []string{"linux-amd64", "linux-arm64"}

// ValidateReleaseInfrastructureArtifacts reports every native infrastructure
// artifact the provisioners resolve that the manifest does not carry with a
// URL, a checksum and a URL matching the pinned version.
func ValidateReleaseInfrastructureArtifacts(m *gitops.Manifest) error {
	if m == nil {
		return errors.New("release manifest is nil")
	}
	var problems []string
	for _, name := range releaseNativeInfrastructure {
		infra := m.GetInfrastructure(name)
		if infra == nil {
			problems = append(problems, fmt.Sprintf("infrastructure entry %q is missing", name))
			continue
		}
		for _, arch := range releaseNativeInfrastructureArches {
			artifact, err := artifactForArch(infra, arch, "release", m.PlatformVersion)
			if err != nil {
				problems = append(problems, err.Error())
				continue
			}
			if strings.TrimSpace(artifact.Checksum) == "" {
				problems = append(problems, fmt.Sprintf("infrastructure %s %s artifact has no checksum", name, arch))
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("release manifest native infrastructure: %s", strings.Join(problems, "; "))
	}
	return nil
}
