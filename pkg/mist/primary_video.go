package mist

import (
	"strings"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// VideoTrackFacts are the track properties that decide which video track
// describes a stream's resolution. Height and TrackIndex are 0 and -1 when
// Mist did not report them.
type VideoTrackFacts struct {
	TrackType   string
	Codec       string
	SourceTrack string
	Height      int32
	TrackIndex  int32
}

// IsImageVideoCodec reports whether a video-typed track carries still images
// (thumbnail sprites, previews) rather than continuous video.
func IsImageVideoCodec(codec string) bool {
	switch strings.ToLower(strings.TrimSpace(codec)) {
	case "jpeg", "mjpeg", "png":
		return true
	}
	return false
}

// PrimaryVideoIndex picks the track that describes the stream's resolution:
// a continuous-video track, preferring original tracks over process outputs
// (renditions carry a source track), then the largest known height, then the
// lowest Mist track index. Mist reports tracks as a JSON object, so input
// order carries no meaning. It returns -1 when no continuous-video track
// exists. A picked track may still have an unknown height.
func PrimaryVideoIndex(n int, facts func(i int) VideoTrackFacts) int {
	best := -1
	var bestFacts VideoTrackFacts
	for i := 0; i < n; i++ {
		f := facts(i)
		if !strings.EqualFold(strings.TrimSpace(f.TrackType), "video") || IsImageVideoCodec(f.Codec) {
			continue
		}
		if best < 0 || preferPrimaryVideo(f, bestFacts) {
			best, bestFacts = i, f
		}
	}
	return best
}

func preferPrimaryVideo(a, b VideoTrackFacts) bool {
	aOriginal, bOriginal := a.SourceTrack == "", b.SourceTrack == ""
	if aOriginal != bOriginal {
		return aOriginal
	}
	if a.Height != b.Height {
		return a.Height > b.Height
	}
	if a.TrackIndex >= 0 && (b.TrackIndex < 0 || a.TrackIndex < b.TrackIndex) {
		return true
	}
	return false
}

// PrimaryVideoTrack applies PrimaryVideoIndex to parsed trigger tracks.
func PrimaryVideoTrack(tracks []*ipcpb.StreamTrack) *ipcpb.StreamTrack {
	idx := PrimaryVideoIndex(len(tracks), func(i int) VideoTrackFacts {
		t := tracks[i]
		index := int32(-1)
		if t.TrackIndex != nil {
			index = t.GetTrackIndex()
		}
		return VideoTrackFacts{
			TrackType:   t.GetTrackType(),
			Codec:       t.GetCodec(),
			SourceTrack: t.GetSourceTrack(),
			Height:      t.GetHeight(),
			TrackIndex:  index,
		}
	})
	if idx < 0 {
		return nil
	}
	return tracks[idx]
}
