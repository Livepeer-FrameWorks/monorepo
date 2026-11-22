package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

type processingOOMProcess struct {
	eventsPath   string
	initialKills uint64
	killsKnown   bool
}

type processingMemoryObservation struct {
	peak      uint64
	processes map[int]processingOOMProcess
}

var processingMemoryObservations = struct {
	sync.Mutex
	streams map[string]*processingMemoryObservation
}{streams: make(map[string]*processingMemoryObservation)}

var processingOOMCounter = func(path string) (uint64, bool) { return readProcessingOOMKills(os.ReadFile, path) }

func processingOOMEventsPath(pid string, readFile func(string) ([]byte, error)) string {
	data, err := readFile(filepath.Join("/proc", pid, "cgroup"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.SplitN(line, ":", 3)
		if len(fields) == 3 && fields[0] == "0" && fields[1] == "" && strings.HasPrefix(fields[2], "/") {
			return filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(filepath.Clean(fields[2]), "/"), "memory.events")
		}
	}
	return ""
}

func readProcessingOOMKills(readFile func(string) ([]byte, error), paths ...string) (uint64, bool) {
	path := ""
	if len(paths) > 0 {
		path = paths[0]
	} else {
		path = processingOOMEventsPath("self", readFile)
	}
	if path == "" {
		return 0, false
	}
	data, err := readFile(path)
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "oom_kill" {
			value, err := strconv.ParseUint(fields[1], 10, 64)
			return value, err == nil
		}
	}
	return 0, false
}

// Only the source identity in a processor's JSON command assigns its resident
// memory to a job; a stream name occurring elsewhere in argv is not authority.
func processingResidentBytes(procRoot, stream string) uint64 {
	total, _ := processingResidentSnapshot(procRoot, stream)
	return total
}

func processingResidentSnapshot(procRoot, stream string) (uint64, []int) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return 0, nil
	}
	var total uint64
	var pids []int
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, parseErr := strconv.Atoi(entry.Name())
		if parseErr != nil || pid <= 0 {
			continue
		}
		dir := filepath.Join(procRoot, entry.Name())
		name, err := os.ReadFile(filepath.Join(dir, "comm"))
		if err != nil {
			continue
		}
		process := strings.TrimSpace(string(name))
		if process != "MistProcAV" && process != "MistProcThumbs" {
			continue
		}
		command, err := os.ReadFile(filepath.Join(dir, "cmdline"))
		if err != nil {
			continue
		}
		own := false
		for _, arg := range bytes.Split(command, []byte{0}) {
			var config struct {
				Source string `json:"source"`
			}
			if json.Unmarshal(arg, &config) == nil && config.Source == stream {
				own = true
				break
			}
		}
		if !own {
			continue
		}
		pids = append(pids, pid)
		status, err := os.ReadFile(filepath.Join(dir, "status"))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(status), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 3 && (fields[0] == "RssAnon:" || fields[0] == "RssShmem:") && fields[2] == "kB" {
				if kb, err := strconv.ParseUint(fields[1], 10, 54); err == nil {
					total += kb * 1024
				}
			}
		}
	}
	return total, pids
}

func observeProcessingMemory(stream string) func() {
	if runtime.GOOS != "linux" {
		return func() {}
	}
	observation := &processingMemoryObservation{processes: make(map[int]processingOOMProcess)}
	processingMemoryObservations.Lock()
	processingMemoryObservations.streams[stream] = observation
	processingMemoryObservations.Unlock()
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			peak, pids := processingResidentSnapshot("/proc", stream)
			discovered := make(map[int]processingOOMProcess, len(pids))
			for _, pid := range pids {
				path := processingOOMEventsPath(strconv.Itoa(pid), os.ReadFile)
				kills, known := processingOOMCounter(path)
				discovered[pid] = processingOOMProcess{eventsPath: path, initialKills: kills, killsKnown: known}
			}
			processingMemoryObservations.Lock()
			for pid, process := range discovered {
				if previous, exists := observation.processes[pid]; !exists || !previous.killsKnown {
					observation.processes[pid] = process
				}
			}
			observation.peak = max(observation.peak, peak)
			processingMemoryObservations.Unlock()
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
		}
	}()
	return func() {
		close(stop)
		<-done
		processingMemoryObservations.Lock()
		if processingMemoryObservations.streams[stream] == observation {
			delete(processingMemoryObservations.streams, stream)
		}
		processingMemoryObservations.Unlock()
	}
}

type processingOOMError struct {
	resource *processingResourceError
	required uint64
	policy   string
}

func (e *processingOOMError) Error() string {
	return fmt.Sprintf("processing ran out of memory; requeueing with %d required bytes: %v", e.required, e.resource)
}
func (e *processingOOMError) Unwrap() error { return e.resource }

func processingExitError(evt ProcessExitEvent) error {
	resource := &processingResourceError{event: evt}
	if (evt.ProcessType != "AV" && evt.ProcessType != "Thumbs") || (evt.ExitCode != -9 && evt.ExitCode != 137) {
		return resource
	}
	processingMemoryObservations.Lock()
	observation := processingMemoryObservations.streams[evt.StreamName]
	if observation == nil {
		processingMemoryObservations.Unlock()
		return resource
	}
	peakObserved := observation.peak
	process, owned := observation.processes[evt.PID]
	processingMemoryObservations.Unlock()
	if !owned {
		return resource
	}
	kills, known := processingOOMCounter(process.eventsPath)
	if !known || !process.killsKnown || kills <= process.initialKills {
		return resource
	}
	processingProcessOverridesMu.Lock()
	record := processingOverrideRecords[evt.StreamName]
	processingProcessOverridesMu.Unlock()
	peak := max(peakObserved, record.MemoryBytes)
	if peak == 0 {
		return resource
	}
	// Sampling can miss the allocation that triggered the kill. Keep the admitted
	// floor as well as the observed aggregate peak, then add 25% and 64 MiB.
	required := peak + peak/4 + 64*memoryMiB
	if required < peak {
		required = ^uint64(0) >> 1
	}
	return &processingOOMError{resource: resource, required: required, policy: record.ProcessesJSON}
}

func processingOOMOutputs(err error) map[string]string {
	var oom *processingOOMError
	if !errors.As(err, &oom) {
		return nil
	}
	return map[string]string{mist.ProcessingResultRetryCause: mist.ProcessingRetryCauseMemoryPressure, mist.ProcessingRequiredMemoryBytes: strconv.FormatUint(oom.required, 10), mist.ProcessingOOMRequiredMemoryBytes: strconv.FormatUint(oom.required, 10), mist.ProcessingMemoryPolicy: oom.policy}
}
