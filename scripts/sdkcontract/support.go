package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

const (
	supportPath = publicDir + "/support.yaml"
	majorsDir   = publicDir + "/majors"
)

// supportFile is pkg/graphql/public/support.yaml: the SDK version lines and
// the platform releases each supports.
type supportFile struct {
	Current string        `yaml:"current"`
	Lines   []supportLine `yaml:"lines"`
}

type supportLine struct {
	Line      string `yaml:"line"`
	Status    string `yaml:"status"`
	MinServer string `yaml:"min_server"`
}

const (
	lineLive    = "live"
	lineRetired = "retired"
)

func loadSupport(repo string) (*supportFile, error) {
	data, err := os.ReadFile(filepath.Join(repo, supportPath))
	if err != nil {
		return nil, err
	}
	var s supportFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("%s: %w", supportPath, err)
	}
	if err := s.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", supportPath, err)
	}
	return &s, nil
}

func (s *supportFile) validate() error {
	if len(s.Lines) == 0 {
		return errors.New("no SDK lines")
	}
	seen := map[string]bool{}
	for _, l := range s.Lines {
		if seen[l.Line] {
			return fmt.Errorf("line %s is listed twice", l.Line)
		}
		seen[l.Line] = true
		if l.Status != lineLive && l.Status != lineRetired {
			return fmt.Errorf("line %s: status %q is neither %s nor %s", l.Line, l.Status, lineLive, lineRetired)
		}
		if _, ok := parseSemver(l.MinServer); !ok {
			return fmt.Errorf("line %s: min_server %q is not a stable release version", l.Line, l.MinServer)
		}
		if majorOf(l.Line) < 0 {
			return fmt.Errorf("line %s is not MAJOR.MINOR", l.Line)
		}
	}
	cur := s.line(s.Current)
	if cur == nil {
		return fmt.Errorf("current line %q is not listed", s.Current)
	}
	if cur.Status != lineLive {
		return fmt.Errorf("current line %s must be live", s.Current)
	}
	return nil
}

func (s *supportFile) line(name string) *supportLine {
	for i := range s.Lines {
		if s.Lines[i].Line == name {
			return &s.Lines[i]
		}
	}
	return nil
}

func majorOf(line string) int {
	var major, minor int
	if n, err := fmt.Sscanf(line, "%d.%d", &major, &minor); n != 2 || err != nil || fmt.Sprintf("%d.%d", major, minor) != line {
		return -1
	}
	return major
}

// manifest is pkg/graphql/public/majors/v<N>.json: every line of one SDK
// major with the operation documents that line ships. Lines other than the
// current one are frozen; their documents are what the compatibility checks
// validate against later releases.
type manifest struct {
	Major int                     `json:"major"`
	Lines map[string]manifestLine `json:"lines"`
}

type manifestLine struct {
	MinServer  string              `json:"minServer"`
	Operations []manifestOperation `json:"operations"`
}

type manifestOperation struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Since    string `json:"since"`
	Hash     string `json:"hash"`
	Document string `json:"document"`
}

func manifestPath(repo string, major int) string {
	return filepath.Join(repo, majorsDir, fmt.Sprintf("v%d.json", major))
}

func loadManifest(repo string, major int) (*manifest, error) {
	data, err := os.ReadFile(manifestPath(repo, major))
	if errors.Is(err, os.ErrNotExist) {
		return &manifest{Major: major, Lines: map[string]manifestLine{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", manifestPath(repo, major), err)
	}
	if m.Lines == nil {
		m.Lines = map[string]manifestLine{}
	}
	return &m, nil
}

func (m *manifest) encode() ([]byte, error) {
	for name, line := range m.Lines {
		sort.Slice(line.Operations, func(i, j int) bool { return line.Operations[i].Name < line.Operations[j].Name })
		m.Lines[name] = line
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
