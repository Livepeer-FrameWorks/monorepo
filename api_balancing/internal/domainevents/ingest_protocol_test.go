package domainevents

import (
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	"testing"
)

func TestPullIngestProtocol(t *testing.T) {
	if got := IngestProtocol("pull"); got != publicv1.IngestProtocol_INGEST_PROTOCOL_PULL {
		t.Fatalf("pull protocol=%v want PULL", got)
	}
}

func TestMKVIngestProtocol(t *testing.T) {
	if got := IngestProtocol("mkv"); got != publicv1.IngestProtocol_INGEST_PROTOCOL_MKV {
		t.Fatalf("mkv protocol=%v want MKV", got)
	}
}
