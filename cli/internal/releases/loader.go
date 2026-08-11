// Package releases is the embedded upgrade-knowledge catalog. It carries
// only metadata that cannot be inferred from embedded SQL or compiled-in
// data-migration registries: per-release tooling floor (min_cli_version),
// automatic-rollback policy (rollback_disabled), and the list of required
// data migrations a release introduces.
//
// The release list can be empty, but service database ownership is still
// populated so schema gates protect DB-backed services from day one.
package releases

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

var baseVersionPattern = regexp.MustCompile(`^(v?\d+\.\d+\.\d+)`)

// BaseVersion strips any pre-release/build suffix from a version, returning a CANONICAL vX.Y.Z. A pre-release of X
// (e.g. v0.2.97-rc1) is built from the X line and carries X's schema/behavior, so "is this migration/transition
// introduced by the release I'm deploying?" compares the introduced version against the DEPLOY TARGET'S BASE --
// otherwise a canary (-rc1, which sorts before the final) would skip the very migrations/transitions the final
// introduces. The leading `v` is CANONICALIZED (always present in the result) so a version's base identity is
// spelling-independent: "1.0.0" and "v1.0.0" share one base ("v1.0.0"), which is what makes the one-entry-per-release-
// line check and Lookup immune to the optional-`v` distinction.
func BaseVersion(v string) string {
	m := baseVersionPattern.FindString(strings.TrimSpace(v))
	if m == "" {
		return strings.TrimSpace(v)
	}
	if !strings.HasPrefix(m, "v") {
		return "v" + m
	}
	return m
}

//go:embed catalog.yaml
var catalogYAML []byte

// Release is one platform release entry from the catalog.
type Release struct {
	Version                string                     `yaml:"version"`
	MinCLIVersion          string                     `yaml:"min_cli_version,omitempty"`
	RequiredDataMigrations []DataMigrationRequirement `yaml:"required_data_migrations,omitempty"`
	// RollbackDisabled lists deploy names whose readiness contract changed in THIS release such that a restored
	// previous binary cannot pass the current health gate — published into the fetched manifest so `cluster upgrade`
	// skips automatic rollback for them this release only (see gitops.Manifest.RollbackDisabled).
	RollbackDisabled []string `yaml:"rollback_disabled,omitempty"`
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
	ServiceDatabases   map[string]string              `yaml:"service_databases"`
	Releases           []Release                      `yaml:"releases"`
	ReleaseTransitions []ReleaseTransitionRequirement `yaml:"release_transitions,omitempty"`
}

// ReleaseTransitionRequirement declares a reconciliation transition the release DAG must run once the target reaches
// IntroducedIn. It is keyed by the transition's COMPILED handler id. This EMBEDDED catalog only knows transitions the
// running CLI was built with, so it cannot, on its own, make an OUTDATED CLI aware of a NEWER release's transition —
// that authority is the FETCHED release metadata (gitops.Manifest.RequiredTransitions + MinCLIVersion), which the
// upgrade/release paths validate against this compiled registry BEFORE any migration, failing closed if the running
// CLI is too old or missing a required handler.
type ReleaseTransitionRequirement struct {
	ID           string `yaml:"id"`
	IntroducedIn string `yaml:"introduced_in"`
}

// catalogState is an immutable parsed catalog. Parsing happens ONCE at package
// initialization via parseCatalog; there is no mutable global and no lazy
// sync.Once, so a corrupt catalog is a fixed property of the parsed value (its
// err field) — not something a caller, or a test, can toggle at runtime.
// Accessors read this value and fail closed when err is set.
type catalogState struct {
	releases         []Release
	serviceDatabases map[string]string
	transitions      []ReleaseTransitionRequirement
	err              error
}

// strictSemverPattern accepts a CANONICAL vX.Y.Z with an optional -prerelease and +build. The leading `v` is REQUIRED
// (unlike baseVersionPattern, which tolerates its absence when normalizing external inputs): the catalog must use one
// canonical spelling so `v1.0.0` and `1.0.0` cannot both be declared and evade the one-entry-per-release-line check.
// It anchors both ends so a typo'd or truncated version is rejected rather than silently parsed with zeroed components.
var strictSemverPattern = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

// ValidateVersion is the SINGLE canonical version validator: a well-formed vX.Y.Z[-pre][+build] with a REQUIRED leading
// v, no leading zeros in the version core or numeric prerelease identifiers, and no empty dot-separated identifiers.
// Every version-consuming gate — catalog entries, fetched min_cli_version, CLI-version detection, migration targets,
// and release selectors — validates through this one function, so they all apply identical rules before CompareSemver
// and a malformed value (v1.0.0-, v1.0.0-., v1.0.0-01) is rejected everywhere rather than silently mis-parsed.
func ValidateVersion(v string) error {
	// BYTE-EXACT: no internal trimming. A validator that trims would pass " v1.2.3 " while callers keep — and reuse
	// (as a filename, migration target, or comparison key) — the untrimmed original. The anchored pattern rejects any
	// surrounding whitespace, so a value that validates is exactly the value callers use.
	if !strictSemverPattern.MatchString(v) {
		return fmt.Errorf("%q is not a valid version (want vX.Y.Z[-pre][+build], no surrounding whitespace)", v)
	}
	return validateVersionStructure(v)
}

// parseCatalog is a PURE function: bytes in, immutable parsed state out. Decoding is STRICT (KnownFields) so a
// misspelled key — `rollback_disabld`, `required_data_migations` — is a hard error rather than a silently dropped
// field that removes a release gate; the decoded catalog is then semantically validated (version formats + uniqueness,
// migration phases/ids/services, transition ids, ownership names). Any failure is carried on the returned state's err
// (it never panics and never mutates a global), so the embedded catalog and any test fixture parse through one path.
func parseCatalog(data []byte) *catalogState {
	var cf catalogFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cf); err != nil && !errors.Is(err, io.EOF) {
		return &catalogState{err: fmt.Errorf("parse embedded release catalog: %w", err)}
	}
	// Reject trailing YAML documents: a `---`-separated second document would be silently ignored, so a whole extra
	// catalog (or a stray override) could sit in the file unread. Only a single document is a valid catalog.
	if err := dec.Decode(new(catalogFile)); !errors.Is(err, io.EOF) {
		if err == nil {
			return &catalogState{err: fmt.Errorf("invalid release catalog: unexpected trailing YAML document (the catalog must be a single document)")}
		}
		return &catalogState{err: fmt.Errorf("parse embedded release catalog: %w", err)}
	}
	if err := validateCatalog(&cf); err != nil {
		return &catalogState{err: fmt.Errorf("invalid release catalog: %w", err)}
	}
	// Sort releases ascending by semver so Catalog() and ReleasesBelow() are
	// deterministic regardless of file order.
	sort.Slice(cf.Releases, func(i, j int) bool {
		return CompareSemver(cf.Releases[i].Version, cf.Releases[j].Version) < 0
	})
	return &catalogState{
		releases:         cf.Releases,
		serviceDatabases: cf.ServiceDatabases,
		transitions:      cf.ReleaseTransitions,
	}
}

// validateCatalog enforces the catalog's semantic contract after a strict decode. It is fail-closed: the FIRST problem
// aborts, so a broken catalog is never partially trusted. Checks are INTERNAL-consistency only (formats, uniqueness,
// enums, non-empty identifiers) — cross-artifact completeness (e.g. every database-backed service owning an entry) is
// asserted separately against topology, which this package does not import.
func validateCatalog(cf *catalogFile) error {
	validVersion := func(field, v string) error {
		if err := ValidateVersion(v); err != nil {
			return fmt.Errorf("%s %w", field, err)
		}
		return nil
	}
	// Uniqueness is by canonical BASE version, not raw string: Lookup base-normalizes (an RC resolves to its final), so
	// declaring both v1.0.0-rc1 and v1.0.0 would let whichever sorts first silently supply the release line's
	// rollback/min-CLI policy. One entry per release line, period. (This also rejects exact-string duplicates.)
	seenBase := map[string]bool{}
	for _, rel := range cf.Releases {
		if err := validVersion("release version", rel.Version); err != nil {
			return err
		}
		base := BaseVersion(rel.Version)
		if seenBase[base] {
			return fmt.Errorf("multiple catalog entries for release line %q (e.g. %q); declare exactly one entry per base version", base, rel.Version)
		}
		seenBase[base] = true
		if rel.MinCLIVersion != "" {
			if err := validVersion(fmt.Sprintf("release %s min_cli_version", rel.Version), rel.MinCLIVersion); err != nil {
				return err
			}
		}
		for _, dm := range rel.RequiredDataMigrations {
			if strings.TrimSpace(dm.ID) == "" || strings.TrimSpace(dm.Service) == "" {
				return fmt.Errorf("release %s declares a data migration with empty id or service", rel.Version)
			}
			if err := validVersion(fmt.Sprintf("release %s data migration %s introduced_in", rel.Version, dm.ID), dm.IntroducedIn); err != nil {
				return err
			}
			switch dm.RequiredBeforePhase {
			case "postdeploy", "contract":
			default:
				return fmt.Errorf("release %s data migration %s has invalid required_before_phase %q (want postdeploy|contract)", rel.Version, dm.ID, dm.RequiredBeforePhase)
			}
			if dm.RequiredBeforeVersion != "" {
				if err := validVersion(fmt.Sprintf("release %s data migration %s required_before_version", rel.Version, dm.ID), dm.RequiredBeforeVersion); err != nil {
					return err
				}
			}
		}
	}
	seenTransition := map[string]bool{}
	for _, rt := range cf.ReleaseTransitions {
		if strings.TrimSpace(rt.ID) == "" {
			return fmt.Errorf("release transition with empty id")
		}
		if seenTransition[rt.ID] {
			return fmt.Errorf("duplicate release transition id %q", rt.ID)
		}
		seenTransition[rt.ID] = true
		if err := validVersion(fmt.Sprintf("transition %s introduced_in", rt.ID), rt.IntroducedIn); err != nil {
			return err
		}
	}
	for svc, db := range cf.ServiceDatabases {
		if strings.TrimSpace(svc) == "" || strings.TrimSpace(db) == "" {
			return fmt.Errorf("service_databases has an empty service or database name (%q: %q)", svc, db)
		}
	}
	return nil
}

// embedded is the process-wide catalog, parsed once at init from the embedded
// YAML and immutable thereafter.
var embedded = parseCatalog(catalogYAML)

// Catalog returns all declared releases in ascending version order. It returns
// nil for BOTH an intentionally-empty catalog and a parse failure — callers that
// gate on catalog contents MUST NOT use it, because they cannot distinguish
// "nothing declared" from "corrupt, read nothing". Use CatalogOrError instead so
// a corrupt catalog fails closed rather than reading as empty.
func Catalog() []Release {
	if embedded.err != nil {
		return nil
	}
	out := make([]Release, len(embedded.releases))
	copy(out, embedded.releases)
	return out
}

// CatalogOrError returns the declared releases and any load error. This is the
// accessor gates MUST use: a parse failure surfaces as a non-nil error (fail
// closed) instead of being silently reinterpreted as an empty catalog, which
// would disable every catalog-driven gate. A successful load with zero declared
// releases returns (nil, nil) — honestly empty.
func CatalogOrError() ([]Release, error) {
	if embedded.err != nil {
		return nil, embedded.err
	}
	out := make([]Release, len(embedded.releases))
	copy(out, embedded.releases)
	return out, nil
}

// ReleaseTransitionsUpTo returns the declared reconciliation transitions a release to `target` must run — those whose
// IntroducedIn is <= the target's BASE version (so a canary of X includes X's transitions). A corrupt catalog is an
// ERROR (fail closed), never an empty "nothing required" list.
func ReleaseTransitionsUpTo(target string) ([]ReleaseTransitionRequirement, error) {
	if embedded.err != nil {
		return nil, embedded.err
	}
	base := BaseVersion(target)
	var out []ReleaseTransitionRequirement
	for _, rt := range embedded.transitions {
		if rt.IntroducedIn == "" || CompareSemver(rt.IntroducedIn, base) <= 0 {
			out = append(out, rt)
		}
	}
	return out, nil
}

// RequiredTransitionIDs returns the compiled handler ids a release to `target` requires (those introduced at or before
// the target's base version), for PUBLISHING into the fetched release manifest so an outdated CLI can validate them.
func RequiredTransitionIDs(target string) ([]string, error) {
	reqs, err := ReleaseTransitionsUpTo(target)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(reqs))
	for _, r := range reqs {
		ids = append(ids, r.ID)
	}
	return ids, nil
}

// MinCLIVersionFor returns the declared minimum CLI version for a concrete release version, or "" when the catalog
// does not declare one. Published into the fetched manifest so an outdated CLI fails closed before deploying.
func MinCLIVersionFor(version string) string {
	if r := Lookup(version); r != nil {
		return r.MinCLIVersion
	}
	return ""
}

// RollbackDisabledFor returns the deploy names for which a release disables automatic rollback (base-version
// normalized), for PUBLISHING into the fetched manifest. Empty when the catalog declares none.
func RollbackDisabledFor(version string) []string {
	if r := Lookup(version); r != nil {
		return r.RollbackDisabled
	}
	return nil
}

// Lookup returns the release entry for a version, or nil. Matching is BASE-VERSION normalized: a prerelease/RC target
// (v0.2.97-rc1) resolves to the declared final release (v0.2.97) — an RC carries the same catalog requirements as its
// base, and the catalog declares one entry per base version, not per RC.
func Lookup(version string) *Release {
	if embedded.err != nil {
		return nil
	}
	want := BaseVersion(version)
	for i := range embedded.releases {
		if BaseVersion(embedded.releases[i].Version) == want {
			return &embedded.releases[i]
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
	if embedded.err != nil {
		return "", false
	}
	db, ok := embedded.serviceDatabases[service]
	return db, ok
}

// LoadError exposes any parse error encountered loading the embedded catalog.
// Callers that need to fail-closed on a corrupt catalog should check this.
func LoadError() error {
	return embedded.err
}

// ReleasesBelow returns every catalog release whose BASE version is strictly below the target's base — the prior
// releases whose postdeploy migrations must already be present before the target deploys, in ascending order. The
// pre-deploy migration gate does NOT scan these one release at a time: because the migration ledger check is CUMULATIVE
// (every postdeploy migration <= a given version), it checks only the HIGHEST entry here once, which subsumes all the
// others. The selection is deliberately independent of any running service's reported version: a skewed/unreadable
// replica cannot narrow it — completeness is proven against the actual _migrations ledger (the authority). The target's
// own base is EXCLUDED (its postdeploy runs after the deploy); comparing by BASE version so an RC target (v0.2.97-rc1)
// still excludes the declared final (v0.2.97). Empty catalog ⇒ empty.
func ReleasesBelow(target string) []Release {
	if embedded.err != nil {
		return nil
	}
	return releasesBelow(embedded.releases, target)
}

// releasesBelow is the pure selection over an already-ascending release slice,
// split out so it is testable without swapping any package state.
func releasesBelow(releases []Release, target string) []Release {
	targetBase := BaseVersion(target)
	var out []Release
	for _, rel := range releases {
		if CompareSemver(BaseVersion(rel.Version), targetBase) < 0 {
			out = append(out, rel)
		}
	}
	return out
}

// CompareSemver compares two vX.Y.Z[-prerelease][+build] strings. Returns -1, 0, +1. It follows SemVer precedence
// (semver.org clause 11): the (major, minor, patch) tuple decides first; an absent prerelease outranks any present one
// (v1.0.0 > v1.0.0-rc1); build metadata is IGNORED. Prerelease identifiers compare dot-by-dot — numeric identifiers
// numerically, alphanumeric lexically, numeric below alphanumeric, and a longer identifier list outranks a shorter one
// that is otherwise equal. Additionally, a trailing numeric run WITHIN one alphanumeric identifier compares numerically
// when the non-numeric prefixes match, so our combined-form prereleases order correctly (rc2 < rc10, not the lexical
// rc10 < rc2). This gate protects both the embedded catalog and the fetched release manifest, so getting rc ordering
// right is what keeps an older CLI from passing a higher-rc floor.
func CompareSemver(a, b string) int {
	majA, minA, patA, preA := splitSemver(a)
	majB, minB, patB, preB := splitSemver(b)
	for _, pair := range [][2]string{{majA, majB}, {minA, minB}, {patA, patB}} {
		if c := compareNumericString(pair[0], pair[1]); c != 0 {
			return c
		}
	}
	return comparePrerelease(preA, preB)
}

// splitSemver returns the raw major/minor/patch numeric STRINGS and the prerelease, with build metadata stripped.
// Keeping the components as strings lets compareNumericString order them without ever parsing to a bounded integer, so
// an overflowing component cannot be silently truncated (to zero) and mis-ordered.
func splitSemver(v string) (maj, min, pat, pre string) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	// Build metadata (+...) has NO effect on precedence (SemVer clause 10) -- strip it before extracting the prerelease.
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	if i := strings.IndexByte(v, '-'); i >= 0 {
		pre = v[i+1:]
		v = v[:i]
	}
	parts := strings.SplitN(v, ".", 3)
	at := func(i int) string {
		if i < len(parts) {
			return parts[i]
		}
		return "0"
	}
	return at(0), at(1), at(2), pre
}

// compareNumericString compares two decimal digit strings numerically, without parsing to a bounded integer: leading
// zeros are ignored, a longer run of significant digits is larger, and equal-length runs compare lexically. A non-digit
// or empty input normalizes to zero. This is what keeps a huge numeric component from mis-ordering.
func compareNumericString(a, b string) int {
	a, b = normalizeNumeric(a), normalizeNumeric(b)
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

// normalizeNumeric strips leading zeros; a zero, empty, or non-digit input normalizes to "" (meaning zero).
func normalizeNumeric(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return ""
		}
	}
	return strings.TrimLeft(s, "0")
}

// comparePrerelease implements SemVer clause 11 prerelease precedence. Empty (no prerelease) outranks any present prerelease.
func comparePrerelease(a, b string) int {
	switch {
	case a == "" && b == "":
		return 0
	case a == "":
		return 1 // no prerelease > any prerelease
	case b == "":
		return -1
	}
	ai := strings.Split(a, ".")
	bi := strings.Split(b, ".")
	for i := 0; i < len(ai) && i < len(bi); i++ {
		if c := comparePrereleaseIdent(ai[i], bi[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(ai) < len(bi):
		return -1 // a is a prefix of b → fewer identifiers has lower precedence
	case len(ai) > len(bi):
		return 1
	default:
		return 0
	}
}

// comparePrereleaseIdent compares one dot-separated prerelease identifier. Pure-numeric identifiers compare numerically
// and rank below alphanumeric ones. Two alphanumeric identifiers that share a non-numeric prefix and differ only in a
// trailing numeric run compare on that run numerically (rc2 < rc10); otherwise they compare lexically (ASCII).
func comparePrereleaseIdent(a, b string) int {
	aAllDigits := isAllDigits(a)
	bAllDigits := isAllDigits(b)
	switch {
	case aAllDigits && bAllDigits:
		return compareNumericString(a, b)
	case aAllDigits: // numeric < alphanumeric
		return -1
	case bAllDigits:
		return 1
	}
	aPre, aTail, aHas := splitTrailingDigits(a)
	bPre, bTail, bHas := splitTrailingDigits(b)
	if aHas && bHas && aPre == bPre {
		return compareNumericString(aTail, bTail)
	}
	return strings.Compare(a, b)
}

// isAllDigits reports whether s is a non-empty run of ASCII digits.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// splitTrailingDigits splits s into a leading part and a trailing run of digit CHARACTERS (e.g. "rc10" -> "rc", "10",
// true). hasDigits is false when s does not end in a digit. The digit run is returned as a string so the caller can
// compare it without integer overflow.
func splitTrailingDigits(s string) (prefix, digits string, hasDigits bool) {
	i := len(s)
	for i > 0 && s[i-1] >= '0' && s[i-1] <= '9' {
		i--
	}
	if i == len(s) {
		return s, "", false
	}
	return s[:i], s[i:], true
}

// validateVersionStructure enforces the SemVer identifier rules the permissive regex cannot express: the version core
// and numeric prerelease identifiers must not carry leading zeros (SemVer clauses 2/9), and no dot-separated
// prerelease/build identifier may be empty. Ordering itself is overflow-proof (compareNumericString); this rejects
// malformed spellings such as v1.0.0-01 or v1.0.0-. up front so they never enter the catalog.
func validateVersionStructure(v string) error {
	main := strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexByte(main, '+'); i >= 0 {
		if err := checkDotIdentifiers(main[i+1:], false); err != nil {
			return fmt.Errorf("build metadata %w", err)
		}
		main = main[:i]
	}
	if i := strings.IndexByte(main, '-'); i >= 0 {
		if err := checkDotIdentifiers(main[i+1:], true); err != nil {
			return fmt.Errorf("prerelease %w", err)
		}
		main = main[:i]
	}
	for _, part := range strings.SplitN(main, ".", 3) {
		if len(part) > 1 && part[0] == '0' {
			return fmt.Errorf("version core component %q has a leading zero", part)
		}
	}
	return nil
}

// checkDotIdentifiers validates dot-separated SemVer identifiers: none may be empty, and (when numericRules is set, for
// a prerelease) no numeric run may carry a leading zero — whether the whole identifier is numeric (01) OR it is the
// trailing numeric run of an alphanumeric identifier (rc01). The latter matters because our comparison orders combined
// forms by their trailing number, so rc01 would otherwise compare equal to rc1 despite being a distinct identifier.
func checkDotIdentifiers(s string, numericRules bool) error {
	if s == "" {
		return fmt.Errorf("is empty")
	}
	for _, id := range strings.Split(s, ".") {
		if id == "" {
			return fmt.Errorf("has an empty identifier")
		}
		if numericRules {
			if _, digits, has := splitTrailingDigits(id); has && len(digits) > 1 && digits[0] == '0' {
				return fmt.Errorf("numeric run in identifier %q has a leading zero", id)
			}
		}
	}
	return nil
}
