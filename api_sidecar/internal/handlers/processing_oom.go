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

type processingMemoryObservation struct {
	peak, initialKills uint64
	killsKnown         bool
}

var processingMemoryObservations = struct {
	sync.Mutex
	streams map[string]*processingMemoryObservation
}{streams: make(map[string]*processingMemoryObservation)}

var processingOOMCounter = func() (uint64, bool) { return readProcessingOOMKills(os.ReadFile) }

func readProcessingOOMKills(readFile func(string) ([]byte, error)) (uint64, bool) {
	data, err := readFile("/sys/fs/cgroup/memory.events")
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
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return 0
	}
	var total uint64
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := strconv.ParseUint(entry.Name(), 10, 32); err != nil {
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
		status, err := os.ReadFile(filepath.Join(dir, "status"))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(status), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 3 && fields[0] == "VmRSS:" && fields[2] == "kB" {
				if kb, err := strconv.ParseUint(fields[1], 10, 54); err == nil {
					total += kb * 1024
				}
			}
		}
	}
	return total
}

func observeProcessingMemory(stream string) func() {
	if runtime.GOOS != "linux" {
		return func() {}
	}
	kills, known := processingOOMCounter()
	observation := &processingMemoryObservation{initialKills: kills, killsKnown: known}
	processingMemoryObservations.Lock()
	processingMemoryObservations.streams[stream] = observation
	processingMemoryObservations.Unlock()
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			peak := processingResidentBytes("/proc", stream)
			processingMemoryObservations.Lock()
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
	snapshot := *observation
	processingMemoryObservations.Unlock()
	kills, known := processingOOMCounter()
	if !known || !snapshot.killsKnown || kills <= snapshot.initialKills {
		return resource
	}
	processingProcessOverridesMu.Lock()
	record := processingOverrideRecords[evt.StreamName]
	processingProcessOverridesMu.Unlock()
	peak := max(snapshot.peak, record.MemoryBytes)
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
	return map[string]string{mist.ProcessingResultRetryCause: mist.ProcessingRetryCauseMemoryPressure, mist.ProcessingRequiredMemoryBytes: strconv.FormatUint(oom.required, 10), mist.ProcessingMemoryPolicy: oom.policy}
}
