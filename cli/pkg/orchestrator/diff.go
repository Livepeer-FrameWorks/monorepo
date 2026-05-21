package orchestrator

import (
	"slices"
	"sort"

	"frameworks/cli/pkg/detect"
)

// DiffKind is the typed change-kind a host needs from a `cluster apply` run.
// DiffUnknown is the explicit fall-through: any service whose provisioner
// did not register a fingerprinter, or whose diff includes something we
// don't model, lands here. `cluster apply` refuses to touch it and points
// operators at `cluster provision`.
type DiffKind string

const (
	DiffBinary  DiffKind = "binary"
	DiffEnv     DiffKind = "env"
	DiffUnit    DiffKind = "unit"
	DiffCert    DiffKind = "cert"
	DiffInfra   DiffKind = "infra"
	DiffUnknown DiffKind = "unknown"
)

// HostDiff is the per-host classification for one service. Kinds is empty
// when desired and observed agree across every modeled FileKind — that's
// the no-op fast path the operator wants 99% of the time. Details holds
// human-readable evidence per kind: usually "<path>: expected <hash>, got
// <hash|missing>".
type HostDiff struct {
	Host    string
	Service string
	Kinds   []DiffKind
	Details map[DiffKind]string
}

// HasKind reports whether the diff contains a specific kind.
func (d *HostDiff) HasKind(k DiffKind) bool {
	return slices.Contains(d.Kinds, k)
}

// fileKindToDiffKind maps the file-level FileKind onto the higher-level
// DiffKind a rolling-update strategy reasons about.
var fileKindToDiffKind = map[detect.FileKind]DiffKind{
	detect.FileKindBinary: DiffBinary,
	detect.FileKindEnv:    DiffEnv,
	detect.FileKindUnit:   DiffUnit,
	detect.FileKindCert:   DiffCert,
}

// Classify compares the desired Fingerprint against observed sha256 results
// (keyed by absolute file path) and returns the typed diff for a single
// host/service.
//
// Rules:
//   - desired == nil: the service did not register a fingerprinter, so we
//     can't reason about what should be on disk. Return DiffUnknown.
//   - For each modeled (kind, expected) pair: if observed hash differs from
//     expected, append the corresponding DiffKind. Missing files diff just
//     like wrong hashes.
//   - Expected SHA256 == "" means "no expectation for this file" — skipped.
//   - An empty Kinds slice means the host is in sync for every modeled kind.
func Classify(serviceName, hostName string, desired *detect.Fingerprint, observed map[string]string) HostDiff {
	out := HostDiff{
		Host:    hostName,
		Service: serviceName,
		Details: map[DiffKind]string{},
	}

	if desired == nil || len(desired.Files) == 0 {
		out.Kinds = []DiffKind{DiffUnknown}
		out.Details[DiffUnknown] = "service has no fingerprinter registered"
		return out
	}

	// Iterate FileKinds in stable order so output (and tests) are deterministic.
	kinds := make([]detect.FileKind, 0, len(desired.Files))
	for k := range desired.Files {
		kinds = append(kinds, k)
	}
	sort.Slice(kinds, func(i, j int) bool { return string(kinds[i]) < string(kinds[j]) })

	for _, fk := range kinds {
		expected := desired.Files[fk]
		if expected.SHA256 == "" {
			continue
		}
		dk, ok := fileKindToDiffKind[fk]
		if !ok {
			// Unmodeled FileKind in the desired set — explicit unknown so we
			// fall through to cluster provision rather than guess.
			out.Kinds = append(out.Kinds, DiffUnknown)
			out.Details[DiffUnknown] = string(fk) + ": no DiffKind mapping"
			continue
		}
		got, present := observed[expected.Path]
		if !present || got == "" {
			out.Kinds = append(out.Kinds, dk)
			out.Details[dk] = expected.Path + ": missing on host (expected " + short(expected.SHA256) + ")"
			continue
		}
		if got != expected.SHA256 {
			out.Kinds = append(out.Kinds, dk)
			out.Details[dk] = expected.Path + ": expected " + short(expected.SHA256) + ", got " + short(got)
		}
	}
	return out
}

func short(h string) string {
	if len(h) <= 12 {
		return h
	}
	return h[:12]
}
