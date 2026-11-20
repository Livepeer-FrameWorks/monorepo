package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"frameworks/api_sidecar/internal/appconfig"
	"frameworks/api_sidecar/internal/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/sirupsen/logrus"
)

const memoryMiB = uint64(1024 * 1024)

// Source selection is empty even before metadata exists; inhibitors also keep
// this entry out of the process graph once source tracks appear.
const processingSourceProbePolicy = `[{"process":"AV","codec":"H264","track_select":"video=none&audio=none&subtitle=none&meta=none","track_inhibit":"video=all&audio=all"}]`

func processingMemorySnapshot() (total, available uint64, err error) {
	return readProcessingMemorySnapshot(config.MemoryCapacityBytes(), os.ReadFile)
}

func readProcessingMemorySnapshot(capacity uint64, readFile func(string) ([]byte, error)) (total, available uint64, err error) {
	total = capacity
	available = total
	if data, readErr := readFile("/proc/meminfo"); readErr == nil {
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] == "MemAvailable:" {
				kb, parseErr := strconv.ParseUint(fields[1], 10, 64)
				if parseErr == nil {
					available = min(available, kb*1024)
				}
			}
		}
	}
	for _, pair := range [][3]string{{"/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory.current", "/sys/fs/cgroup/memory.stat"}, {"/sys/fs/cgroup/memory/memory.limit_in_bytes", "/sys/fs/cgroup/memory/memory.usage_in_bytes", "/sys/fs/cgroup/memory/memory.stat"}} {
		data, readErr := readFile(pair[0])
		if readErr != nil {
			continue
		}
		limit, parseErr := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
		if parseErr != nil || limit == 0 {
			continue
		}
		usageData, usageErr := readFile(pair[1])
		if usageErr != nil {
			return 0, 0, usageErr
		}
		used, usageErr := strconv.ParseUint(strings.TrimSpace(string(usageData)), 10, 64)
		if usageErr != nil {
			return 0, 0, usageErr
		}
		if stats, statErr := readFile(pair[2]); statErr == nil {
			key := "inactive_file"
			if strings.Contains(pair[0], "memory.limit_in_bytes") {
				key = "total_inactive_file"
			}
			for _, line := range strings.Split(string(stats), "\n") {
				fields := strings.Fields(line)
				if len(fields) == 2 && fields[0] == key {
					if reclaimable, parseErr := strconv.ParseUint(fields[1], 10, 64); parseErr == nil {
						used -= min(used, reclaimable)
					}
				}
			}
		}
		total = min(total, limit)
		if used >= limit {
			available = 0
		} else {
			available = min(available, limit-used)
		}
	}
	if total == 0 {
		return 0, 0, errors.New("node memory capacity unavailable")
	}
	return total, available, nil
}

func boundedProcessingMemoryConfig(raw string) (string, error) {
	return mist.ProcessingMemoryDefaults(raw, runtime.GOMAXPROCS(0))
}

func processingMemoryEstimate(raw string, source mist.SourceMediaInfo) (uint64, error) {
	return mist.EstimateProcessingMemory(raw, source)
}

func processingMemoryBudget(total uint64) uint64 {
	return total - min(total, max(512*memoryMiB, total/6))
}

type processingCapacityError struct{ reason string }
type processingPermanentCapacityError struct{ reason string }

func (e *processingPermanentCapacityError) Error() string { return e.reason }
func processingMemoryResultStatus(err error) string {
	var permanent *processingPermanentCapacityError
	if errors.As(err, &permanent) {
		return "failed"
	}
	var capacity *processingCapacityError
	if errors.As(err, &capacity) {
		return processingResultRetryable
	}
	return processingReadinessFailureStatus(err)
}

func (e *processingCapacityError) Error() string { return e.reason }
func processingMemoryFailureOutputs(err error) map[string]string {
	var permanent *processingPermanentCapacityError
	if errors.As(err, &permanent) {
		return map[string]string{mist.ProcessingResultRetryCause: mist.ProcessingRetryCauseInsufficientMemory}
	}
	var capacity *processingCapacityError
	if errors.As(err, &capacity) {
		return map[string]string{mist.ProcessingResultRetryCause: mist.ProcessingRetryCauseMemoryPressure}
	}
	return processingReadinessFailureOutputs(err)
}

// The persisted policy is also the memory ledger, so a restarted Helmsman
// cannot forget encoders that Mist still runs. Publication and accounting use
// the same lock, and no policy reaches Mist before its reservation is durable.
func reserveProcessingMemory(stream, job string, desired string, bytes, total, available uint64) (bool, error) {
	budget := processingMemoryBudget(total)
	if bytes > budget {
		return false, &processingPermanentCapacityError{reason: fmt.Sprintf("processing workload cannot fit node budget: needs %d bytes; budget is %d", bytes, budget)}
	}
	processingProcessOverridesMu.Lock()
	defer processingProcessOverridesMu.Unlock()
	record, ok := processingOverrideRecords[stream]
	if !ok || record.JobID != job {
		return false, errors.New("processing memory reservation lost its policy owner")
	}
	var used uint64
	for name, other := range processingOverrideRecords {
		if name != stream {
			used += other.MemoryBytes
		}
	}
	// An upgrade replaces this job's existing reservation after its source restarts.
	extra := bytes - min(bytes, record.MemoryBytes)
	if used > budget-bytes || (bytes > 0 && available < extra+128*memoryMiB) {
		return false, nil
	}
	record.ProcessesJSON = desired
	record.MemoryBytes = bytes
	if err := persistProcessingOverrideLocked(record); err != nil {
		return false, err
	}
	return true, nil
}

func (h *ProcessingJobHandler) admitProcessingPolicy(log *logrus.Entry, req *ipcpb.ProcessingJobRequest, stream, desired string, source mist.SourceMediaInfo, send func(*ipcpb.ControlMessage)) (string, error) {
	snapshot := h.memorySnapshot
	if snapshot == nil {
		snapshot = processingMemorySnapshot
	}
	total, available, err := snapshot()
	if err != nil {
		return "", err
	}
	controls := h.memoryControls
	if controls == nil && h.mistServerURL != "" {
		cfg := appconfig.MistClient()
		cfg.BaseURL = h.mistServerURL
		client := mist.NewClient(h.logger, cfg)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		var capabilityErr error
		controls, capabilityErr = client.ProcessingResourceControls(ctx)
		cancel()
		if capabilityErr != nil {
			log.WithError(capabilityErr).Warn("Mist resource controls unavailable; using conservative memory admission")
		}
	}
	cpus := runtime.GOMAXPROCS(0)
	plan, err := mist.PlanProcessingMemoryForCapabilities(desired, source, processingMemoryBudget(total), cpus, controls)
	if err != nil {
		return "", err
	}
	if !plan.Fits {
		return "", &processingPermanentCapacityError{reason: fmt.Sprintf("processing workload cannot fit node budget: needs %d bytes; budget is %d", plan.Bytes, processingMemoryBudget(total))}
	}
	// Mist treats an empty override as a request for its wildcard processes.
	// Keep an explicit inhibited entry when this job needs no extra processes.
	var processes []json.RawMessage
	if err = json.Unmarshal([]byte(plan.ProcessesJSON), &processes); err != nil {
		return "", err
	}
	if len(processes) == 0 {
		plan.ProcessesJSON = processingSourceProbePolicy
	}
	admitted, err := reserveProcessingMemory(stream, req.GetJobId(), plan.ProcessesJSON, plan.Bytes, total, available)
	if err != nil {
		return "", err
	}
	if !admitted {
		return "", &processingCapacityError{reason: "waiting for processing memory"}
	}
	log.WithFields(logrus.Fields{"memory_bytes": plan.Bytes, "node_memory_bytes": total}).Info("Processing work admitted by memory")
	h.updateProcessConfigCache(send, req.GetJobId(), req.GetArtifactHash(), plan.ProcessesJSON)
	return plan.ProcessesJSON, nil
}

func (h *ProcessingJobHandler) prepareProcessingMemory(log *logrus.Entry, client *mist.Client, req *ipcpb.ProcessingJobRequest, stream, outputPath, desired string, exitCh <-chan ProcessExitEvent, send func(*ipcpb.ControlMessage)) (string, mist.SourceMediaInfo, error) {
	outputs, _, err := h.waitForProcessingStreamReady(context.Background(), log, client, req, stream, "[]", exitCh, nil, nil, nil)
	if err != nil {
		return "", mist.SourceMediaInfo{}, err
	}
	source, _ := sourceFromReadinessOutputs(outputs)
	desired, err = mist.EnsureRotatedSourceRendition(desired, source)
	if err != nil {
		return "", source, err
	}
	estimated, err := processingMemoryEstimate(desired, source)
	if err != nil {
		return "", source, err
	}
	processingProcessOverridesMu.Lock()
	previous := processingOverrideRecords[stream].MemoryBytes
	processingProcessOverridesMu.Unlock()
	// The probe must not retain input buffers while this attempt waits in Foghorn's queue.
	if estimated > 0 || previous > 0 {
		if err = h.restartProcessingStreamForLocalFallback(log, client, stream, outputPath, 0); err != nil {
			return "", source, err
		}
		clearProcessingResourceFailures(stream)
	}
	admitted, err := h.admitProcessingPolicy(log, req, stream, desired, source, send)
	return admitted, source, err
}

func (h *ProcessingJobHandler) finishProcessingMemory(log *logrus.Entry, client *mist.Client, stream, job string) {
	processingProcessOverridesMu.Lock()
	record, exists := processingOverrideRecords[stream]
	processingProcessOverridesMu.Unlock()
	if !exists || record.JobID != job || record.MemoryBytes == 0 {
		return
	}
	if err := client.NukeStream(stream); err != nil {
		log.WithError(err).Warn("Retaining memory reservation until processing stream stops")
		return
	}
	if !waitProcessingStreamStopped(client, stream, processingStreamStopTimeout) {
		log.Warn("Retaining memory reservation for processing stream still stopping")
		return
	}
	processingProcessOverridesMu.Lock()
	defer processingProcessOverridesMu.Unlock()
	current, ok := processingOverrideRecords[stream]
	if !ok || current.JobID != job {
		return
	}
	current.MemoryBytes = 0
	if err := persistProcessingOverrideLocked(current); err != nil {
		log.WithError(err).Warn("Could not release durable processing memory reservation")
	}
}
