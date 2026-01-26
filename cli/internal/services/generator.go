package services

import (
	"bytes"
	"fmt"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
)

// ComposeModel is a minimal Docker Compose structure used for generation.
type ComposeModel struct {
	Version  string                    `yaml:"version"`
	Services map[string]map[string]any `yaml:"services"`
}

// GenerateFragments writes one compose fragment per service into dir as svc-<name>.yml
func GenerateFragments(dir string, specs []ServiceSpec, overwrite bool) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, s := range specs {
		if s.Role == "observability" {
			fmt.Fprintf(os.Stderr, "WARNING: %s requires manual setup (volumes, configs not yet supported by generator)\n", s.Name)
			continue
		}
		name := fmt.Sprintf("svc-%s.yml", s.Name)
		path := filepath.Join(dir, name)
		if !overwrite {
			if _, err := os.Stat(path); err == nil {
				return fmt.Errorf("file exists: %s (use --overwrite)", path)
			}
		}
		m := ComposeModel{Version: "3.9", Services: map[string]map[string]any{}}
		deployName := s.Deploy
		if deployName == "" {
			deployName = s.Name
		}
		svc := map[string]any{
			"image":          s.Image,
			"container_name": fmt.Sprintf("frameworks-%s", deployName),
			"restart":        "unless-stopped",
			"env_file":       []string{".central.env"},
		}
		if len(s.Ports) > 0 {
			svc["ports"] = s.Ports
		}
		m.Services[s.Name] = svc
		var buf bytes.Buffer
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(2)
		if err := enc.Encode(m); err != nil {
			return err
		}
		_ = enc.Close()
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// GenerateEnv returns a minimal .env suggestion (placeholder for future use).
func SummarizeSelection(specs []ServiceSpec) string {
	var b bytes.Buffer
	for _, s := range specs {
		fmt.Fprintf(&b, "- %-18s %-10s %s\n", s.Name+":", s.Role, s.Image)
	}
	return b.String()
}
