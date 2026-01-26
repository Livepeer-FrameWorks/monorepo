package services

import (
	"embed"
	"fmt"
	"gopkg.in/yaml.v3"
	"io/fs"
	"sort"
	"strings"
)

//go:embed catalog.yaml
var embeddedFS embed.FS

type HealthSpec struct {
	Protocol string `yaml:"protocol"`
	Path     string `yaml:"path,omitempty"`
	Port     int    `yaml:"port"`
}

type ServiceSpec struct {
	Name         string     `yaml:"name"`
	Deploy       string     `yaml:"deploy,omitempty"`
	Title        string     `yaml:"title"`
	Role         string     `yaml:"role"`
	Image        string     `yaml:"image"`
	Ports        []string   `yaml:"ports"`
	Health       HealthSpec `yaml:"health"`
	Dependencies []string   `yaml:"dependencies,omitempty"`
}

type Catalog struct {
	Profiles map[string][]string    `yaml:"profiles"`
	Services map[string]ServiceSpec `yaml:"services"`
}

func LoadCatalog() (Catalog, error) {
	var c Catalog
	b, err := fs.ReadFile(embeddedFS, "catalog.yaml")
	if err != nil {
		return Catalog{}, err
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		return Catalog{}, err
	}
	return c, nil
}

func ResolveSelection(c Catalog, profile string, includeCSV string, excludeCSV string) ([]ServiceSpec, error) {
	selected := map[string]struct{}{}
	if profile != "" {
		list, ok := c.Profiles[profile]
		if !ok {
			return nil, fmt.Errorf("unknown profile: %s", profile)
		}
		for _, s := range list {
			selected[s] = struct{}{}
		}
	}
	if includeCSV != "" {
		for _, s := range splitCSV(includeCSV) {
			selected[s] = struct{}{}
		}
	}
	if excludeCSV != "" {
		for _, s := range splitCSV(excludeCSV) {
			delete(selected, s)
		}
	}
	// default: if none selected, use central-all
	if len(selected) == 0 {
		if list, ok := c.Profiles["central-all"]; ok {
			for _, s := range list {
				selected[s] = struct{}{}
			}
		}
	}
	// Build output slice
	names := make([]string, 0, len(selected))
	for n := range selected {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]ServiceSpec, 0, len(names))
	for _, n := range names {
		sp, ok := c.Services[n]
		if !ok {
			return nil, fmt.Errorf("unknown service in selection: %s", n)
		}
		out = append(out, sp)
	}
	return out, nil
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
