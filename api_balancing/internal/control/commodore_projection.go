package control

import (
	"math"

	commodoreclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// marshalRecordingTracks maps the accepted A/V track set (from a completion-validated
// ProcessingJobResult) to the durable MediaTrack JSON stored on foghorn.artifacts.tracks. It
// ALWAYS returns a valid JSON array ("[]" for an empty set), never nil: whether to replace the
// stored summary is decided by the result's tracks_present bit at the call site, not by
// emptiness here — so an authoritative empty set clears prior tracks while a track-less result
// (present=false) leaves them untouched. The authoritative capture point is the processing-
// result path (keyed by the already-resolved artifact_hash) — NOT the raw RECORDING_END
// trigger, which fires before Helmsman accepts the job. The artifact reconciler is the sole
// writer that then projects tracks onto the catalog.
func marshalRecordingTracks(tracks []*ipcpb.StreamTrack) (string, error) {
	body, err := commodoreclient.MarshalMediaTracks(mapStreamTracks(tracks))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// mapStreamTracks translates the wire StreamTrack summary into the commodore
// MediaTrack projection, keeping only the durable A/V descriptors (codec/geometry/
// rates) and dropping transient timing/jitter metrics. fps is the only float; a non-finite value
// (NaN/±Inf) is dropped so JSON serialization can't fail and lose the authoritative track set.
func mapStreamTracks(tracks []*ipcpb.StreamTrack) []*commodorepb.MediaTrack {
	out := make([]*commodorepb.MediaTrack, 0, len(tracks))
	for _, t := range tracks {
		if t == nil {
			continue
		}
		out = append(out, &commodorepb.MediaTrack{
			Type:        t.GetTrackType(),
			Codec:       t.GetCodec(),
			Width:       t.Width,
			Height:      t.Height,
			Fps:         finiteFloatOrNil(t.Fps),
			Resolution:  t.Resolution,
			BitrateKbps: t.BitrateKbps,
			Channels:    t.Channels,
			SampleRate:  t.SampleRate,
		})
	}
	return out
}

// finiteFloatOrNil drops a non-finite float (NaN/±Inf) — encoding/json rejects those, which would
// otherwise fail track serialization and drop the whole authoritative set at completion time.
func finiteFloatOrNil(f *float64) *float64 {
	if f == nil || math.IsNaN(*f) || math.IsInf(*f, 0) {
		return nil
	}
	return f
}
