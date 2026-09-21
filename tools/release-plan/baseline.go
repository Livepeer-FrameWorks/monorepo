package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Track classifies a platform version string. Stable tags look like
// v1.2.3; release-candidates look like v1.2.3-rcN.
type Track string

const (
	TrackStable Track = "stable"
	TrackRC     Track = "rc"
)

// classifyTrack returns "rc" when the tag contains -rc<digits>, else "stable".
// We deliberately don't accept arbitrary pre-release tags (alpha/beta) — the
// project ships stable/rc and nothing else. Adding more tracks should be a
// deliberate schema change, not a regex relaxation.
func classifyTrack(tag string) Track {
	if rcPattern.MatchString(tag) {
		return TrackRC
	}
	return TrackStable
}

var (
	stableTagPattern = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)
	rcTagPattern     = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)-rc(\d+)$`)
	rcPattern        = regexp.MustCompile(`-rc\d+$`)
)

type parsedTag struct {
	raw        string
	major      int
	minor      int
	patch      int
	rc         int
	isRC       bool
	wellFormed bool
}

func parseTag(tag string) parsedTag {
	p := parsedTag{raw: tag}
	if m := stableTagPattern.FindStringSubmatch(tag); m != nil {
		p.major, p.minor, p.patch = atoi(m[1]), atoi(m[2]), atoi(m[3])
		p.wellFormed = true
		return p
	}
	if m := rcTagPattern.FindStringSubmatch(tag); m != nil {
		p.major, p.minor, p.patch = atoi(m[1]), atoi(m[2]), atoi(m[3])
		p.rc = atoi(m[4])
		p.isRC = true
		p.wellFormed = true
		return p
	}
	return p
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// less reports whether a < b in our intended ordering. Stable v1.2.3 sorts
// after every rc v1.2.3-rcN of the same major.minor.patch, matching semver
// pre-release semantics: "rc precedes stable". Tags with different
// major.minor.patch sort by those fields first.
func (a parsedTag) less(b parsedTag) bool {
	if a.major != b.major {
		return a.major < b.major
	}
	if a.minor != b.minor {
		return a.minor < b.minor
	}
	if a.patch != b.patch {
		return a.patch < b.patch
	}
	if a.isRC && !b.isRC {
		return true
	}
	if !a.isRC && b.isRC {
		return false
	}
	if a.isRC && b.isRC {
		return a.rc < b.rc
	}
	return false
}

// listReleases scans gitopsDir/releases for v*.yaml manifests and returns
// them sorted ascending by parsed semver. Files that don't match either
// pattern are ignored.
func listReleases(gitopsDir string) ([]parsedTag, error) {
	releasesDir := filepath.Join(gitopsDir, "releases")
	entries, err := os.ReadDir(releasesDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", releasesDir, err)
	}
	var out []parsedTag
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yaml") {
			continue
		}
		tag := strings.TrimSuffix(name, ".yaml")
		p := parseTag(tag)
		if !p.wellFormed {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].less(out[j]) })
	return out, nil
}

// resolveBaseline picks the latest semantic-version predecessor of newTag.
// Stable and rc releases share one lineage: an rc newer than the latest stable
// is the natural baseline, while an old rc can never displace a newer stable.
//
// If newTag has no eligible baseline (first release ever), the returned
// parsedTag has wellFormed=false and the caller treats every component as
// build.
func resolveBaseline(newTag parsedTag, releases []parsedTag) (parsedTag, []BaselineLineageStep) {
	var lineage []BaselineLineageStep
	if !newTag.wellFormed {
		lineage = append(lineage, BaselineLineageStep{Tag: newTag.raw, Why: "new tag is not well-formed; no baseline"})
		return parsedTag{}, lineage
	}

	predecessor := latest(releases, func(p parsedTag) bool { return p.less(newTag) })
	if predecessor.wellFormed {
		lineage = append(lineage, BaselineLineageStep{
			Track: string(classifyTrack(predecessor.raw)),
			Tag:   predecessor.raw,
			Why:   "latest semantic-version predecessor",
		})
		return predecessor, lineage
	}
	lineage = append(lineage, BaselineLineageStep{Tag: newTag.raw, Why: "no prior release found; treat all components as build"})
	return parsedTag{}, lineage
}

// latest returns the most recent parsedTag matching the predicate, or a
// zero parsedTag (wellFormed=false) if none matches.
func latest(releases []parsedTag, pred func(parsedTag) bool) parsedTag {
	var best parsedTag
	for _, p := range releases {
		if !pred(p) {
			continue
		}
		if !best.wellFormed || best.less(p) {
			best = p
		}
	}
	return best
}

// loadManifest reads and parses a release manifest YAML.
func loadManifest(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var m Manifest
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w", path, err)
	}
	return &m, nil
}

func manifestPath(gitopsDir, tag string) string {
	return filepath.Join(gitopsDir, "releases", tag+".yaml")
}
