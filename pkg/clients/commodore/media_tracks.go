package commodore

import (
	"encoding/json"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

// mediaTrackJSON is the JSONB storage shape of a MediaTrack, shared by the Foghorn
// side that persists the finalized track set to foghorn.artifacts.tracks and the
// reconciler that reads it back to re-project. Field names match the commodore
// catalog projection and the GraphQL ArtifactTrack camelCase.
type mediaTrackJSON struct {
	Type        string   `json:"type"`
	Codec       string   `json:"codec,omitempty"`
	Width       *int32   `json:"width,omitempty"`
	Height      *int32   `json:"height,omitempty"`
	Fps         *float64 `json:"fps,omitempty"`
	Resolution  *string  `json:"resolution,omitempty"`
	BitrateKbps *int32   `json:"bitrateKbps,omitempty"`
	Channels    *int32   `json:"channels,omitempty"`
	SampleRate  *int32   `json:"sampleRate,omitempty"`
}

// MarshalMediaTracks serializes a MediaTrack slice to the JSONB storage shape.
func MarshalMediaTracks(tracks []*commodorepb.MediaTrack) ([]byte, error) {
	rows := make([]mediaTrackJSON, 0, len(tracks))
	for _, t := range tracks {
		if t == nil {
			continue
		}
		rows = append(rows, mediaTrackJSON{
			Type: t.GetType(), Codec: t.GetCodec(), Width: t.Width, Height: t.Height,
			Fps: t.Fps, Resolution: t.Resolution, BitrateKbps: t.BitrateKbps,
			Channels: t.Channels, SampleRate: t.SampleRate,
		})
	}
	return json.Marshal(rows)
}

// UnmarshalMediaTracks parses the JSONB storage shape back into MediaTracks.
// Returns an error on malformed JSON so callers can fail closed.
func UnmarshalMediaTracks(raw []byte) ([]*commodorepb.MediaTrack, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var rows []mediaTrackJSON
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	out := make([]*commodorepb.MediaTrack, 0, len(rows))
	for _, r := range rows {
		out = append(out, &commodorepb.MediaTrack{
			Type: r.Type, Codec: r.Codec, Width: r.Width, Height: r.Height,
			Fps: r.Fps, Resolution: r.Resolution, BitrateKbps: r.BitrateKbps,
			Channels: r.Channels, SampleRate: r.SampleRate,
		})
	}
	return out, nil
}
