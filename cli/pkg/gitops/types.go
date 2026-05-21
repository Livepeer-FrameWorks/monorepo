package gitops

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Manifest represents a release manifest from the gitops repo (actual CI/CD format)
type Manifest struct {
	PlatformVersion      string                `yaml:"platform_version"`
	GitCommit            string                `yaml:"git_commit"`
	ReleaseDate          time.Time             `yaml:"release_date"`
	Services             []ServiceEntry        `yaml:"services"`
	NativeBinaries       []NativeBinary        `yaml:"native_binaries"`
	Interfaces           []InterfaceEntry      `yaml:"interfaces"`
	Infrastructure       []InfrastructureEntry `yaml:"infrastructure"`
	ExternalDependencies []ExternalDependency  `yaml:"external_dependencies,omitempty"`
}

// ExternalDependency represents an external dependency (e.g. MistServer)
type ExternalDependency struct {
	Name       string           `yaml:"name"`
	Image      string           `yaml:"image,omitempty"`
	Digest     string           `yaml:"digest,omitempty"`
	ReleaseURL string           `yaml:"release_url,omitempty"`
	ReleaseTag string           `yaml:"release_tag,omitempty"`
	Binaries   []ExternalBinary `yaml:"binaries,omitempty"`
	Raw        map[string]any   `yaml:",inline"` // Preserve dependency fields this CLI does not consume.
}

// ExternalBinary represents a binary artifact for an external dependency.
// Checksum format is "<algo>:<hex>" with algo in {sha256, sha512}.
type ExternalBinary struct {
	Name      string `yaml:"name"`
	URL       string `yaml:"url,omitempty"`
	Checksum  string `yaml:"checksum,omitempty"`
	SizeBytes int64  `yaml:"size_bytes,omitempty"`
}

// ServiceEntry represents a single service in the manifest
type ServiceEntry struct {
	Name           string                   `yaml:"name"`
	ServiceVersion string                   `yaml:"service_version"`
	Image          string                   `yaml:"image"`
	Digest         string                   `yaml:"digest"`
	Images         map[string]RegistryImage `yaml:"images,omitempty"`
	SourceHash     string                   `yaml:"source_hash,omitempty"`
	CarriedFrom    string                   `yaml:"carried_from,omitempty"`
}

// RegistryImage is an alternate registry location for the same release image.
type RegistryImage struct {
	Image  string `yaml:"image"`
	Digest string `yaml:"digest"`
}

// NativeBinary represents native binaries for a service
type NativeBinary struct {
	Name        string     `yaml:"name"`
	SourceHash  string     `yaml:"source_hash,omitempty"`
	CarriedFrom string     `yaml:"carried_from,omitempty"`
	Artifacts   []Artifact `yaml:"artifacts"`
}

// Artifact represents a single binary artifact.
// Checksum format is "<algo>:<hex>" with algo in {sha256, sha512}.
type Artifact struct {
	Arch      string `yaml:"arch"` // linux-amd64, linux-arm64, etc.
	File      string `yaml:"file"` // filename
	URL       string `yaml:"url,omitempty"`
	Checksum  string `yaml:"checksum,omitempty"`
	SizeBytes int64  `yaml:"size_bytes,omitempty"`
}

// InterfaceEntry represents an interface service (chartroom, foredeck)
type InterfaceEntry struct {
	Name         string                   `yaml:"name"`
	Image        string                   `yaml:"image"`
	Digest       string                   `yaml:"digest"`
	StaticBundle string                   `yaml:"static_bundle,omitempty"`
	Images       map[string]RegistryImage `yaml:"images,omitempty"`
	SourceHash   string                   `yaml:"source_hash,omitempty"`
	CarriedFrom  string                   `yaml:"carried_from,omitempty"`
}

// InfrastructureEntry represents an infrastructure component pinned by a
// platform release. Artifacts is populated for components that raw-download
// a tarball/binary instead of pulling from an OS package manager or Docker
// registry. Digest is the OCI manifest digest for content-addressed Docker
// pulls; when present, the resolver returns image@digest.
type InfrastructureEntry struct {
	Name      string     `yaml:"name"`
	Version   string     `yaml:"version"`
	Image     string     `yaml:"image,omitempty"`
	Digest    string     `yaml:"digest,omitempty"`
	Artifacts []Artifact `yaml:"artifacts,omitempty"`
}

// GetInfrastructure looks up an infrastructure entry by name.
func (m *Manifest) GetInfrastructure(name string) *InfrastructureEntry {
	for i := range m.Infrastructure {
		if m.Infrastructure[i].Name == name {
			return &m.Infrastructure[i]
		}
	}
	return nil
}

// GetArtifact returns the artifact for the given arch (linux-amd64, linux-arm64, ...),
// or nil if the infra entry has no artifact for that arch.
func (e *InfrastructureEntry) GetArtifact(arch string) *Artifact {
	for i := range e.Artifacts {
		if e.Artifacts[i].Arch == arch {
			return &e.Artifacts[i]
		}
	}
	return nil
}

// ServiceInfo holds release information for a service (helper struct).
// Binaries is keyed by arch and preserves URL + Checksum from the manifest so
// callers can do integrity-verified downloads.
type ServiceInfo struct {
	Name      string
	Version   string
	Image     string
	Digest    string
	Images    map[string]RegistryImage
	Binaries  map[string]Artifact
	FullImage string
}

// ValidateServiceCohorts rejects manifests that split the core control-plane
// cohort across different service versions. Those services exchange Foghorn
// routing/control RPCs during provisioning and request handling, so a manifest
// that carries only part of the cohort forward is not deployable.
func (m *Manifest) ValidateServiceCohorts() error {
	if m == nil {
		return nil
	}
	if !supportsCoreCohortValidation(m.PlatformVersion) {
		return nil
	}
	for _, cohort := range [][]string{{"bridge", "commodore", "foghorn", "quartermaster"}} {
		versions := make(map[string]string, len(cohort))
		for _, name := range cohort {
			version, ok := m.serviceVersion(name)
			if !ok {
				versions = nil
				break
			}
			versions[name] = version
		}
		if len(versions) == 0 {
			continue
		}

		expected := versions[cohort[0]]
		for _, name := range cohort[1:] {
			if versions[name] == expected {
				continue
			}
			return fmt.Errorf("release manifest %s splits core control-plane service versions: %s=%s, %s=%s", m.PlatformVersion, cohort[0], expected, name, versions[name])
		}
		for _, name := range cohort {
			if err := m.validateNativeBinaryVersion(name, versions[name]); err != nil {
				return err
			}
		}
	}
	return nil
}

func supportsCoreCohortValidation(version string) bool {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	parts := strings.Split(version, ".")
	if len(parts) < 3 {
		return true
	}
	major, majorErr := strconv.Atoi(parts[0])
	minor, minorErr := strconv.Atoi(parts[1])
	patch, patchErr := strconv.Atoi(parts[2])
	if majorErr != nil || minorErr != nil || patchErr != nil {
		return true
	}
	if major != 0 {
		return major > 0
	}
	if minor != 2 {
		return minor > 2
	}
	return patch >= 40
}

func (m *Manifest) serviceVersion(name string) (string, bool) {
	for _, svc := range m.Services {
		if svc.Name != name {
			continue
		}
		version := strings.TrimSpace(svc.ServiceVersion)
		if version == "" {
			version = strings.TrimSpace(m.PlatformVersion)
		}
		return version, true
	}
	return "", false
}

func (m *Manifest) validateNativeBinaryVersion(name, version string) error {
	version = strings.TrimSpace(version)
	if version == "" {
		return nil
	}
	for _, nb := range m.NativeBinaries {
		if nb.Name != name {
			continue
		}
		for _, artifact := range nb.Artifacts {
			if artifact.URL == "" || strings.Contains(artifact.URL, version) {
				continue
			}
			return fmt.Errorf("release manifest %s has %s native artifact %s outside service version %s", m.PlatformVersion, name, artifact.URL, version)
		}
	}
	return nil
}

// GetExternalDependency looks up an external dependency by name (e.g., "mistserver", "caddy").
func (m *Manifest) GetExternalDependency(name string) *ExternalDependency {
	for i := range m.ExternalDependencies {
		if m.ExternalDependencies[i].Name == name {
			return &m.ExternalDependencies[i]
		}
	}
	return nil
}

// GetBinary returns the full binary record for the given os-arch key, or nil
// if the dependency carries no artifact for that arch.
func (d *ExternalDependency) GetBinary(arch string) *ExternalBinary {
	for i := range d.Binaries {
		if d.Binaries[i].Name == arch {
			return &d.Binaries[i]
		}
	}
	return nil
}

// FetchOptions configures manifest fetching
type FetchOptions struct {
	Channel        string        // stable | rc
	Version        string        // v1.2.3 | latest
	CacheDir       string        // Local cache directory
	Offline        bool          // Use cache only, don't fetch
	Repository     string        // GitOps repository URL (or local path)
	LatestTTL      time.Duration // Cache TTL for latest (channel) manifests
	LatestMaxStale time.Duration // Maximum staleness for latest manifests
	PinnedTTL      time.Duration // Cache TTL for pinned versions
	PinnedMaxStale time.Duration // Maximum staleness for pinned versions
	RetryCount     int           // Retry attempts for remote fetch
	RetryDelay     time.Duration // Base retry delay for remote fetch
}
