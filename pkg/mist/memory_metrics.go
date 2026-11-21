package mist

import (
	"math"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// ApplyMemoryMetrics converts Mist's KiB counters into the lifecycle's byte units.
func ApplyMemoryMetrics(update *ipcpb.NodeLifecycleUpdate, payload map[string]any) {
	update.MemoryMetricsAreBytes = true
	number := func(value any, scale uint64) uint64 {
		count, ok := value.(float64)
		if !ok || math.IsNaN(count) || count <= 0 || count >= float64(uint64(math.MaxUint64)/scale) {
			return 0
		}
		return uint64(count * float64(scale))
	}
	if value, present := payload["mem_total"]; present {
		update.RamMax = number(value, 1024)

	}
	if value, present := payload["mem_used"]; present {
		update.RamCurrent = number(value, 1024)

	}
	update.ShmTotalBytes = number(payload["shm_total"], 1024)
	update.ShmUsedBytes = number(payload["shm_used"], 1024)
}

// NodeMemoryBytes normalizes pre-v0.3.16 lifecycle counters without relying on
// node clocks or rollout timing. The explicit marker makes replay idempotent.
func NodeMemoryBytes(update *ipcpb.NodeLifecycleUpdate) (total, used, shmTotal, shmUsed uint64) {
	scale := uint64(1024)
	if update.GetMemoryMetricsAreBytes() {
		scale = 1
	}
	convert := func(value uint64) uint64 {
		if value > math.MaxUint64/scale {
			return 0
		}
		return value * scale
	}
	return convert(update.GetRamMax()), convert(update.GetRamCurrent()), convert(update.GetShmTotalBytes()), convert(update.GetShmUsedBytes())
}
