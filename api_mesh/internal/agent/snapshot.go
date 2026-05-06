package agent

import (
	"context"
	"time"

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// snapshotDiskPath is the path Privateer reports disk utilization for.
// "/" matches the root filesystem on Linux and macOS hosts.
const snapshotDiskPath = "/"

// cpuSampleWindow is the duration cpu.Percent blocks for to compute a
// non-zero, instantaneous-ish CPU figure. Short enough not to delay the
// 30s SyncMesh tick noticeably; long enough that the first sample after
// boot is meaningful.
const cpuSampleWindow = 250 * time.Millisecond

// collectResourceSnapshot returns a complete NodeResourceSnapshot for the host
// the agent is running on. Returns nil on any collection failure: proto scalar
// fields cannot distinguish "missing" from a real zero, so partial snapshots
// would overwrite previously-good server values with zeroes.
func collectResourceSnapshot(ctx context.Context, diskPath string) *pb.NodeResourceSnapshot {
	if diskPath == "" {
		diskPath = snapshotDiskPath
	}
	pcts, err := cpu.PercentWithContext(ctx, cpuSampleWindow, false)
	if err != nil || len(pcts) == 0 {
		return nil
	}

	vm, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil || vm == nil || vm.Total == 0 {
		return nil
	}

	du, err := disk.UsageWithContext(ctx, diskPath)
	if err != nil || du == nil || du.Total == 0 {
		return nil
	}

	uptime, err := host.UptimeWithContext(ctx)
	if err != nil || uptime == 0 {
		return nil
	}

	return &pb.NodeResourceSnapshot{
		CpuPercent:     float32(pcts[0]),
		RamUsedBytes:   vm.Used,
		RamTotalBytes:  vm.Total,
		DiskUsedBytes:  du.Used,
		DiskTotalBytes: du.Total,
		UptimeSeconds:  uptime,
		CollectedAt:    timestamppb.New(time.Now().UTC()),
	}
}
