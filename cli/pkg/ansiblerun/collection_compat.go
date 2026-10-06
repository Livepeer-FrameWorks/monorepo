package ansiblerun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

var ansibleCoreVersionPattern = regexp.MustCompile(`\[core ([0-9][^\]\s]*)\]`)

// checkAnsibleCompatibility refuses an ansible-core release outside the requires_ansible range
// of any collection installed under collectionsPath. A collection run on a release it does not
// support can fail mid-provision inside its own tasks, after hosts were already changed, so the
// mismatch is reported before any playbook runs.
func checkAnsibleCompatibility(ctx context.Context, galaxyBinary, collectionsPath string) error {
	runtimes, err := filepath.Glob(filepath.Join(collectionsPath, "ansible_collections", "*", "*", "meta", "runtime.yml"))
	if err != nil {
		return fmt.Errorf("list installed collections: %w", err)
	}
	sort.Strings(runtimes)
	type requirement struct{ collection, spec string }
	var requirements []requirement
	for _, runtime := range runtimes {
		raw, readErr := os.ReadFile(runtime)
		if readErr != nil {
			return fmt.Errorf("read %s: %w", runtime, readErr)
		}
		var meta struct {
			RequiresAnsible string `yaml:"requires_ansible"`
		}
		if yamlErr := yaml.Unmarshal(raw, &meta); yamlErr != nil {
			return fmt.Errorf("parse %s: %w", runtime, yamlErr)
		}
		if strings.TrimSpace(meta.RequiresAnsible) == "" {
			continue
		}
		requirements = append(requirements, requirement{collectionLabel(filepath.Dir(filepath.Dir(runtime))), meta.RequiresAnsible})
	}
	if len(requirements) == 0 {
		return nil
	}

	core, err := ansibleCoreVersion(ctx, galaxyBinary)
	if err != nil {
		return err
	}
	var refused []string
	for _, r := range requirements {
		ok, specErr := satisfiesRequiresAnsible(core, r.spec)
		if specErr != nil {
			return fmt.Errorf("%s requires_ansible %q: %w", r.collection, r.spec, specErr)
		}
		if !ok {
			refused = append(refused, fmt.Sprintf("%s supports ansible-core %s", r.collection, r.spec))
		}
	}
	if len(refused) > 0 {
		return fmt.Errorf("ansible-core %s is not supported by the pinned Ansible collections (%s); install an ansible-core release inside that range",
			core, strings.Join(refused, "; "))
	}
	return nil
}

// collectionLabel names a collection directory as "namespace.name version", falling back to the
// directory names when MANIFEST.json is missing.
func collectionLabel(dir string) string {
	name := filepath.Base(filepath.Dir(dir)) + "." + filepath.Base(dir)
	raw, err := os.ReadFile(filepath.Join(dir, "MANIFEST.json"))
	if err != nil {
		return name
	}
	var manifest struct {
		CollectionInfo struct {
			Version string `json:"version"`
		} `json:"collection_info"`
	}
	if json.Unmarshal(raw, &manifest) != nil || manifest.CollectionInfo.Version == "" {
		return name
	}
	return name + " " + manifest.CollectionInfo.Version
}

func ansibleCoreVersion(ctx context.Context, galaxyBinary string) (string, error) {
	out, err := exec.CommandContext(ctx, galaxyBinary, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("read ansible-core version from %s --version: %w", galaxyBinary, err)
	}
	match := ansibleCoreVersionPattern.FindSubmatch(out)
	if match == nil {
		return "", fmt.Errorf("%s --version did not report an ansible-core version: %q", galaxyBinary, strings.TrimSpace(string(out)))
	}
	return string(match[1]), nil
}

// satisfiesRequiresAnsible evaluates a collection's requires_ansible specifier (comma-separated
// clauses with >=, <=, >, <, == or !=) against an ansible-core version.
func satisfiesRequiresAnsible(version, spec string) (bool, error) {
	have, err := releaseNumbers(version)
	if err != nil {
		return false, err
	}
	for _, clause := range strings.Split(spec, ",") {
		clause = strings.TrimSpace(clause)
		if clause == "" {
			continue
		}
		op := ""
		for _, candidate := range []string{">=", "<=", "==", "!=", ">", "<"} {
			if strings.HasPrefix(clause, candidate) {
				op = candidate
				break
			}
		}
		if op == "" {
			return false, fmt.Errorf("unsupported version clause %q", clause)
		}
		want, numErr := releaseNumbers(strings.TrimSpace(strings.TrimPrefix(clause, op)))
		if numErr != nil {
			return false, numErr
		}
		cmp := compareRelease(have, want)
		var ok bool
		switch op {
		case ">=":
			ok = cmp >= 0
		case "<=":
			ok = cmp <= 0
		case ">":
			ok = cmp > 0
		case "<":
			ok = cmp < 0
		case "==":
			ok = cmp == 0
		case "!=":
			ok = cmp != 0
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

// releaseNumbers parses the numeric release segments of a version; a pre-release suffix on a
// segment ("0rc1", "0.dev0") is ignored, so a candidate compares as its final release.
func releaseNumbers(version string) ([3]int, error) {
	var out [3]int
	parts := strings.Split(version, ".")
	for i := 0; i < len(parts) && i < 3; i++ {
		digits := parts[i]
		if end := strings.IndexFunc(digits, func(r rune) bool { return r < '0' || r > '9' }); end >= 0 {
			digits = digits[:end]
		}
		if digits == "" {
			if i == 0 {
				return out, errors.New("version " + strconv.Quote(version) + " has no release number")
			}
			break
		}
		n, err := strconv.Atoi(digits)
		if err != nil {
			return out, fmt.Errorf("version %q: %w", version, err)
		}
		out[i] = n
		if digits != parts[i] {
			break
		}
	}
	return out, nil
}

func compareRelease(a, b [3]int) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}
