package mist

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const ProcessingMemoryNoNodeReason = "no node in this cluster has enough memory for this ladder"

const (
	ProcessingWaitMemory                 = "memory"
	ProcessingWaitSlots                  = "slots"
	ProcessingWaitFittingNodeUnavailable = "fitting_node_unavailable"
	ProcessingWaitClusterUnavailable     = "cluster_unavailable"
)

const processingMiB = uint64(1024 * 1024)

type ProcessingMemoryPlan struct {
	ProcessesJSON string
	Bytes         uint64
	Fits          bool
}

// ProcessingMemoryDefaults supplies explicit codec concurrency without changing
// the encoder preset's lookahead or scaling quality.
func ProcessingMemoryDefaults(raw string, cpus int) (string, error) {
	var processes []map[string]any
	if err := json.Unmarshal([]byte(raw), &processes); err != nil {
		return "", err
	}
	threads := max(1, min(cpus, 16))
	for _, process := range processes {
		delete(process, "memory_budgeted")
		switch process["process"] {
		case "AV":
			codec, ok := process["codec"].(string)
			if ok && isAVVideoCodec(codec) {
				process["encode_threads"] = threads
				process["decode_threads"] = min(threads, 4)
			} else {
				process["encode_threads"] = 1
				process["decode_threads"] = 1
			}
			delete(process, "lookahead")
		case "Thumbs":
			process["decode_threads"] = min(threads, 4)
			process["encode_threads"] = 1
		}
	}
	encoded, err := json.Marshal(processes)
	return string(encoded), err
}

func processOptionInt(process map[string]any, key string, fallback int) int {
	switch value := process[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	default:
		return fallback
	}
}

func processLookahead(process map[string]any) int {
	if _, present := process["lookahead"]; present {
		return max(0, min(250, processOptionInt(process, "lookahead", 20)))
	}
	preset, _ := process["preset"].(string) //nolint:errcheck // absent preset uses Mist's faster default
	switch preset {
	case "ultrafast", "superfast":
		return 0
	case "veryfast":
		return 10
	case "", "faster":
		return 20
	case "fast":
		return 30
	case "medium":
		return 40
	default:
		return 60
	}
}

// localProcessRenditions returns the rungs a local AV process encodes. A rung
// an original track retains starts no encoder, so it costs no memory.
func localProcessRenditions(process map[string]any, source SourceMediaInfo, tracks []TrackInfo) ([]ExpectedRendition, error) {
	if process["process"] != "AV" {
		return nil, nil
	}
	encoded, err := json.Marshal([]map[string]any{process})
	if err != nil {
		return nil, err
	}
	expected, err := ExpectedVideoRenditions(string(encoded), source, tracks...)
	if err != nil {
		return nil, err
	}
	encodes := expected[:0]
	for _, rung := range expected {
		if !rung.Retained {
			encodes = append(encodes, rung)
		}
	}
	return encodes, nil
}

// processRetainsRung reports whether an original track retains the process's
// rung. Such a process stays in the published policy even when the source is
// too small for it: the policy is what readiness, completeness and the VOD
// selector read the retained rung from.
func processRetainsRung(process map[string]any, source SourceMediaInfo, tracks []TrackInfo) bool {
	if _, retainable := process["track_retain"]; !retainable || len(tracks) == 0 {
		return false
	}
	encoded, err := json.Marshal([]map[string]any{process})
	if err != nil {
		return false
	}
	expected, err := ExpectedVideoRenditions(string(encoded), source, tracks...)
	return err == nil && len(expected) == 1 && expected[0].Retained
}

// EstimateProcessingMemory counts only processes running on this edge.
// Livepeer owns compressed request/response buffers, not local video decoders.
// Tracks are the stream's video tracks; a rung one of them retains is free.
func EstimateProcessingMemory(raw string, source SourceMediaInfo, tracks ...TrackInfo) (uint64, error) {

	if source.Width < 0 || source.Height < 0 || source.Width > 32768 || source.Height > 32768 {
		return 0, fmt.Errorf("source dimensions outside memory admission range")
	}
	var processes []map[string]any
	if err := json.Unmarshal([]byte(raw), &processes); err != nil {
		return 0, err
	}
	pixels := uint64(source.Width) * uint64(source.Height)
	// Per source pixel, from testdata/processing_memory_20261125.json. Admission
	// does not know the sample depth, so codecs that carry 10-bit (HEVC, AV1,
	// VP9, ...) are costed at their 10-bit peak; only H264 keeps the 8-bit fit.
	decodeBase, decodePerThread, thumbsDecode := uint64(14), uint64(3), uint64(4)
	if !strings.EqualFold(source.VideoCodec, "H264") {
		decodeBase, decodePerThread, thumbsDecode = 20, 6, 18
	}
	var bytes uint64
	for _, process := range processes {
		decodeThreads := uint64(max(1, processOptionInt(process, "decode_threads", 1)))
		encodeThreads := uint64(max(1, processOptionInt(process, "encode_threads", 1)))
		if decodeThreads > 64 {
			decodeThreads = 64
		}
		if encodeThreads > 64 {
			encodeThreads = 64
		}
		switch process["process"] {
		case "Livepeer":
			bytes += 128 * processingMiB
		case "Thumbs":
			bytes += (32*processingMiB + pixels*thumbsDecode) * 115 / 100
		case "AV":
			rungs, err := localProcessRenditions(process, source, tracks)
			if err != nil {
				return 0, err
			}
			for _, rung := range rungs {
				outputPixels := uint64(rung.Width) * uint64(rung.Height)
				lookahead := uint64(0)
				if codec, ok := process["codec"].(string); ok && strings.EqualFold(codec, "H264") {
					lookahead = uint64(processLookahead(process))
				}
				// Include each decoder's source-sized surfaces and retain 15% allocation margin.
				bytes += (40*processingMiB + pixels*(decodeBase+decodePerThread*(decodeThreads-1)) + outputPixels*(8+6*(encodeThreads-1)) + outputPixels*2*lookahead/5) * 115 / 100
			}
			if codec, ok := process["codec"].(string); ok && (strings.EqualFold(codec, "AAC") || strings.EqualFold(codec, "opus")) {
				bytes += 32 * processingMiB
			}
		}
	}
	return bytes, nil
}

// PlanProcessingMemory spends memory on parallelism while retaining the preset's
// lookahead. Only a node that cannot fit even one codec thread per rendition
// reduces lookahead, starting with the largest local H264 rendition.
func PlanProcessingMemory(raw string, source SourceMediaInfo, budget uint64, cpus int, tracks ...TrackInfo) (ProcessingMemoryPlan, error) {
	return planProcessingMemory(raw, source, budget, cpus, false, tracks)
}

// PlanProcessingMemoryThreads changes concurrency while preserving the quality
// selected against the node's full budget, including any explicit lookahead.
func PlanProcessingMemoryThreads(raw string, source SourceMediaInfo, budget uint64, cpus int, tracks ...TrackInfo) (ProcessingMemoryPlan, error) {
	return planProcessingMemory(raw, source, budget, cpus, true, tracks)
}

func planProcessingMemory(raw string, source SourceMediaInfo, budget uint64, cpus int, preserveQuality bool, tracks []TrackInfo) (ProcessingMemoryPlan, error) {
	base, err := ProcessingMemoryDefaults(raw, cpus)
	if err != nil {
		return ProcessingMemoryPlan{}, err
	}
	var processes []map[string]any
	if err = json.Unmarshal([]byte(base), &processes); err != nil {
		return ProcessingMemoryPlan{}, err
	}
	if preserveQuality {
		var original []map[string]any
		if err = json.Unmarshal([]byte(raw), &original); err != nil {
			return ProcessingMemoryPlan{}, err
		}
		for i := range processes {
			if value, present := original[i]["lookahead"]; present {
				processes[i]["lookahead"] = value
			}
		}
	}
	// Source-size inhibitors are known after the probe. Do not publish their
	// encoders and rely on later graph retirement: even a short start allocates
	// a full source decoder that the memory estimate deliberately excludes.
	runnable := processes[:0]
	for _, process := range processes {
		if process["process"] == "AV" {
			codec, knownCodec := process["codec"].(string)
			inhibitor, knownInhibitor := process["track_inhibit"].(string)
			if knownCodec && knownInhibitor && isAVVideoCodec(codec) && shouldInhibitLivepeerSelector(inhibitor, source) && !processRetainsRung(process, source, tracks) {
				continue
			}
		}
		runnable = append(runnable, process)
	}
	processes = runnable
	type candidate struct {
		index int
		area  uint64
	}
	var local []candidate
	var largest uint64
	for i, process := range processes {
		rungs, parseErr := localProcessRenditions(process, source, tracks)
		if parseErr != nil {
			return ProcessingMemoryPlan{}, parseErr
		}
		for _, rung := range rungs {
			area := uint64(rung.Width) * uint64(rung.Height)
			local = append(local, candidate{i, area})
			largest = max(largest, area)
		}
	}
	sort.SliceStable(local, func(i, j int) bool { return local[i].area > local[j].area })
	maxThreads := max(1, min(cpus, 16))
	var result ProcessingMemoryPlan
	tryThreads := func() (bool, error) {
		for threads := maxThreads; threads >= 1; threads-- {
			for _, rung := range local {
				concurrency := 1
				if largest > 0 {
					concurrency = max(1, int(uint64(threads)*rung.area/largest))
				}
				processes[rung.index]["encode_threads"] = concurrency
				processes[rung.index]["decode_threads"] = min(min(threads, 4), max(1, (source.Width*source.Height+1280*720-1)/(1280*720)))
			}
			for _, process := range processes {
				if process["process"] == "Thumbs" {
					process["decode_threads"] = min(threads, 4)
				}
			}
			encoded, encodeErr := json.Marshal(processes)
			if encodeErr != nil {
				return false, encodeErr
			}
			bytes, estimateErr := EstimateProcessingMemory(string(encoded), source, tracks...)
			if estimateErr != nil {
				return false, estimateErr
			}
			result = ProcessingMemoryPlan{ProcessesJSON: string(encoded), Bytes: bytes, Fits: bytes <= budget}
			if result.Fits {
				return true, nil
			}
		}
		return false, nil
	}
	if fits, planErr := tryThreads(); planErr != nil || fits {
		return result, planErr
	}
	if preserveQuality {
		return result, nil
	}
	for _, rung := range local {
		process := processes[rung.index]
		codec, ok := process["codec"].(string)
		if !ok || !strings.EqualFold(codec, "H264") || processLookahead(process) == 0 {
			continue
		}
		process["lookahead"] = 0
		if fits, planErr := tryThreads(); planErr != nil || fits {
			return result, planErr
		}
	}
	return result, nil
}

// ProcessingMemoryBudget leaves headroom for the runtime and non-job work.
func ProcessingMemoryBudget(total uint64) uint64 {
	headroom := max(512*processingMiB, total/6)
	if headroom >= total {
		return 0
	}
	return total - headroom
}
