package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"frameworks/api_sidecar/internal/appconfig"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"frameworks/api_sidecar/internal/config"
	"frameworks/api_sidecar/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/sirupsen/logrus"
)

const memoryMiB = uint64(1024 * 1024)

// Source selection is empty even before metadata exists; inhibitors also keep
// this entry out of the process graph once source tracks appear.
const processingSourceProbePolicy = `[{"process":"AV","codec":"H264","track_select":"video=none&audio=none&subtitle=none&meta=none","track_inhibit":"video=all&audio=all","x-LSP-probe":true}]`
const processingSourceIdlePolicy = `[{"process":"AV","codec":"H264","track_select":"video=none&audio=none&subtitle=none&meta=none","track_inhibit":"video=all&audio=all"}]`

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

func processingMemoryEstimate(raw string, source mist.SourceMediaInfo, tracks ...mist.TrackInfo) (uint64, error) {
	return mist.EstimateProcessingMemory(raw, source, tracks...)
}

func processingMemoryBudget(total uint64) uint64 {
	return mist.ProcessingMemoryBudget(total)
}

type processingCapacityError struct {
	reason        string
	requiredBytes uint64
	nodeTooSmall  bool
	policy        string
	budgetBytes   uint64
}

func (e *processingCapacityError) Error() string { return e.reason }
func processingMemoryFailureOutputs(err error) map[string]string {
	var admission *processingAdmissionError
	if errors.As(err, &admission) {
		return map[string]string{mist.ProcessingResultRetryCause: mist.ProcessingRetryCauseNodeUnavailable}
	}
	var capacity *processingCapacityError
	if errors.As(err, &capacity) {
		cause := mist.ProcessingRetryCauseMemoryPressure
		if capacity.nodeTooSmall {
			cause = mist.ProcessingRetryCauseMemoryNodeTooSmall
		}
		return map[string]string{mist.ProcessingResultRetryCause: cause, mist.ProcessingRequiredMemoryBytes: strconv.FormatUint(capacity.requiredBytes, 10), mist.ProcessingMemoryPolicy: capacity.policy, mist.ProcessingNodeMemoryBudget: strconv.FormatUint(capacity.budgetBytes, 10)}
	}
	return processingReadinessFailureOutputs(err)
}

// The persisted policy is also the memory ledger, so a restarted Helmsman
// cannot forget encoders that Mist still runs. Publication and accounting use
// the same lock, and no policy reaches Mist before its reservation is durable.
func reserveProcessingMemory(stream, job string, desired string, bytes, total, available uint64) (bool, error) {
	budget := processingMemoryBudget(total)
	if bytes > budget {
		return false, &processingCapacityError{reason: fmt.Sprintf("processing work needs %d memory bytes; node processing budget is %d", bytes, budget), requiredBytes: bytes, nodeTooSmall: true, policy: desired, budgetBytes: budget}
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
	protected := config.ProcessingProtectedMemory(job)
	remaining := budget - min(budget, protected)
	physical := available - min(available, protected)
	if bytes > remaining || used > remaining-bytes || (bytes > 0 && (physical < extra || physical-extra < 128*memoryMiB)) {
		return false, nil
	}
	record.ProcessesJSON = desired
	record.MemoryBytes = bytes
	if err := persistProcessingOverrideLocked(record); err != nil {
		return false, &processingAdmissionError{err: err}
	}
	return true, nil
}

// admitProcessingPolicy reserves memory for the rungs this edge encodes;
// tracks are the stream's video tracks, and a rung one of them retains starts
// no encoder and reserves nothing.
func (h *ProcessingJobHandler) admitProcessingPolicy(log *logrus.Entry, req *ipcpb.ProcessingJobRequest, stream, desired string, source mist.SourceMediaInfo, tracks []mist.TrackInfo, send func(*ipcpb.ControlMessage)) (string, error) {
	snapshot := h.memorySnapshot
	if snapshot == nil {
		snapshot = processingMemorySnapshot
	}
	total, available, err := snapshot()
	if err != nil {
		return "", &processingAdmissionError{err: err}
	}
	budget := processingMemoryBudget(total)
	floor := uint64(0)
	if raw := req.GetParams()[mist.ProcessingRequiredMemoryBytes]; raw != "" {
		var parseErr error
		floor, parseErr = strconv.ParseUint(raw, 10, 63)
		if parseErr != nil {
			return "", fmt.Errorf("invalid processing memory floor: %w", parseErr)
		}
	}
	plan, err := mist.PlanProcessingMemory(desired, source, budget, runtime.GOMAXPROCS(0), tracks...)
	if err != nil {
		return "", err
	}
	minimum, err := mist.PlanProcessingMemoryThreads(plan.ProcessesJSON, source, 0, 1, tracks...)
	if err != nil {
		return "", err
	}
	minimum.Bytes = max(minimum.Bytes, floor)
	plan.Fits = plan.Fits && floor <= budget
	if !plan.Fits {
		minimum, err = mist.PlanProcessingMemory(desired, source, 0, 1, tracks...)
		if err != nil {
			return "", err
		}
		minimum.Bytes = max(minimum.Bytes, floor)
		return "", &processingCapacityError{reason: "processing ladder exceeds this node's memory budget", requiredBytes: minimum.Bytes, nodeTooSmall: true, policy: desired, budgetBytes: budget}
	}
	reserve := func(candidate *mist.ProcessingMemoryPlan) (bool, error) {
		candidate.Bytes = max(candidate.Bytes, floor)
		var processes []json.RawMessage
		if parseErr := json.Unmarshal([]byte(candidate.ProcessesJSON), &processes); parseErr != nil {
			return false, parseErr
		}
		if len(processes) == 0 {
			candidate.ProcessesJSON = processingSourceIdlePolicy
		}
		return reserveProcessingMemory(stream, req.GetJobId(), candidate.ProcessesJSON, candidate.Bytes, total, available)
	}
	admitted, err := reserve(&plan)
	if err != nil {
		return "", err
	}
	if !admitted {
		free, freeErr := processingMemoryRemainder(stream, req.GetJobId(), total, available)
		if freeErr != nil {
			return "", freeErr
		}
		plan, err = mist.PlanProcessingMemoryThreads(plan.ProcessesJSON, source, free, runtime.GOMAXPROCS(0), tracks...)
		if err != nil {
			return "", err
		}
		if plan.Fits {
			admitted, err = reserve(&plan)
			if err != nil {
				return "", err
			}
		}
	}
	if !admitted {
		return "", &processingCapacityError{reason: "waiting for processing memory", requiredBytes: minimum.Bytes, policy: desired, budgetBytes: budget}
	}
	log.WithFields(logrus.Fields{"memory_bytes": plan.Bytes, "node_memory_bytes": total}).Info("Processing work admitted by memory")
	h.updateProcessConfigCache(send, req.GetJobId(), req.GetArtifactHash(), plan.ProcessesJSON, minimum.Bytes, plan.Bytes)
	return plan.ProcessesJSON, nil
}

func processingMemoryRemainder(stream, job string, total, available uint64) (uint64, error) {
	processingProcessOverridesMu.Lock()
	defer processingProcessOverridesMu.Unlock()
	record, ok := processingOverrideRecords[stream]
	if !ok || record.JobID != job {
		return 0, errors.New("processing memory reservation lost its policy owner")
	}
	var used uint64
	for name, other := range processingOverrideRecords {
		if name != stream {
			used += other.MemoryBytes
		}
	}
	budget := processingMemoryBudget(total)
	if used >= budget {
		return 0, nil
	}
	physical := uint64(0)
	if available > 128*memoryMiB {
		physical = available - 128*memoryMiB
	}
	protected := config.ProcessingProtectedMemory(job)
	logical := budget - used
	logical -= min(logical, protected)
	// Existing bytes belong to this generation and can be reused on upgrade.
	physical += record.MemoryBytes
	physical -= min(physical, protected)
	return min(logical, physical), nil
}

type processingAdmissionError struct{ err error }

func (e *processingAdmissionError) Error() string { return e.err.Error() }
func (e *processingAdmissionError) Unwrap() error { return e.err }

func (h *ProcessingJobHandler) prepareProcessingMemory(log *logrus.Entry, client *mist.Client, req *ipcpb.ProcessingJobRequest, stream, outputPath, desired string, exitCh <-chan ProcessExitEvent, send func(*ipcpb.ControlMessage)) (string, mist.SourceMediaInfo, error) {

	probe, cached := cachedProcessingProbe(req)
	var err error
	if !cached {
		outputs, _, probeErr := h.waitForProcessingStreamReady(context.Background(), log, client, req, stream, "[]", exitCh, nil, nil, nil)
		if probeErr != nil {
			return "", mist.SourceMediaInfo{}, probeErr
		}
		source, _ := sourceFromReadinessOutputs(outputs)
		probe = mist.ProcessingSourceProbe{SourceKey: processingProbeIdentity(req), Source: source, Outputs: outputs}
	}
	if err = saveProcessingProbe(req, stream, probe, send); err != nil {
		return "", probe.Source, err
	}
	source := probe.Source

	if !mist.HasLivepeerProcesses(desired) {
		desired, err = mist.EnsureRotatedSourceRendition(desired, source)
		if err != nil {
			return "", source, err
		}
	}
	processingProcessOverridesMu.Lock()
	previous := processingOverrideRecords[stream].MemoryBytes
	processingProcessOverridesMu.Unlock()
	// A running generation must drain before its reservation can be replaced.
	// A fresh probe stays paused until admission publishes its final policy.
	if previous > 0 {
		if err = h.restartProcessingStreamForLocalFallback(log, client, stream, outputPath, 0); err != nil {
			return "", source, &processingAdmissionError{err: err}
		}
		clearProcessingResourceFailures(stream)
		if err = releaseStoppedProcessingMemory(stream, req.GetJobId()); err != nil {
			return "", source, err
		}
	}
	admitted, err := h.admitProcessingPolicy(log, req, stream, desired, source, readinessVideoTracks(probe.Outputs), send)
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

	current, ok := processingOverrideRecords[stream]
	if !ok || current.JobID != job {
		processingProcessOverridesMu.Unlock()
		return
	}
	current.MemoryBytes = 0
	if err := persistProcessingOverrideLocked(current); err != nil {
		processingProcessOverridesMu.Unlock()
		log.WithError(err).Warn("Could not release durable processing memory reservation")
		return
	}
	processingProcessOverridesMu.Unlock()
	notifyProcessingMemoryReleased(job)
}

func processingMemoryFailureStatus(err error) string {
	var admission *processingAdmissionError
	if errors.As(err, &admission) {
		return processingResultRetryable
	}
	var capacity *processingCapacityError
	if errors.As(err, &capacity) {
		return processingResultRetryable
	}
	return processingReadinessFailureStatus(err)
}

func (h *ProcessingJobHandler) admitLocalProcessingFallback(log *logrus.Entry, client processingRuntimeClient, req *ipcpb.ProcessingJobRequest, stream, output string, pushID int, desired string, source mist.SourceMediaInfo, tracks []mist.TrackInfo, send func(*ipcpb.ControlMessage)) (string, error) {
	if err := h.restartProcessingStreamForLocalFallback(log, client, stream, output, pushID); err != nil {
		return "", &processingAdmissionError{err: err}
	}
	if err := releaseStoppedProcessingMemory(stream, req.GetJobId()); err != nil {
		return "", err
	}
	clearProcessingResourceFailures(stream)
	return h.admitProcessingPolicy(log, req, stream, desired, source, tracks, send)
}

// The caller must have confirmed the generation drained before releasing it.
func releaseStoppedProcessingMemory(stream, job string) error {
	processingProcessOverridesMu.Lock()
	record, ok := processingOverrideRecords[stream]
	if !ok || record.JobID != job {
		processingProcessOverridesMu.Unlock()
		return errors.New("processing memory reservation lost its policy owner")
	}
	released := record.MemoryBytes > 0
	record.MemoryBytes = 0
	err := persistProcessingOverrideLocked(record)
	processingProcessOverridesMu.Unlock()
	if err != nil {
		return &processingAdmissionError{err: err}
	}
	if released {
		notifyProcessingMemoryReleased(job)
	}
	return nil
}

func notifyProcessingMemoryReleased(job string) {
	log := logger
	if log == nil {
		log = logrus.New()
	}
	client := mist.NewClient(log, appconfig.MistClient())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.NotifyProcessingCapacityReleased(ctx); err != nil && logger != nil {
		logger.WithError(err).Debug("Local live replacements will use their admission retry timer")
	}
	if err := control.SendProcessingMemoryReleased(job); err != nil && logger != nil {
		logger.WithError(err).Warn("Could not announce released processing memory")
	}
}
