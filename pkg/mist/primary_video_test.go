package mist

import (
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

func TestPrimaryVideoTrack(t *testing.T) {
	source := &ipcpb.StreamTrack{TrackName: "source", TrackType: "video", Codec: "H264", TrackIndex: proto.Int32(0), Height: proto.Int32(1080)}
	bootingSource := &ipcpb.StreamTrack{TrackName: "booting-source", TrackType: "video", Codec: "H264", TrackIndex: proto.Int32(0), Height: proto.Int32(0)}
	rendition := &ipcpb.StreamTrack{TrackName: "rendition", TrackType: "video", Codec: "H264", TrackIndex: proto.Int32(4), Height: proto.Int32(360), SourceTrack: proto.String("0")}
	thumbnail := &ipcpb.StreamTrack{TrackName: "thumbnail", TrackType: "video", Codec: "JPEG", TrackIndex: proto.Int32(7), Height: proto.Int32(2160)}
	preview := &ipcpb.StreamTrack{TrackName: "preview", TrackType: "video", Codec: "jpeg", TrackIndex: proto.Int32(8), Height: proto.Int32(90)}
	audio := &ipcpb.StreamTrack{TrackName: "audio", TrackType: "audio", Codec: "AAC", TrackIndex: proto.Int32(1)}
	secondSource := &ipcpb.StreamTrack{TrackName: "second-source", TrackType: "video", Codec: "H264", TrackIndex: proto.Int32(5), Height: proto.Int32(1080)}
	smallSource := &ipcpb.StreamTrack{TrackName: "small-source", TrackType: "video", Codec: "H264", TrackIndex: proto.Int32(2), Height: proto.Int32(720)}

	cases := []struct {
		name   string
		tracks []*ipcpb.StreamTrack
		want   *ipcpb.StreamTrack
	}{
		{"no tracks", nil, nil},
		{"audio only", []*ipcpb.StreamTrack{audio}, nil},
		{"images only", []*ipcpb.StreamTrack{thumbnail, preview, audio}, nil},
		{"source after thumbnail, preview and rendition", []*ipcpb.StreamTrack{thumbnail, preview, rendition, audio, source}, source},
		{"source with unknown height over rendition with known height", []*ipcpb.StreamTrack{rendition, bootingSource}, bootingSource},
		{"rendition when no original video exists", []*ipcpb.StreamTrack{thumbnail, rendition}, rendition},
		{"largest original height", []*ipcpb.StreamTrack{smallSource, source}, source},
		{"equal heights resolve to the lowest track index", []*ipcpb.StreamTrack{secondSource, source}, source},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PrimaryVideoTrack(tc.tracks)
			if got != tc.want {
				t.Fatalf("PrimaryVideoTrack = %v, want %v", got.GetTrackName(), tc.want.GetTrackName())
			}
		})
	}
}
