// Package streamident parses Mist runtime stream names into a typed result.
//
// A runtime stream name is the name Mist itself uses to route a stream. It
// either carries one of the well-known FrameWorks prefixes (live+, pull+,
// vod+, dvr+, processing+) or is bare. Bare names are not self-describing —
// they can be a mist-native concrete internal_name, an unprefixed artifact
// key, or a leaked playback_id. Resolution is the caller's job; this
// package only classifies and unprefixes.
package streamident

import "strings"

// Kind classifies a Mist runtime stream name.
type Kind int

const (
	// KindBare is an unprefixed name. Could be a mist-native concrete
	// internal_name, an unprefixed artifact key, or a leaked playback_id.
	// Only the caller's registry/resolver can decide which.
	KindBare Kind = iota
	// KindSourceLive is live+<internal_name> — push ingest source stream.
	KindSourceLive
	// KindSourcePull is pull+<internal_name> — pull ingest source stream.
	KindSourcePull
	// KindArtifactVOD is vod+<artifact_internal_name>.
	KindArtifactVOD
	// KindArtifactDVR is dvr+<dvr_artifact_internal_name>. The rolling
	// surface for an actively-recording stream. Chapter playback resolves
	// through Commodore via the chapter VOD artifact's playback_id and
	// never produces a dvr+ runtime name.
	KindArtifactDVR
	// KindArtifactProcessing is processing+<artifact_hash>. Note the
	// concrete token is the artifact hash, not the artifact internal_name.
	KindArtifactProcessing
)

// prefixes is the canonical list of FrameWorks Mist runtime prefixes.
// New prefixes get added here and only here.
var prefixes = []struct {
	prefix string
	kind   Kind
}{
	{"live+", KindSourceLive},
	{"pull+", KindSourcePull},
	{"vod+", KindArtifactVOD},
	{"dvr+", KindArtifactDVR},
	{"processing+", KindArtifactProcessing},
}

// String returns the human-readable kind name.
func (k Kind) String() string {
	switch k {
	case KindBare:
		return "bare"
	case KindSourceLive:
		return "source_live"
	case KindSourcePull:
		return "source_pull"
	case KindArtifactVOD:
		return "artifact_vod"
	case KindArtifactDVR:
		return "artifact_dvr"
	case KindArtifactProcessing:
		return "artifact_processing"
	default:
		return "invalid"
	}
}

// Prefix returns the Mist routing prefix for a kind, or "" for KindBare.
func (k Kind) Prefix() string {
	for _, p := range prefixes {
		if p.kind == k {
			return p.prefix
		}
	}
	return ""
}

// IsSource reports whether the kind is a known source-stream prefix.
// KindBare returns false because the parser cannot confirm a bare name is
// a source — the registry must.
func (k Kind) IsSource() bool {
	return k == KindSourceLive || k == KindSourcePull
}

// IsArtifact reports whether the kind is one of the artifact prefixes.
func (k Kind) IsArtifact() bool {
	return k == KindArtifactVOD || k == KindArtifactDVR || k == KindArtifactProcessing
}

// Parsed is the result of parsing a Mist runtime stream name.
type Parsed struct {
	// Kind classifies the prefix (or KindBare for unprefixed input).
	Kind Kind
	// Concrete is the token after stripping the prefix. For KindBare it
	// equals Original. The semantic of Concrete depends on Kind:
	//   KindSourceLive, KindSourcePull → source stream internal_name
	//   KindArtifactVOD, KindArtifactDVR → artifact internal_name
	//   KindArtifactProcessing → artifact hash
	//   KindBare → undefined; resolver decides
	Concrete string
	// Original is the input string, unmodified.
	Original string
}

// IsSource is shorthand for p.Kind.IsSource().
func (p Parsed) IsSource() bool { return p.Kind.IsSource() }

// IsArtifact is shorthand for p.Kind.IsArtifact().
func (p Parsed) IsArtifact() bool { return p.Kind.IsArtifact() }

// Parse classifies a Mist runtime stream name. It never returns an error;
// unrecognized input becomes KindBare with Concrete == Original.
func Parse(runtimeName string) Parsed {
	for _, p := range prefixes {
		if rest, ok := strings.CutPrefix(runtimeName, p.prefix); ok {
			return Parsed{Kind: p.kind, Concrete: rest, Original: runtimeName}
		}
	}
	return Parsed{Kind: KindBare, Concrete: runtimeName, Original: runtimeName}
}
