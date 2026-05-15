package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Planner builds a PlanOutput by hashing each component in
// release-components.json against the baseline manifest's recorded
// source_hash for that component.
type Planner struct {
	MonorepoRoot string
	GitopsDir    string
	NewTag       string
	Components   ReleaseComponents

	workflowSalt       string
	goToolchainVersion string
}

func NewPlanner(monorepoRoot, gitopsDir, newTag string, components ReleaseComponents) *Planner {
	return &Planner{
		MonorepoRoot: monorepoRoot,
		GitopsDir:    gitopsDir,
		NewTag:       newTag,
		Components:   components,
	}
}

// Plan computes the per-component decisions and returns a PlanOutput ready
// to marshal.
func (p *Planner) Plan() (*PlanOutput, error) {
	salt, err := HashWorkflowFiles(p.MonorepoRoot)
	if err != nil {
		return nil, err
	}
	p.workflowSalt = salt

	tv, err := ReadGoToolchainVersion(p.MonorepoRoot)
	if err != nil {
		return nil, err
	}
	p.goToolchainVersion = tv

	parsed := parseTag(p.NewTag)
	releases, err := listReleases(p.GitopsDir)
	if err != nil {
		return nil, err
	}
	baseline, lineage := resolveBaseline(parsed, releases)

	out := &PlanOutput{
		PlatformVersion: p.NewTag,
		Track:           string(classifyTrack(p.NewTag)),
		GeneratedAt:     time.Now().UTC(),
		Decisions:       map[string]Decision{},
		BaselineLineage: lineage,
	}

	var baselineManifest *Manifest
	if baseline.wellFormed {
		out.BaselineTag = baseline.raw
		out.BaselinePath = manifestPath(p.GitopsDir, baseline.raw)
		baselineManifest, err = loadManifest(out.BaselinePath)
		if err != nil {
			return nil, err
		}
	}

	for _, comp := range p.Components.Services {
		d, err := p.decideForGoService(comp, KindService, baselineManifest)
		if err != nil {
			return nil, fmt.Errorf("decide %s: %w", comp.Name, err)
		}
		out.Decisions[comp.Name] = d
	}
	nodeToolchain := ReadNodeToolchainVersion(p.MonorepoRoot)
	for _, app := range p.Components.Webapps {
		d, err := p.decideForWebapp(app, nodeToolchain, baselineManifest)
		if err != nil {
			return nil, fmt.Errorf("decide webapp %s: %w", app.Name, err)
		}
		out.Decisions[app.Name] = d
	}

	for _, d := range out.Decisions {
		switch d.Action {
		case ActionBuild:
			out.Summary.BuildCount++
		case ActionCarryForward:
			out.Summary.CarryForwardCount++
		}
	}

	return out, nil
}

func (p *Planner) decideForGoService(comp ReleaseComponent, kind ComponentKind, baseline *Manifest) (Decision, error) {
	hash, _, err := ComputeServiceSourceHash(HashInputs{
		MonorepoRoot:       p.MonorepoRoot,
		Component:          comp,
		WorkflowSalt:       p.workflowSalt,
		GoToolchainVersion: p.goToolchainVersion,
	})
	if err != nil {
		return Decision{}, err
	}

	d := Decision{
		Name:       comp.Name,
		Kind:       kind,
		Action:     ActionBuild,
		SourceHash: hash,
	}

	if baseline == nil {
		return d, nil
	}

	prior := findServiceInManifest(baseline, comp.Name)
	if prior == nil || prior.SourceHash == "" || prior.SourceHash != hash {
		if prior != nil {
			d.BaselineSourceHash = prior.SourceHash
		}
		return d, nil
	}

	d.Action = ActionCarryForward
	d.BaselineTag = baseline.PlatformVersion
	d.BaselineSourceHash = prior.SourceHash
	carried := *prior
	d.CarriedService = &carried
	if nb := findNativeBinaryInManifest(baseline, comp.Name); nb != nil {
		copyNB := *nb
		d.CarriedNativeBinary = &copyNB
	}
	return d, nil
}

func (p *Planner) decideForWebapp(app ReleaseWebapp, nodeToolchain string, baseline *Manifest) (Decision, error) {
	hash, _, err := ComputeWebappSourceHash(WebappHashInputs{
		MonorepoRoot:         p.MonorepoRoot,
		Webapp:               app,
		WorkflowSalt:         p.workflowSalt,
		NodeToolchainVersion: nodeToolchain,
	})
	if err != nil {
		return Decision{}, err
	}

	d := Decision{
		Name:       app.Name,
		Kind:       KindInterface,
		Action:     ActionBuild,
		SourceHash: hash,
	}

	if baseline == nil {
		return d, nil
	}
	prior := findInterfaceInManifest(baseline, app.Name)
	if prior == nil || prior.SourceHash == "" || prior.SourceHash != hash {
		if prior != nil {
			d.BaselineSourceHash = prior.SourceHash
		}
		return d, nil
	}

	d.Action = ActionCarryForward
	d.BaselineTag = baseline.PlatformVersion
	d.BaselineSourceHash = prior.SourceHash
	carried := *prior
	d.CarriedInterface = &carried
	return d, nil
}

func findInterfaceInManifest(m *Manifest, name string) *InterfaceEntry {
	if m == nil {
		return nil
	}
	for i := range m.Interfaces {
		if m.Interfaces[i].Name == name {
			return &m.Interfaces[i]
		}
	}
	return nil
}

func findServiceInManifest(m *Manifest, name string) *ServiceEntry {
	if m == nil {
		return nil
	}
	for i := range m.Services {
		if m.Services[i].Name == name {
			return &m.Services[i]
		}
	}
	return nil
}

func findNativeBinaryInManifest(m *Manifest, name string) *NativeBinary {
	if m == nil {
		return nil
	}
	for i := range m.NativeBinaries {
		if m.NativeBinaries[i].Name == name {
			return &m.NativeBinaries[i]
		}
	}
	return nil
}

// LoadComponentsFromFile reads .github/release-components.json.
func LoadComponentsFromFile(path string) (ReleaseComponents, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return ReleaseComponents{}, fmt.Errorf("read %s: %w", path, err)
	}
	var c ReleaseComponents
	if err := json.Unmarshal(b, &c); err != nil {
		return ReleaseComponents{}, fmt.Errorf("unmarshal %s: %w", path, err)
	}
	return c, nil
}

// WriteJSON marshals out to dest path with stable indentation.
func WriteJSON(dest string, out *PlanOutput) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	if !strings.HasSuffix(string(b), "\n") {
		b = append(b, '\n')
	}
	return os.WriteFile(dest, b, 0o644)
}
