package mist

import (
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"testing"
)

func TestNodeMemoryByteNormalizationPreservesHistoricalSamples(t *testing.T) {
	for _, marked := range []bool{false, true} {
		scale := uint64(1024)
		if marked {
			scale = 1
		}
		report := &ipcpb.NodeLifecycleUpdate{RamMax: 1048576 / scale, RamCurrent: 524288 / scale, ShmTotalBytes: 262144 / scale, ShmUsedBytes: 131072 / scale, MemoryMetricsAreBytes: marked}
		total, used, shm, shmUsed := NodeMemoryBytes(report)
		if total != 1048576 || used != 524288 || shm != 262144 || shmUsed != 131072 {
			t.Fatalf("wire marker=%t changed historical byte values: %d %d %d %d", marked, total, used, shm, shmUsed)
		}
	}
	var report ipcpb.NodeLifecycleUpdate
	ApplyMemoryMetrics(&report, map[string]any{"mem_total": float64(1024)})
	if !report.GetMemoryMetricsAreBytes() || report.GetRamMax() != 1048576 {
		t.Fatalf("new byte report lost wire marker: %+v", &report)
	}
}
