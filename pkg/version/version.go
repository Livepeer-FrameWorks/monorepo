package version

// These variables will be injected at build time via ldflags
var (
	Version   = "dev"     // semantic version (e.g., v1.2.3)
	GitCommit = "unknown" // git commit hash
	BuildDate = "unknown" // build timestamp
)

// Info represents version information for a service
type Info struct {
	Version   string `json:"version"`
	GitCommit string `json:"git_commit"`
	BuildDate string `json:"build_date"`
}

// GetInfo returns version information as a struct
func GetInfo() Info {
	return Info{
		Version:   Version,
		GitCommit: GitCommit,
		BuildDate: BuildDate,
	}
}

// GetInfoMap returns version information as a map for compatibility
func GetInfoMap() map[string]string {
	return map[string]string{
		"version":    Version,
		"git_commit": GitCommit,
		"build_date": BuildDate,
	}
}

// GetShortCommit returns the short git commit hash (first 7 characters)
func GetShortCommit() string {
	if len(GitCommit) >= 7 {
		return GitCommit[:7]
	}
	return GitCommit
}
