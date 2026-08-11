// Package catalogview holds neutral presentation helpers over the canonical storage catalog
// (commodore.StorageArtifactInfo), shared by the GraphQL resolver and the MCP resource so neither
// depends on the other. It carries no GraphQL/MCP types — only plain derivations.
package catalogview

import commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"

// TrackSummary derives the flat resolution/codec/bitrate summary the VOD surfaces expose from the
// catalog's per-track array: the first video track supplies resolution + video codec + bitrate,
// the first audio track supplies the audio codec. Absent fields stay nil.
func TrackSummary(tracks []*commodorepb.MediaTrack) (resolution, videoCodec, audioCodec *string, bitrateKbps *int) {
	var haveVideo, haveAudio bool
	for _, t := range tracks {
		switch t.GetType() {
		case "video":
			if haveVideo {
				continue
			}
			haveVideo = true
			if v := t.GetResolution(); v != "" {
				vv := v
				resolution = &vv
			}
			if v := t.GetCodec(); v != "" {
				vv := v
				videoCodec = &vv
			}
			if t.BitrateKbps != nil {
				b := int(t.GetBitrateKbps())
				bitrateKbps = &b
			}
		case "audio":
			if haveAudio {
				continue
			}
			haveAudio = true
			if v := t.GetCodec(); v != "" {
				vv := v
				audioCodec = &vv
			}
		}
	}
	return resolution, videoCodec, audioCodec, bitrateKbps
}
