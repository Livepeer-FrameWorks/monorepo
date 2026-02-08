package gitops

import "time"

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
	Name       string                 `yaml:"name"`
	Image      string                 `yaml:"image,omitempty"`
	Digest     string                 `yaml:"digest,omitempty"`
	ReleaseURL string                 `yaml:"release_url,omitempty"`
	ReleaseTag string                 `yaml:"release_tag,omitempty"`
	Binaries   []ExternalBinary       `yaml:"binaries,omitempty"`
	Raw        map[string]interface{} `yaml:",inline"` // Catch any additional fields
}

// ExternalBinary represents a binary artifact for an external dependency
type ExternalBinary struct {
	Name      string `yaml:"name"`
	URL       string `yaml:"url,omitempty"`
	SizeBytes int64  `yaml:"size_bytes,omitempty"`
}

// ServiceEntry represents a single service in the manifest
type ServiceEntry struct {
	Name           string `yaml:"name"`
	ServiceVersion string `yaml:"service_version"`
	Image          string `yaml:"image"`
	Digest         string `yaml:"digest"`
}

// NativeBinary represents native binaries for a service
type NativeBinary struct {
	Name      string     `yaml:"name"`
	Artifacts []Artifact `yaml:"artifacts"`
}

// Artifact represents a single binary artifact
type Artifact struct {
	Arch      string `yaml:"arch"` // linux-amd64, linux-arm64, etc.
	File      string `yaml:"file"` // filename
	URL       string `yaml:"url,omitempty"`
	SizeBytes int64  `yaml:"size_bytes,omitempty"`
}

// InterfaceEntry represents an interface service (webapp, website)
type InterfaceEntry struct {
	Name         string `yaml:"name"`
	Image        string `yaml:"image"`
	Digest       string `yaml:"digest"`
	StaticBundle string `yaml:"static_bundle,omitempty"`
}

// InfrastructureEntry represents infrastructure requirements
type InfrastructureEntry struct {
	Name           string `yaml:"name"`
	TestedVersion  string `yaml:"tested_version"`
	MinimumVersion string `yaml:"minimum_version"`
	Image          string `yaml:"image"`
	Notes          string `yaml:"notes,omitempty"`
}

// ServiceInfo holds release information for a service (helper struct)
type ServiceInfo struct {
	Name      string            // Service name
	Version   string            // Service version
	Image     string            // Docker image with registry
	Digest    string            // sha256 digest
	Binaries  map[string]string // arch → filename
	FullImage string            // image@digest for docker
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
