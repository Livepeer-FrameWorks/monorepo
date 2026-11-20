package mist

import (
	"encoding/json"
	"fmt"
	"runtime"
	"sort"
	"strings"
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

func localProcessRenditions(process map[string]any, source SourceMediaInfo) ([]ExpectedRendition, error) {
	if process["process"] != "AV" {
		return nil, nil
	}
	encoded, err := json.Marshal([]map[string]any{process})
	if err != nil {
		return nil, err
	}
	return ExpectedVideoRenditions(string(encoded), source)
}

// EstimateProcessingMemory counts only processes running on this edge.
// Livepeer owns compressed request/response buffers, not local video decoders.
func EstimateProcessingMemory(raw string, source SourceMediaInfo) (uint64, error) {
	return estimateProcessingMemory(raw, source, true)
}

func estimateProcessingMemory(raw string, source SourceMediaInfo, bounded bool) (uint64, error) {
	if source.Width < 0 || source.Height < 0 || source.Width > 32768 || source.Height > 32768 {
		return 0, fmt.Errorf("source dimensions outside memory admission range")
	}
	var processes []map[string]any
	if err := json.Unmarshal([]byte(raw), &processes); err != nil {
		return 0, err
	}
	pixels := uint64(source.Width) * uint64(source.Height)
	var bytes uint64
	for _, process := range processes {
		decodeThreads := uint64(max(1, processOptionInt(process, "decode_threads", 1)))
		encodeThreads := uint64(max(1, processOptionInt(process, "encode_threads", 1)))
		if bounded {
			if decodeThreads > 64 {
				decodeThreads = 64
			}
			if encodeThreads > 64 {
				encodeThreads = 64
			}
		}
		switch process["process"] {
		case "Livepeer":
			bytes += 128 * processingMiB
		case "Thumbs":
			bytes += pixels*(32+3*(decodeThreads-1)) + 64*processingMiB
		case "AV":
			rungs, err := localProcessRenditions(process, source)
			if err != nil {
				return 0, err
			}
			for _, rung := range rungs {
				outputPixels := uint64(rung.Width) * uint64(rung.Height)
				lookahead := uint64(0)
				if codec, ok := process["codec"].(string); ok && strings.EqualFold(codec, "H264") {
					lookahead = uint64(processLookahead(process))
				}
				bytes += pixels*(32+3*(decodeThreads-1)) + outputPixels*(8+3*(encodeThreads-1)) + outputPixels*3*lookahead/2 + 64*processingMiB
			}
			if codec, ok := process["codec"].(string); ok && (strings.EqualFold(codec, "AAC") || strings.EqualFold(codec, "opus")) {
				bytes += 64 * processingMiB
			}
		}
	}
	return bytes, nil
}

// PlanProcessingMemory spends memory on parallelism while retaining the preset's
// lookahead. Only a node that cannot fit even one codec thread per rendition
// reduces lookahead, starting with the largest local H264 rendition.
func PlanProcessingMemory(raw string, source SourceMediaInfo, budget uint64, cpus int) (ProcessingMemoryPlan, error) {
	base, err := ProcessingMemoryDefaults(raw, cpus)
	if err != nil {
		return ProcessingMemoryPlan{}, err
	}
	var processes []map[string]any
	if err = json.Unmarshal([]byte(base), &processes); err != nil {
		return ProcessingMemoryPlan{}, err
	}
	type candidate struct {
		index int
		area  uint64
	}
	var local []candidate
	var largest uint64
	for i, process := range processes {
		rungs, parseErr := localProcessRenditions(process, source)
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
				processes[rung.index]["decode_threads"] = min(concurrency, 4)
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
			bytes, estimateErr := EstimateProcessingMemory(string(encoded), source)
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

// PlanProcessingMemoryForCapabilities never budgets controls an older Mist ignores.
// Legacy FFmpeg decoders use up to sixteen auto threads; software encoders may
// select more from the host CPU count, independently of the Go process quota.
func PlanProcessingMemoryForCapabilities(raw string, source SourceMediaInfo, budget uint64, cpus int, controls map[string]bool) (ProcessingMemoryPlan, error) {
	var processes []map[string]any
	if err := json.Unmarshal([]byte(raw), &processes); err != nil {
		return ProcessingMemoryPlan{}, err
	}
	modern := true
	for _, process := range processes {
		name, named := process["process"].(string)
		if !named {
			continue
		}
		if (name == "AV" || name == "Thumbs") && !controls[name] {
			modern = false
		}
	}
	if modern {
		return PlanProcessingMemory(raw, source, budget, cpus)
	}
	for _, process := range processes {
		for _, key := range []string{"memory_budgeted", "encode_threads", "decode_threads", "lookahead"} {
			delete(process, key)
		}
	}
	published, err := json.Marshal(processes)
	if err != nil {
		return ProcessingMemoryPlan{}, err
	}
	for _, process := range processes {
		if process["process"] == "AV" || process["process"] == "Thumbs" {
			process["decode_threads"] = 16
			process["encode_threads"] = max(64, max(cpus, runtime.NumCPU())*2)
		}
	}
	estimated, err := json.Marshal(processes)
	if err != nil {
		return ProcessingMemoryPlan{}, err
	}
	bytes, err := estimateProcessingMemory(string(estimated), source, false)
	return ProcessingMemoryPlan{ProcessesJSON: string(published), Bytes: bytes, Fits: bytes <= budget}, err
}
