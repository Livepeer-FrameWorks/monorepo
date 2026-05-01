// Package releases is the embedded upgrade-knowledge catalog. It carries
// only metadata that cannot be inferred from embedded SQL or compiled-in
// data-migration registries: per-release compatibility (compatible_from,
// requires_intermediate, min_cli_version) and the list of required data
// migrations a release introduces.
//
// The release list can be empty, but service database ownership is still
// populated so schema gates protect DB-backed services from day one.
package releases

import (
	_ "embed"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed catalog.yaml
var catalogYAML []byte

// Release is one platform release entry from the catalog.
type Release struct {
	Version                string                     `yaml:"version"`
	MinCLIVersion          string                     `yaml:"min_cli_version,omitempty"`
	CompatibleFrom         string                     `yaml:"compatible_from,omitempty"`
	RequiresIntermediate   []string                   `yaml:"requires_intermediate,omitempty"`
	RequiredDataMigrations []DataMigrationRequirement `yaml:"required_data_migrations,omitempty"`
}

// DataMigrationRequirement is the catalog's view of one data migration that a
// release declares as required. Service-side state lives in the service's own
// _data_migrations table and is queried via cluster data-migrate.
type DataMigrationRequirement struct {
	ID                    string `yaml:"id"`
	Service               string `yaml:"service"`
	IntroducedIn          string `yaml:"introduced_in"`
	RequiredBeforePhase   string `yaml:"required_before_phase"` // postdeploy | contract
	RequiredBeforeVersion string `yaml:"required_before_version,omitempty"`
}

type catalogFile struct {
	ServiceDatabases map[string]string `yaml:"service_databases"`
	Releases         []Release         `yaml:"releases"`
}

var (
	catalogOnce sync.Once
	catalogData catalogFile
	catalogErr  error
)

func load() {
	catalogOnce.Do(func() {
		if err := yaml.Unmarshal(catalogYAML, &catalogData); err != nil {
			catalogErr = fmt.Errorf("parse embedded release catalog: %w", err)
			return
		}
		// Sort releases ascending by semver so Catalog() and Path() are
		// deterministic regardless of file order.
		sort.Slice(catalogData.Releases, func(i, j int) bool {
			return CompareSemver(catalogData.Releases[i].Version, catalogData.Releases[j].Version) < 0
		})
	})
}

// Catalog returns all declared releases in ascending version order.
func Catalog() []Release {
	load()
	if catalogErr != nil {
		return nil
	}
	out := make([]Release, len(catalogData.Releases))
	copy(out, catalogData.Releases)
	return out
}

// Lookup returns the release entry for a concrete version, or nil.
func Lookup(version string) *Release {
	load()
	if catalogErr != nil {
		return nil
	}
	for i := range catalogData.Releases {
		if catalogData.Releases[i].Version == version {
			return &catalogData.Releases[i]
		}
	}
	return nil
}

// ServiceDatabase returns the platform database name a service owns, or "" if
// the catalog has not declared ownership for that service. Empty result is
// the honest "ownership unknown" signal — gates must treat it as a reason to
// refuse, not as "service has no database."
func ServiceDatabase(service string) string {
	db, _ := ServiceDatabaseLookup(service)
	return db
}

// ServiceDatabaseLookup returns the platform database name a service owns and
// whether the ownership map contains an entry for that service.
func ServiceDatabaseLookup(service string) (string, bool) {
	load()
	if catalogErr != nil {
		return "", false
	}
	db, ok := catalogData.ServiceDatabases[service]
	return db, ok
}

// LoadError exposes any parse error encountered loading the embedded catalog.
// Callers that need to fail-closed on a corrupt catalog should check this.
func LoadError() error {
	load()
	return catalogErr
}

// Path returns every release in (from, to] that gates the upgrade. The walk
// follows compatible_from links and refuses direct skips when an intermediate
// is required but missing.
//
// Empty release catalogs return an empty path with no error. A non-empty
// catalog that does not declare the target release is unsafe for DB-backed
// upgrades because the CLI cannot know compatibility or required data work.
//
// If the catalog declares to.compatible_from > from, returns an error naming
// the intermediate(s) the operator must transit first.
func Path(from, to string) ([]Release, error) {
	load()
	if catalogErr != nil {
		return nil, catalogErr
	}
	if from == "" || to == "" {
		return nil, nil
	}

	target := Lookup(to)
	if target == nil {
		if len(catalogData.Releases) > 0 {
			return nil, fmt.Errorf("target release %s is not declared in the embedded release catalog", to)
		}
		return nil, nil
	}

	if target.CompatibleFrom != "" && CompareSemver(target.CompatibleFrom, from) > 0 {
		return nil, fmt.Errorf("direct upgrade %s -> %s not allowed: %s.compatible_from = %s; deploy %s first",
			from, to, to, target.CompatibleFrom, target.CompatibleFrom)
	}

	for _, rel := range target.RequiresIntermediate {
		if CompareSemver(rel, from) > 0 && CompareSemver(rel, to) < 0 {
			return nil, fmt.Errorf("direct upgrade %s -> %s not allowed: requires intermediate %s; deploy it first",
				from, to, rel)
		}
	}

	var out []Release
	for _, rel := range catalogData.Releases {
		if CompareSemver(rel.Version, from) > 0 && CompareSemver(rel.Version, to) <= 0 {
			out = append(out, rel)
		}
	}
	return out, nil
}

// CompareSemver compares two vX.Y.Z[-tag] strings. Returns -1, 0, +1.
// Pre-release tags compare lexicographically when the (X, Y, Z) tuple is
// equal; an absent tag sorts after one that is present (i.e. v1.0.0 > v1.0.0-rc1).
func CompareSemver(a, b string) int {
	majA, minA, patA, preA := parseSemver(a)
	majB, minB, patB, preB := parseSemver(b)
	for _, pair := range [][2]int{{majA, majB}, {minA, minB}, {patA, patB}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	switch {
	case preA == "" && preB == "":
		return 0
	case preA == "":
		return 1
	case preB == "":
		return -1
	case preA < preB:
		return -1
	case preA > preB:
		return 1
	default:
		return 0
	}
}

func parseSemver(v string) (maj, min, pat int, pre string) {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		pre = v[i+1:]
		v = v[:i]
	}
	parts := strings.SplitN(v, ".", 3)
	for i := range parts {
		n, _ := strconv.Atoi(parts[i]) //nolint:errcheck // best-effort parse, returns 0 on failure
		switch i {
		case 0:
			maj = n
		case 1:
			min = n
		case 2:
			pat = n
		}
	}
	return
}
