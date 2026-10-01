// Package catalogview holds neutral presentation helpers over the canonical storage catalog
// (commodore.StorageArtifactInfo), shared by the GraphQL resolver and the MCP resource so neither
// depends on the other. It carries no GraphQL/MCP types — only plain derivations.
package catalogview

import (
	"strconv"
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

// TrackSummary derives the flat resolution/codec/bitrate summary the VOD surfaces expose from the
// catalog's per-track array. The primary video track (mist.PrimaryVideoIndex: continuous video,
// originals over renditions, then the largest height) supplies resolution + video codec +
// bitrate; the primary audio track (mist.PrimaryAudioIndex: originals over transcodes) supplies
// the audio codec. Thumbnail sprites and previews never count as video. Absent fields stay nil.
// Catalog tracks carry no Mist track index, so remaining ties resolve to catalog order.
func TrackSummary(tracks []*commodorepb.MediaTrack) (resolution, videoCodec, audioCodec *string, bitrateKbps *int) {
	if i := mist.PrimaryVideoIndex(len(tracks), func(i int) mist.VideoTrackFacts {
		t := tracks[i]
		return mist.VideoTrackFacts{
			TrackType:   t.GetType(),
			Codec:       t.GetCodec(),
			SourceTrack: t.GetSourceTrack(),
			Height:      trackHeight(t),
			TrackIndex:  -1,
		}
	}); i >= 0 {
		t := tracks[i]
		if v := t.GetResolution(); v != "" {
			resolution = &v
		}
		if v := t.GetCodec(); v != "" {
			videoCodec = &v
		}
		if t.BitrateKbps != nil {
			b := int(t.GetBitrateKbps())
			bitrateKbps = &b
		}
	}
	if i := mist.PrimaryAudioIndex(len(tracks), func(i int) mist.AudioTrackFacts {
		t := tracks[i]
		return mist.AudioTrackFacts{
			TrackType:   t.GetType(),
			SourceTrack: t.GetSourceTrack(),
			TrackIndex:  -1,
		}
	}); i >= 0 {
		if v := tracks[i].GetCodec(); v != "" {
			audioCodec = &v
		}
	}
	return resolution, videoCodec, audioCodec, bitrateKbps
}

// trackHeight returns the track's height, falling back to the "WxH" resolution string; 0 when
// neither is known.
func trackHeight(t *commodorepb.MediaTrack) int32 {
	if h := t.GetHeight(); h > 0 {
		return h
	}
	_, h, ok := strings.Cut(t.GetResolution(), "x")
	if !ok {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(h), 10, 32)
	if err != nil || n < 0 {
		return 0
	}
	return int32(n)
}
