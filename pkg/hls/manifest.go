// Package hls builds and parses HLS m3u8 manifests for FrameWorks.
//
// Used by:
//   - DVR rolling manifests: Mist writes these directly during recording;
//     helpers here parse them on the sidecar's reconciliation backstop
//     (RECORDING_SEGMENT missed-trigger recovery) and on cross-node
//     reconstruction during DTSC pull.
//   - Chapter finalize temp HLS: the finalization queue stages a one-shot
//     VOD playlist that Mist's input_hls reads to remux source TS into
//     the canonical chapter .mkv. This playlist is not a public artifact;
//     it lives under storage/processing/ and is removed after the job.
//
// The package no longer emits chapter manifests as public artifacts —
// chapter playback resolves to a canonical .mkv VOD artifact, not an
// HLS playlist. BuildVOD's #EXT-X-GAP handling remains for the
// reconstruction case where a single lost segment shouldn't break the
// rolling playlist.
package hls

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Manifest is the parsed result of an .m3u8 file.
type Manifest struct {
	TargetDuration int
	// MediaSequence is the value of #EXT-X-MEDIA-SEQUENCE (0 when absent).
	// For rolling live manifests, MediaSequence > 0 means the first parsed
	// segment is NOT the first segment of the recording — readers must not
	// assume `Segments[0]` is at media time 0.
	MediaSequence int64
	Segments      []Segment
}

// Segment is one parsed entry from an HLS manifest.
type Segment struct {
	Name              string
	Duration          float64
	ProgramDateTimeMs int64
}

// FinalSegment is one row from the durable DVR segment ledger, in the shape
// internal HLS rebuilders consume (chapter finalization temp playlist,
// rolling-DVR reconstruction from the segment pool). Sequence/media times
// are needed for ordering and for inserting #EXT-X-DISCONTINUITY at
// media-time jumps.
type FinalSegment struct {
	// Name is the segment filename written into the playlist when URI is
	// empty.
	Name string
	// URI overrides Name + SegmentURIPrefix when a caller needs a fully
	// materialized segment URI, such as a player-facing manifest with presigned
	// segment URLs.
	URI string
	// Sequence is the ledger sequence used for stable ordering when two
	// segments share the same media_start_ms.
	Sequence int64
	// DurationMs is the segment's playback duration in milliseconds.
	DurationMs int64
	// MediaStartMs / MediaEndMs are the segment's media-time bounds; used
	// to detect playback discontinuities that warrant #EXT-X-DISCONTINUITY.
	MediaStartMs int64
	MediaEndMs   int64
	// ProgramDateTimeMs renders #EXT-X-PROGRAM-DATE-TIME before this segment
	// when an absolute UTC media timestamp is available.
	ProgramDateTimeMs int64
	// Lost is true for segments whose local copy was evicted before upload
	// (status=lost_local in the ledger). The VOD manifest renders these as
	// #EXT-X-GAP + #EXTINF so the timeline duration is preserved without
	// pretending the bytes are reachable.
	Lost bool
}

// BuildLive returns the header for a fresh live manifest with no segments
// yet appended. AppendSegment writes #EXTINF entries; Finalize closes it.
func BuildLive(targetDurationSeconds int) string {
	if targetDurationSeconds <= 0 {
		targetDurationSeconds = 6
	}
	return fmt.Sprintf(`#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:%d
#EXT-X-MEDIA-SEQUENCE:0
#EXT-X-PLAYLIST-TYPE:EVENT
`, targetDurationSeconds)
}

// AppendSegment adds a single #EXTINF + segment URI to an existing manifest
// file on disk. The segment URI is rendered relative to a "segments/" subdir
// to match the layout Mist writes during DVR.
func AppendSegment(manifestPath, segmentName string, duration float64) error {
	f, err := os.OpenFile(manifestPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open manifest for append: %w", err)
	}
	defer func() { _ = f.Close() }()
	_, err = fmt.Fprintf(f, "#EXTINF:%.3f,\nsegments/%s\n", duration, segmentName)
	if err != nil {
		return fmt.Errorf("write segment entry: %w", err)
	}
	return nil
}

// Finalize appends #EXT-X-ENDLIST to close a live manifest as VOD.
func Finalize(manifestPath string) error {
	f, err := os.OpenFile(manifestPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open manifest for finalize: %w", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString("#EXT-X-ENDLIST\n"); err != nil {
		return fmt.Errorf("write ENDLIST: %w", err)
	}
	return nil
}

// Parse extracts segments and the target duration from an HLS manifest body.
// It is forgiving: missing target duration defaults to 6s, comments and
// unrecognized tags are skipped, and segment URIs may be either bare names
// or "segments/foo.ts" paths.
func Parse(content string) (*Manifest, error) {
	if content == "" {
		return nil, errors.New("empty manifest")
	}
	result := &Manifest{TargetDuration: 6}

	var pendingDuration float64
	var pendingProgramDateTimeMs int64
	for line := range strings.SplitSeq(content, "\n") {
		line = strings.TrimSpace(line)
		if val, ok := strings.CutPrefix(line, "#EXT-X-TARGETDURATION:"); ok {
			if d, err := strconv.Atoi(strings.TrimSpace(val)); err == nil {
				result.TargetDuration = d
			}
			continue
		}
		if val, ok := strings.CutPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"); ok {
			if n, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64); err == nil {
				result.MediaSequence = n
			}
			continue
		}
		if val, ok := strings.CutPrefix(line, "#EXT-X-PROGRAM-DATE-TIME:"); ok {
			if t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(val)); err == nil {
				pendingProgramDateTimeMs = t.UnixMilli()
			}
			continue
		}
		if val, ok := strings.CutPrefix(line, "#EXTINF:"); ok {
			val = strings.Split(val, ",")[0]
			if d, err := strconv.ParseFloat(strings.TrimSpace(val), 64); err == nil {
				pendingDuration = d
			}
			continue
		}
		if strings.HasPrefix(line, "#") || line == "" || pendingDuration <= 0 {
			continue
		}
		segName := filepath.Base(line)
		if idx := strings.Index(segName, "?"); idx > 0 {
			segName = segName[:idx]
		}
		result.Segments = append(result.Segments, Segment{
			Name:              segName,
			Duration:          pendingDuration,
			ProgramDateTimeMs: pendingProgramDateTimeMs,
		})
		pendingDuration = 0
		pendingProgramDateTimeMs = 0
	}
	return result, nil
}

// BuildVODOptions controls VOD manifest rendering.
type BuildVODOptions struct {
	// TargetDurationSeconds for #EXT-X-TARGETDURATION. 0 picks the max
	// observed segment duration, rounded up to the nearest second.
	TargetDurationSeconds int
	// DiscontinuityThresholdMs is how large a media-time gap between adjacent
	// segments must be before the builder inserts #EXT-X-DISCONTINUITY before
	// the next segment. 0 picks 1.5x the segment's own duration. Lost segment
	// rows do not themselves trigger discontinuity — they preserve duration.
	DiscontinuityThresholdMs int64
	// HasGaps forces #EXT-X-VERSION:8 even when no segment carries Lost=true.
	// Caller sets this when the ledger is known to have gaps but the slice
	// passed in happens not to include any (e.g. the gap segments fell
	// outside the requested range).
	HasGaps bool
	// Event renders #EXT-X-PLAYLIST-TYPE:EVENT and omits #EXT-X-ENDLIST.
	// The rolling DVR manifest uses this shape while ingest is running.
	Event bool
	// SegmentURIPrefix is prepended to each segment's name in the manifest.
	// Default "segments/" matches the rolling-DVR manifest written at the
	// artifact root (dvr/{artifact}/{name}.m3u8) where segments live in a
	// sibling segments/ directory. Trailing slash required.
	SegmentURIPrefix string
}

// BuildVOD renders an HLS manifest from a ledger-ordered slice of segments
// (sorted by media_start_ms, sequence). Used by chapter-finalization temp
// playlists (VOD shape) and the rolling-DVR reconstructor (EVENT shape via
// BuildVODOptions.Event, which omits #EXT-X-ENDLIST so the manifest stays
// open while ingest continues).
//
//   - Lost rows render as #EXT-X-GAP + #EXTINF (preserves timeline duration;
//     player skips media but the seek bar stays correct).
//
//   - Media-time discontinuities greater than the threshold insert
//     #EXT-X-DISCONTINUITY before the next real segment without inventing a
//     phantom row for that gap.
//
//   - The manifest closes with #EXT-X-ENDLIST unless Event is set.
//
// Segment URIs are rendered with SegmentURIPrefix unless the segment carries
// an explicit URI. Player-facing manifests use explicit presigned URIs.
func BuildVOD(segments []FinalSegment, opts BuildVODOptions) string {
	target := opts.TargetDurationSeconds
	if target <= 0 {
		var maxMs int64
		for _, s := range segments {
			maxMs = max(maxMs, s.DurationMs)
		}
		target = int((maxMs + 999) / 1000)
		if target <= 0 {
			target = 6
		}
	}

	// HLS protocol version: v8 minimum when #EXT-X-GAP is in play (Apple HLS
	// spec, version 8 introduced GAP). Default v6 keeps clean manifests
	// compatible with older players.
	version := 6
	hasGap := opts.HasGaps
	if !hasGap {
		for _, s := range segments {
			if s.Lost {
				hasGap = true
				break
			}
		}
	}
	if hasGap {
		version = 8
	}

	prefix := opts.SegmentURIPrefix
	if prefix == "" {
		prefix = "segments/"
	}
	var mediaSequence int64
	if len(segments) > 0 {
		mediaSequence = segments[0].Sequence
	}

	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	fmt.Fprintf(&b, "#EXT-X-VERSION:%d\n", version)
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", target)
	fmt.Fprintf(&b, "#EXT-X-MEDIA-SEQUENCE:%d\n", mediaSequence)
	if opts.Event {
		b.WriteString("#EXT-X-PLAYLIST-TYPE:EVENT\n")
	} else {
		b.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
	}

	var prevEnd int64 = -1
	for _, s := range segments {
		threshold := opts.DiscontinuityThresholdMs
		if threshold <= 0 {
			threshold = max((s.DurationMs*3)/2, 1000)
		}
		if prevEnd >= 0 && s.MediaStartMs-prevEnd > threshold {
			b.WriteString("#EXT-X-DISCONTINUITY\n")
		}

		dur := float64(s.DurationMs) / 1000.0
		if s.ProgramDateTimeMs > 0 {
			fmt.Fprintf(&b, "#EXT-X-PROGRAM-DATE-TIME:%s\n", time.UnixMilli(s.ProgramDateTimeMs).UTC().Format("2006-01-02T15:04:05.000Z"))
		}
		if s.Lost {
			b.WriteString("#EXT-X-GAP\n")
		}
		fmt.Fprintf(&b, "#EXTINF:%.3f,\n", dur)
		uri := s.URI
		if uri == "" {
			uri = prefix + s.Name
		}
		fmt.Fprintf(&b, "%s\n", uri)

		prevEnd = s.MediaEndMs
	}

	if !opts.Event {
		b.WriteString("#EXT-X-ENDLIST\n")
	}
	return b.String()
}
