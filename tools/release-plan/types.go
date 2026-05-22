// Package main implements the release-plan tool: it inspects the monorepo
// source tree against a track-aware baseline release manifest and decides,
// per artefact, whether the build matrix should rebuild it or carry forward
// the baseline's image+digest and native binary references unchanged.
//
// The output is a JSON document the release workflow reads at the top of
// every build job (to skip carry_forward jobs) and at the end of the
// manifest job (to copy carried entries from the baseline manifest).
//
// Identity is mode-aware: each component carries its supported Docker
// (image@digest) and/or native (URL+checksum) identities, and carry-forward
// preserves them atomically.
package main

import "time"

// ReleaseComponent describes one Go service in .github/release-components.json.
type ReleaseComponent struct {
	Name           string   `json:"name"`
	Context        string   `json:"context"`
	Cmd            string   `json:"cmd"`
	Dockerfile     string   `json:"dockerfile"`
	CGO            bool     `json:"cgo"`
	DarwinBinary   bool     `json:"darwin_binary"`
	ExtraHashPaths []string `json:"extra_hash_paths,omitempty"`
}

// ReleaseWebapp describes one webapp (SvelteKit / Vite / Astro) in
// .github/release-components.json. Webapps don't carry a Cmd or CGO flag;
// their Dockerfile lives at <context>/Dockerfile by convention.
type ReleaseWebapp struct {
	Name      string `json:"name"`
	Context   string `json:"context"`
	EnvPrefix string `json:"env_prefix,omitempty"`
	BuildDir  string `json:"build_dir,omitempty"`
}

// ReleaseComponents is the wire shape of .github/release-components.json.
type ReleaseComponents struct {
	Services []ReleaseComponent `json:"services"`
	Webapps  []ReleaseWebapp    `json:"webapps,omitempty"`
}

// Manifest is the subset of a gitops release manifest the release-plan tool
// needs to read and emit. It is a deliberate subset of the full schema
// owned by pkg/gitops — we keep it here so the tool can ship as an
// isolated module without taking the monorepo's full dependency graph.
type Manifest struct {
	PlatformVersion      string                `yaml:"platform_version" json:"platform_version"`
	GitCommit            string                `yaml:"git_commit,omitempty" json:"git_commit,omitempty"`
	ReleaseDate          time.Time             `yaml:"release_date,omitempty" json:"release_date,omitempty"`
	Services             []ServiceEntry        `yaml:"services,omitempty" json:"services,omitempty"`
	NativeBinaries       []NativeBinary        `yaml:"native_binaries,omitempty" json:"native_binaries,omitempty"`
	Interfaces           []InterfaceEntry      `yaml:"interfaces,omitempty" json:"interfaces,omitempty"`
	Infrastructure       []InfrastructureEntry `yaml:"infrastructure,omitempty" json:"infrastructure,omitempty"`
	ExternalDependencies []ExternalDependency  `yaml:"external_dependencies,omitempty" json:"external_dependencies,omitempty"`
}

// ServiceEntry — pruned from pkg/gitops; SourceHash is the tool's own field.
type ServiceEntry struct {
	Name           string                   `yaml:"name" json:"name"`
	ServiceVersion string                   `yaml:"service_version" json:"service_version"`
	Image          string                   `yaml:"image" json:"image"`
	Digest         string                   `yaml:"digest" json:"digest"`
	Images         map[string]RegistryImage `yaml:"images,omitempty" json:"images,omitempty"`
	SourceHash     string                   `yaml:"source_hash,omitempty" json:"source_hash,omitempty"`
	CarriedFrom    string                   `yaml:"carried_from,omitempty" json:"carried_from,omitempty"`
}

type RegistryImage struct {
	Image  string `yaml:"image" json:"image"`
	Digest string `yaml:"digest" json:"digest"`
}

type NativeBinary struct {
	Name        string     `yaml:"name" json:"name"`
	SourceHash  string     `yaml:"source_hash,omitempty" json:"source_hash,omitempty"`
	CarriedFrom string     `yaml:"carried_from,omitempty" json:"carried_from,omitempty"`
	Artifacts   []Artifact `yaml:"artifacts" json:"artifacts"`
}

type Artifact struct {
	Arch      string `yaml:"arch" json:"arch"`
	File      string `yaml:"file,omitempty" json:"file,omitempty"`
	URL       string `yaml:"url,omitempty" json:"url,omitempty"`
	Checksum  string `yaml:"checksum,omitempty" json:"checksum,omitempty"`
	SizeBytes int64  `yaml:"size_bytes,omitempty" json:"size_bytes,omitempty"`
}

type InterfaceEntry struct {
	Name         string                   `yaml:"name" json:"name"`
	Image        string                   `yaml:"image" json:"image"`
	Digest       string                   `yaml:"digest" json:"digest"`
	StaticBundle string                   `yaml:"static_bundle,omitempty" json:"static_bundle,omitempty"`
	Images       map[string]RegistryImage `yaml:"images,omitempty" json:"images,omitempty"`
	SourceHash   string                   `yaml:"source_hash,omitempty" json:"source_hash,omitempty"`
	CarriedFrom  string                   `yaml:"carried_from,omitempty" json:"carried_from,omitempty"`
}

type InfrastructureEntry struct {
	Name      string     `yaml:"name" json:"name"`
	Version   string     `yaml:"version" json:"version"`
	Image     string     `yaml:"image,omitempty" json:"image,omitempty"`
	Digest    string     `yaml:"digest,omitempty" json:"digest,omitempty"`
	Artifacts []Artifact `yaml:"artifacts,omitempty" json:"artifacts,omitempty"`
}

type ExternalDependency struct {
	Name       string           `yaml:"name" json:"name"`
	Image      string           `yaml:"image,omitempty" json:"image,omitempty"`
	Digest     string           `yaml:"digest,omitempty" json:"digest,omitempty"`
	ReleaseURL string           `yaml:"release_url,omitempty" json:"release_url,omitempty"`
	ReleaseTag string           `yaml:"release_tag,omitempty" json:"release_tag,omitempty"`
	Binaries   []ExternalBinary `yaml:"binaries,omitempty" json:"binaries,omitempty"`
}

type ExternalBinary struct {
	Name      string `yaml:"name" json:"name"`
	URL       string `yaml:"url,omitempty" json:"url,omitempty"`
	Checksum  string `yaml:"checksum,omitempty" json:"checksum,omitempty"`
	SizeBytes int64  `yaml:"size_bytes,omitempty" json:"size_bytes,omitempty"`
}

// Action is "build" or "carry_forward".
type Action string

const (
	ActionBuild        Action = "build"
	ActionCarryForward Action = "carry_forward"
)

// ComponentKind classifies the component for downstream tooling that needs
// to dispatch differently. The kind matches the manifest section the entry
// will live in once it lands.
type ComponentKind string

const (
	KindService        ComponentKind = "service"
	KindInterface      ComponentKind = "interface"
	KindInfrastructure ComponentKind = "infrastructure"
	KindExternal       ComponentKind = "external"
	KindCLI            ComponentKind = "cli"
)

// Decision describes one component's planned action and the data needed to
// either rebuild it or carry it forward.
type Decision struct {
	Name               string        `json:"name"`
	Kind               ComponentKind `json:"kind"`
	Action             Action        `json:"action"`
	SourceHash         string        `json:"source_hash"`
	BaselineTag        string        `json:"baseline_tag,omitempty"`
	BaselineSourceHash string        `json:"baseline_source_hash,omitempty"`
	// CarriedService is populated when Action=carry_forward and the entry
	// originates in manifest.services[]. Workflows use the embedded Image,
	// Digest, ServiceVersion fields to write the new manifest verbatim.
	CarriedService *ServiceEntry `json:"carried_service,omitempty"`
	// CarriedNativeBinary is populated when the service has native binaries
	// to carry forward. Binary-only services can carry this without
	// CarriedService.
	CarriedNativeBinary *NativeBinary `json:"carried_native_binary,omitempty"`
	// CarriedInterface is populated when Action=carry_forward and the entry
	// originates in manifest.interfaces[].
	CarriedInterface *InterfaceEntry `json:"carried_interface,omitempty"`
}

// PlanOutput is the JSON document emitted by the tool.
type PlanOutput struct {
	PlatformVersion string                `json:"platform_version"`
	Track           string                `json:"track"`
	BaselineTag     string                `json:"baseline_tag,omitempty"`
	BaselinePath    string                `json:"baseline_path,omitempty"`
	GeneratedAt     time.Time             `json:"generated_at"`
	Decisions       map[string]Decision   `json:"decisions"`
	Summary         PlanSummary           `json:"summary"`
	Notes           []string              `json:"notes,omitempty"`
	BaselineLineage []BaselineLineageStep `json:"baseline_lineage,omitempty"`
}

type PlanSummary struct {
	BuildCount        int `json:"build_count"`
	CarryForwardCount int `json:"carry_forward_count"`
}

// BaselineLineageStep records one step in the baseline-resolution walk so
// the user can see why a particular release was chosen.
type BaselineLineageStep struct {
	Track string `json:"track"`
	Tag   string `json:"tag"`
	Why   string `json:"why"`
}
