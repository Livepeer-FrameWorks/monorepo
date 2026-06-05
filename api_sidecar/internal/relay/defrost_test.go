package relay

import (
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"testing"
	"time"
)

func TestDefrostAggregatorFlushesQuietWindow(t *testing.T) {
	emitted := make(chan *ipcpb.StorageLifecycleData, 1)
	agg := newDefrostAggregatorWithInterval(5*time.Millisecond, func(data *ipcpb.StorageLifecycleData) error {
		emitted <- data
		return nil
	})

	agg.record("vod", "asset-hash", 123)

	select {
	case got := <-emitted:
		if got.GetAction() != ipcpb.StorageLifecycleData_ACTION_CACHED {
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
