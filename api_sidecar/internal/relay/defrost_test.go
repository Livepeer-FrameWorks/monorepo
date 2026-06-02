package relay

import (
	"testing"
	"time"

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

func TestDefrostAggregatorFlushesQuietWindow(t *testing.T) {
	emitted := make(chan *pb.StorageLifecycleData, 1)
	agg := newDefrostAggregatorWithInterval(5*time.Millisecond, func(data *pb.StorageLifecycleData) error {
		emitted <- data
		return nil
	})

	agg.record("vod", "asset-hash", 123)

	select {
	case got := <-emitted:
		if got.GetAction() != pb.StorageLifecycleData_ACTION_CACHED {
			t.Fatalf("action = %s, want ACTION_CACHED", got.GetAction())
		}
		if got.GetAssetType() != "vod" || got.GetAssetHash() != "asset-hash" {
			t.Fatalf("unexpected asset identity: type=%q hash=%q", got.GetAssetType(), got.GetAssetHash())
		}
		if got.GetSizeBytes() != 123 {
			t.Fatalf("size_bytes = %d, want 123", got.GetSizeBytes())
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for quiet defrost flush")
	}
}
