package handlers

import (
	"math"
	"sort"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// JSON numbers beyond the exact float64 integer range cannot identify a process
// or media timestamp without rounding. Unknown is distinct from a reported zero.
func exactMistUnsigned(value any) (uint64, bool) {
	number, ok := value.(float64)
	if !ok || math.IsNaN(number) || number < 0 || number > 1<<53-1 || math.Trunc(number) != number {
		return 0, false
	}
	return uint64(number), true
}

func streamProcessObservation(runtimeName string, data map[string]any) *ipcpb.MistStreamProcessObservation {
	observation := &ipcpb.MistStreamProcessObservation{RuntimeName: runtimeName}
	if value, ok := exactMistUnsigned(data["pid"]); ok && value > 0 {
		pid := int64(value)
		observation.BufferPid = &pid
	}
	if pids, known := sourcePIDsFromStreamData(data); known {
		observation.SourcePidsKnown = true
		for pid := range pids {
			observation.SourcePids = append(observation.SourcePids, pid)
		}
		sort.Slice(observation.SourcePids, func(i, j int) bool { return observation.SourcePids[i] < observation.SourcePids[j] })
	}
	if first, ok := exactMistUnsigned(data["firstms"]); ok {
		observation.FirstMediaMs = &first
	}
	if last, ok := exactMistUnsigned(data["lastms"]); ok {
		observation.LastMediaMs = &last
	}
	return observation
}
