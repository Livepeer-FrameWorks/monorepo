package installer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// InstallState tracks binary installation metadata across updates.
type InstallState struct {
	Version     string    `json:"version"`
	InstalledAt time.Time `json:"installed_at"`
	InstallPath string    `json:"install_path"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
	PrevVersion string    `json:"prev_version,omitempty"`
}

func statePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "frameworks", "install.json"), nil
}

// Load reads the install state from disk. Returns nil if no state file exists.
func Load() (*InstallState, error) {
	p, err := statePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var s InstallState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Save persists the install state to disk.
func (s *InstallState) Save() error {
	p, err := statePath()
	if err != nil {
		return err
	}
	if mkErr := os.MkdirAll(filepath.Dir(p), 0755); mkErr != nil {
		return mkErr
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0644)
}

// RecordInstall creates or updates state for a fresh install.
func RecordInstall(version, installPath string) error {
	existing, _ := Load() //nolint:errcheck // missing state file is expected on first install
	s := &InstallState{
		Version:     version,
		InstalledAt: time.Now(),
		InstallPath: installPath,
	}
	if existing != nil {
		s.InstalledAt = existing.InstalledAt
		s.PrevVersion = existing.Version
		s.UpdatedAt = time.Now()
	}
	return s.Save()
}

// String returns a human-readable summary.
func (s *InstallState) String() string {
	if s == nil {
		return "no install state recorded"
	}
	out := fmt.Sprintf("version=%s installed=%s path=%s",
		s.Version, s.InstalledAt.Format(time.RFC3339), s.InstallPath)
	if s.PrevVersion != "" {
		out += fmt.Sprintf(" prev=%s updated=%s", s.PrevVersion, s.UpdatedAt.Format(time.RFC3339))
	}
	return out
}
