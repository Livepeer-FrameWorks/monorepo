// Package artifacts defines neutral types for config-drift comparison.
package artifacts

// TargetID identifies one compare site. Display is the operator-facing
// label, Deploy is the service name passed to the detector, Role
// distinguishes instances or roles that share a Deploy family name.
type TargetID struct {
	Host    string
	Display string
	Deploy  string
	Role    string
}

// ArtifactKind selects how a DesiredArtifact is compared.
type ArtifactKind int

const (
	// KindFileHash compares whole-file bytes via SHA256.
	KindFileHash ArtifactKind = iota
	// KindEnv parses both sides as key=value and diffs at the key level.
	KindEnv
	// KindManagedInvariant asserts substrings exist in a file the
	// provisioner only partially owns. Content outside the invariants
	// is not asserted.
	KindManagedInvariant
)

// Invariant is the assertion set for a KindManagedInvariant artifact.
type Invariant struct {
	MustContain [][]byte
}

// DesiredArtifact is one file claimed as desired state on a host. Content
// is used for KindFileHash and KindEnv; Invariant is used for
// KindManagedInvariant. IgnoreKeys applies only to KindEnv: listed keys
// are dropped from both sides before diffing, so runtime-injected values
// are not reported as drift.
type DesiredArtifact struct {
	Path       string
	Kind       ArtifactKind
	Content    []byte
	Invariant  *Invariant
	IgnoreKeys []string
}

// ConfigDiffStatus is the per-artifact outcome.
type ConfigDiffStatus int

const (
	StatusMatch ConfigDiffStatus = iota
	StatusDiffer
	// StatusMissingOnHost is confirmed absence, distinct from StatusProbeError.
	StatusMissingOnHost
	// StatusProbeError means the fetch failed (transport, permission).
	StatusProbeError
)

// EnvDiff carries key-level deltas. There is no value field by design:
// JSON serialization cannot leak a secret.
type EnvDiff struct {
	Added   []string
	Removed []string
	Changed []string
}

// HasDifferences reports whether any keys differ.
func (d EnvDiff) HasDifferences() bool {
	return len(d.Added) > 0 || len(d.Removed) > 0 || len(d.Changed) > 0
}

// ConfigDiffEntry is one artifact's result. Detail carries probe-error
// context or the specific missing invariant; it never contains decoded
// file content.
type ConfigDiffEntry struct {
	Path   string
	Kind   ArtifactKind
	Status ConfigDiffStatus
	Detail string
	Env    *EnvDiff
}

// ConfigDiff aggregates entries for one target.
type ConfigDiff struct {
	Target  TargetID
	Entries []ConfigDiffEntry
}

// Divergences counts entries that are not StatusMatch. Probe errors count
// so operators notice probe failures rather than treating them as ok.
func (d ConfigDiff) Divergences() int {
	n := 0
	for _, e := range d.Entries {
		if e.Status != StatusMatch {
			n++
		}
	}
	return n
}
