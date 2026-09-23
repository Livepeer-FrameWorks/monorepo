package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

const (
	schemaPath  = "pkg/graphql/schema.graphql"
	catalogPath = "cli/internal/releases/catalog.yaml"
)

// headRef names the working-tree schema in reports.
const headRef = "HEAD"

var stableTagPattern = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

// semver is a stable vMAJOR.MINOR.PATCH release version.
type semver struct {
	Major, Minor, Patch int
}

func parseSemver(v string) (semver, bool) {
	m := stableTagPattern.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return semver{}, false
	}
	var parts [3]int
	for i := range parts {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return semver{}, false
		}
		parts[i] = n
	}
	return semver{parts[0], parts[1], parts[2]}, true
}

func (v semver) String() string { return fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch) }

func (v semver) Less(o semver) bool {
	if v.Major != o.Major {
		return v.Major < o.Major
	}
	if v.Minor != o.Minor {
		return v.Minor < o.Minor
	}
	return v.Patch < o.Patch
}

// schemaSource returns the schema text at a stable tag, or the working tree
// for headRef.
func schemaSource(repo, ref string) (string, error) {
	if ref == headRef {
		data, err := os.ReadFile(filepath.Join(repo, schemaPath))
		return string(data), err
	}
	out, err := gitOutput(repo, "show", ref+":"+schemaPath)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// gitOutput runs a read-only git command in repo.
func gitOutput(repo string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

// loadFullSchema loads the schema at ref including its @internal fields.
// Everything that describes the public contract uses loadPublicSchema.
func loadFullSchema(repo, ref string) (*ast.Schema, error) {
	text, err := schemaSource(repo, ref)
	if err != nil {
		return nil, err
	}
	schema, gerr := gqlparser.LoadSchema(&ast.Source{Name: ref + ":" + schemaPath, Input: text})
	if gerr != nil {
		return nil, fmt.Errorf("load schema at %s: %w", ref, gerr)
	}
	return schema, nil
}

// stableTags returns the reachable stable release tags, oldest first.
func stableTags(repo string) ([]semver, error) {
	out, err := gitOutput(repo, "tag", "--merged", "HEAD", "--list", "v*")
	if err != nil {
		return nil, err
	}
	var tags []semver
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := parseSemver(line); ok {
			tags = append(tags, v)
		}
	}
	sort.Slice(tags, func(i, j int) bool { return tags[i].Less(tags[j]) })
	return tags, nil
}

var catalogVersionPattern = regexp.MustCompile(`(?m)^\s*-\s*version:\s*(v\d+\.\d+\.\d+)\s*$`)

// pendingRelease returns the highest release the catalog declares, which is
// the release the working tree becomes when it is tagged.
func pendingRelease(repo string) (semver, error) {
	data, err := os.ReadFile(filepath.Join(repo, catalogPath))
	if err != nil {
		return semver{}, err
	}
	var best semver
	found := false
	for _, m := range catalogVersionPattern.FindAllStringSubmatch(string(data), -1) {
		v, ok := parseSemver(m[1])
		if ok && (!found || best.Less(v)) {
			best, found = v, true
		}
	}
	if !found {
		return semver{}, fmt.Errorf("%s declares no release", catalogPath)
	}
	return best, nil
}
