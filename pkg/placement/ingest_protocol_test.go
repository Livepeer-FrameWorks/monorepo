package placement

import (
	"testing"

	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

func TestIngestProtocolName(t *testing.T) {
	for _, tc := range []struct {
		protocol sharedpb.IngestProtocol
		want     string
		invalid  bool
	}{
		{sharedpb.IngestProtocol_INGEST_PROTOCOL_UNSPECIFIED, "", false},
		{sharedpb.IngestProtocol_INGEST_PROTOCOL_WHIP, "whip", false},
		{sharedpb.IngestProtocol_INGEST_PROTOCOL_RTMP, "rtmp", false},
		{sharedpb.IngestProtocol_INGEST_PROTOCOL_SRT, "srt", false},
		{sharedpb.IngestProtocol(-1), "", true},
		{sharedpb.IngestProtocol(4), "", true},
	} {
		got, err := IngestProtocolName(tc.protocol)
		if got != tc.want || (err != nil) != tc.invalid {
			t.Fatalf("protocol %d = %q, %v; want %q, invalid=%v", tc.protocol, got, err, tc.want, tc.invalid)
		}
	}
}
