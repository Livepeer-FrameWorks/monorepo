package mist

import (
	"strings"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// AudioTrackFacts are the track properties that decide which audio track
// describes a stream's audio. TrackIndex is -1 when Mist did not report it.
type AudioTrackFacts struct {
	TrackType   string
	SourceTrack string
	TrackIndex  int32
}

// PrimaryAudioIndex picks the track that describes the stream's audio: an
// audio track, preferring original tracks over process outputs (transcodes
// such as an Opus copy of an AAC source carry a source track), then the
// lowest known Mist track index, then the earliest input position. It returns
// -1 when no audio track exists.
func PrimaryAudioIndex(n int, facts func(i int) AudioTrackFacts) int {
	best := -1
	var bestFacts AudioTrackFacts
	for i := 0; i < n; i++ {
		f := facts(i)
		if !strings.EqualFold(strings.TrimSpace(f.TrackType), "audio") {
			continue
		}
		if best < 0 || preferPrimaryAudio(f, bestFacts) {
			best, bestFacts = i, f
		}
	}
	return best
}

func preferPrimaryAudio(a, b AudioTrackFacts) bool {
	aOriginal, bOriginal := a.SourceTrack == "", b.SourceTrack == ""
	if aOriginal != bOriginal {
		return aOriginal
	}
	return a.TrackIndex >= 0 && (b.TrackIndex < 0 || a.TrackIndex < b.TrackIndex)
}

// PrimaryAudioTrack applies PrimaryAudioIndex to parsed trigger tracks.
func PrimaryAudioTrack(tracks []*ipcpb.StreamTrack) *ipcpb.StreamTrack {
	idx := PrimaryAudioIndex(len(tracks), func(i int) AudioTrackFacts {
		t := tracks[i]
		index := int32(-1)
		if t.TrackIndex != nil {
			index = t.GetTrackIndex()
		}
		return AudioTrackFacts{
			TrackType:   t.GetTrackType(),
			SourceTrack: t.GetSourceTrack(),
			TrackIndex:  index,
		}
	})
	if idx < 0 {
		return nil
	}
	return tracks[idx]
}
