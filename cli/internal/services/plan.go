package services

import (
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Plan struct {
	Profile  string   `yaml:"profile,omitempty"`
	Services []string `yaml:"services"`
}

func SavePlan(dir string, specs []ServiceSpec, profile string) error {
	p := Plan{Profile: profile, Services: make([]string, 0, len(specs))}
	for _, s := range specs {
		p.Services = append(p.Services, s.Name)
	}
	sort.Strings(p.Services)
	b, err := yaml.Marshal(&p)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "plan.yaml")
	return os.WriteFile(path, b, 0o644)
}

func LoadPlan(dir string) (Plan, error) {
	var p Plan
	path := filepath.Join(dir, "plan.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		return Plan{}, err
	}
	if err := yaml.Unmarshal(b, &p); err != nil {
		return Plan{}, err
	}
	return p, nil
}

// ResolveServiceList picks services based on precedence: explicit list > plan > all fragments.
func ResolveServiceList(dir string, explicit []string) ([]string, error) {
	if len(explicit) > 0 {
		return explicit, nil
	}
	if p, err := LoadPlan(dir); err == nil && len(p.Services) > 0 {
		return p.Services, nil
	}
	// Fallback: scan fragment files
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "svc-") && strings.HasSuffix(name, ".yml") {
			base := strings.TrimSuffix(strings.TrimPrefix(name, "svc-"), ".yml")
			if base != "" {
				out = append(out, base)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}
