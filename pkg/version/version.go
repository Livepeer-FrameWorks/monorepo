package version

// These variables will be injected at build time via ldflags
var (
	Version          = "dev"     // semantic version (e.g., v1.2.3)
	GitCommit        = "unknown" // git commit hash
	BuildDate        = "unknown" // build timestamp
	ComponentName    = "unknown" // microservice/component name, e.g., gateway, quartermaster, cli
	ComponentVersion = "0.0.0"   // per-component SemVer (e.g., 1.7.3)
)

// Info represents version information for a service
type Info struct {
	Version          string `json:"version"`
	GitCommit        string `json:"git_commit"`
	BuildDate        string `json:"build_date"`
	ComponentName    string `json:"component_name,omitempty"`
	ComponentVersion string `json:"component_version,omitempty"`
}

// GetInfo returns version information as a struct
func GetInfo() Info {
	return Info{
		Version:          Version,
		GitCommit:        GitCommit,
		BuildDate:        BuildDate,
		ComponentName:    ComponentName,
		ComponentVersion: ComponentVersion,
	}
}

// GetInfoMap returns version information as a map for compatibility
func GetInfoMap() map[string]string {
	return map[string]string{
		"version":           Version,
		"git_commit":        GitCommit,
		"build_date":        BuildDate,
		"component_name":    ComponentName,
		"component_version": ComponentVersion,
	}
}

// GetShortCommit returns the short git commit hash (first 7 characters)
func GetShortCommit() string {
	if len(GitCommit) >= 7 {
		return GitCommit[:7]
	}
	return GitCommit
}
