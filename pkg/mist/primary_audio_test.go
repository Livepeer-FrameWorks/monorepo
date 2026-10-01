package mist

import (
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

func TestPrimaryAudioIndex(t *testing.T) {
	video := AudioTrackFacts{TrackType: "video", TrackIndex: 0}
	source := AudioTrackFacts{TrackType: "audio", TrackIndex: 1}
	opus := AudioTrackFacts{TrackType: "audio", SourceTrack: "1", TrackIndex: 3}
	secondSource := AudioTrackFacts{TrackType: "audio", TrackIndex: 2}
	unindexedA := AudioTrackFacts{TrackType: "audio", TrackIndex: -1}
	unindexedB := AudioTrackFacts{TrackType: "audio", TrackIndex: -1}

	cases := []struct {
		name   string
		tracks []AudioTrackFacts
		want   int
	}{
		{"no tracks", nil, -1},
		{"video only", []AudioTrackFacts{video}, -1},
		{"source after its transcode", []AudioTrackFacts{video, opus, source}, 2},
		{"transcode when no original audio exists", []AudioTrackFacts{video, opus}, 1},
		{"lowest track index among originals", []AudioTrackFacts{secondSource, source}, 1},
		{"known track index over unknown", []AudioTrackFacts{unindexedA, source}, 1},
		{"input order when no track index is known", []AudioTrackFacts{unindexedA, unindexedB}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PrimaryAudioIndex(len(tc.tracks), func(i int) AudioTrackFacts { return tc.tracks[i] })
			if got != tc.want {
				t.Fatalf("PrimaryAudioIndex = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestPrimaryAudioTrack(t *testing.T) {
	opus := &ipcpb.StreamTrack{TrackName: "opus", TrackType: "audio", Codec: "opus", TrackIndex: proto.Int32(3), SourceTrack: proto.String("1")}
	aac := &ipcpb.StreamTrack{TrackName: "aac", TrackType: "audio", Codec: "AAC", TrackIndex: proto.Int32(1)}
	video := &ipcpb.StreamTrack{TrackName: "video", TrackType: "video", Codec: "H264", TrackIndex: proto.Int32(0)}
	if got := PrimaryAudioTrack([]*ipcpb.StreamTrack{opus, video, aac}); got != aac {
		t.Fatalf("PrimaryAudioTrack = %v, want aac", got.GetTrackName())
	}
	if got := PrimaryAudioTrack([]*ipcpb.StreamTrack{video}); got != nil {
		t.Fatalf("PrimaryAudioTrack = %v, want nil", got.GetTrackName())
	}
}
