package control

import (
	"errors"
	"io/fs"
	"strconv"
	"sync"
	"time"

	"frameworks/api_sidecar/internal/storage"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

// MistProcAV reports one PROCESS_AV_VIRTUAL_SEGMENT_COMPLETE per process about every second.
// Helmsman folds a process's samples into one durable billing event per wall-clock window, so an
// always-on multi-rendition stream costs one WAL entry and one Foghorn round trip per rendition
// every processBillingWindow instead of every second.
//
// Every sample is durable before the handler answers: the window's running total is rewritten in
// the WAL staging area on each sample and sealed for delivery when the window ends or the process
// reports its final sample. The totals are sums of the per-sample deltas, so Periscope bills the
// window exactly as it billed the samples.
const processBillingWindow = 10 * time.Second

type processBillingWindowState struct {
	id      string
	start   time.Time
	trigger *ipcpb.MistTrigger
}

type processBillingAggregator struct {
	mu   sync.Mutex
	open map[string]*processBillingWindowState
	// unsealed holds windows, by source_event_id, whose seal failed; the sweeper retries them.
	unsealed map[string]*processBillingWindowState
	// bootID separates this process lifetime's windows from those of an earlier one. A window
	// sealed and delivered before a restart must never share an id with a window that starts
	// after it, or downstream deduplication would drop the later window's usage.
	bootID string
	now    func() time.Time
}

var processBilling = newProcessBillingAggregator()

func newProcessBillingAggregator() *processBillingAggregator {
	return &processBillingAggregator{
		open:     make(map[string]*processBillingWindowState),
		unsealed: make(map[string]*processBillingWindowState),
		bootID:   uuid.NewString(),
		now:      time.Now,
	}
}

// RecordProcessBillingSample durably adds one MistProcAV billing sample to its process's current
// window and returns the window's source_event_id. processKey identifies one process output: the
// same key must never be used by two processes at once.
func RecordProcessBillingSample(processKey string, sample *ipcpb.MistTrigger) (string, error) {
	if !triggerForwarderStarted.Load() || triggerWAL == nil {
		return "", errTriggerForwarderUnready
	}
	return processBilling.add(triggerWAL, processKey, sample)
}

func (a *processBillingAggregator) add(wal *storage.TriggerWAL, processKey string, sample *ipcpb.MistTrigger) (string, error) {
	triggerType := sample.GetTriggerType()
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	start := now.Truncate(processBillingWindow)
	window := a.open[processKey]
	if window != nil && !window.start.Equal(start) {
		delete(a.open, processKey)
		a.sealLocked(wal, window, now)
		window = nil
	}
	fresh := window == nil
	if fresh {
		window = &processBillingWindowState{
			id: storage.ComputeSourceEventID(sample.GetNodeId(), triggerType, []byte(processKey),
				a.bootID+":"+strconv.FormatInt(start.UnixMilli(), 10)),
			start: start,
		}
	}
	merged := mergeProcessBillingSample(window.trigger, sample)
	merged.RequestId = window.id
	merged.EventId = storage.ComputeTypedEventID(window.id)
	if err := wal.Stage(merged); err != nil {
		TriggerWALAppends.WithLabelValues(triggerType, "error").Inc()
		return window.id, err
	}
	window.trigger = merged
	if fresh {
		TriggerWALAppends.WithLabelValues(triggerType, "appended").Inc()
	} else {
		TriggerWALAppends.WithLabelValues(triggerType, "aggregated").Inc()
	}
	if merged.GetProcessBilling().GetIsFinal() {
		// A final window is sealed now, so the window-end sweep must not seal it again.
		delete(a.open, processKey)
		a.sealLocked(wal, window, now)
		return window.id, nil
	}
	a.open[processKey] = window
	return window.id, nil
}

// sealLocked hands a window to the forwarder. A window whose seal fails stays staged on disk and
// is retried by the sweeper; a restart seals it as well.
func (a *processBillingAggregator) sealLocked(wal *storage.TriggerWAL, window *processBillingWindowState, now time.Time) {
	if err := wal.Seal(window.id, now); err != nil {
		// A window without its staged file can never be sealed; retrying it would only repeat the
		// failure every sweep.
		if errors.Is(err, fs.ErrNotExist) {
			if pkgLogger != nil {
				pkgLogger.WithError(err).WithFields(logging.Fields{
					"source_event_id": window.id,
					"window_start":    window.start.UTC().Format(time.RFC3339),
				}).Error("Process billing window has no staged event to seal; dropping it")
			}
			return
		}
		a.unsealed[window.id] = window
		if pkgLogger != nil {
			pkgLogger.WithError(err).WithFields(logging.Fields{
				"source_event_id": window.id,
				"window_start":    window.start.UTC().Format(time.RFC3339),
			}).Warn("Failed to seal a process billing window; retrying")
		}
		return
	}
	updateTriggerWALDepthGauge()
	wakeupTriggerForwarder()
}

// sweep seals every window that has ended and retries failed seals.
func (a *processBillingAggregator) sweep(wal *storage.TriggerWAL) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	for id, window := range a.unsealed {
		delete(a.unsealed, id)
		a.sealLocked(wal, window, now)
	}
	for key, window := range a.open {
		if now.Before(window.start.Add(processBillingWindow)) {
			continue
		}
		delete(a.open, key)
		a.sealLocked(wal, window, now)
	}
}

func processBillingSweepLoop(wal *storage.TriggerWAL) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		processBilling.sweep(wal)
	}
}

// mergeProcessBillingSample returns the window total after sample. Per-window deltas and durations
// are summed; cumulative counters, dimensions and timestamps take the latest sample; rates are
// recomputed over the whole window; the window is final once any sample is.
func mergeProcessBillingSample(total, sample *ipcpb.MistTrigger) *ipcpb.MistTrigger {
	merged := proto.CloneOf(sample)
	if total == nil {
		return merged
	}
	prev, next, out := total.GetProcessBilling(), sample.GetProcessBilling(), merged.GetProcessBilling()
	if prev == nil || next == nil || out == nil {
		return merged
	}
	out.DurationMs = prev.GetDurationMs() + next.GetDurationMs()
	out.InputFramesDelta = sumOptional(prev.InputFramesDelta, next.InputFramesDelta)
	out.OutputFramesDelta = sumOptional(prev.OutputFramesDelta, next.OutputFramesDelta)
	out.InputBytesDelta = sumOptional(prev.InputBytesDelta, next.InputBytesDelta)
	out.OutputBytesDelta = sumOptional(prev.OutputBytesDelta, next.OutputBytesDelta)
	out.SourceAdvancedMs = sumOptional(prev.SourceAdvancedMs, next.SourceAdvancedMs)
	out.SinkAdvancedMs = sumOptional(prev.SinkAdvancedMs, next.SinkAdvancedMs)
	if prev.GetIsFinal() || next.GetIsFinal() {
		final := true
		out.IsFinal = &final
	}
	if durationMs := out.GetDurationMs(); durationMs > 0 {
		seconds := float64(durationMs) / 1000
		if out.SourceAdvancedMs != nil {
			rtf := float64(out.GetSourceAdvancedMs()) / float64(durationMs)
			out.RtfIn = &rtf
		}
		if out.SinkAdvancedMs != nil {
			rtf := float64(out.GetSinkAdvancedMs()) / float64(durationMs)
			out.RtfOut = &rtf
		}
		if out.OutputFramesDelta != nil {
			fps := float64(out.GetOutputFramesDelta()) / seconds
			out.OutputFpsMeasured = &fps
		}
		if out.OutputBytesDelta != nil {
			bitrate := int64(float64(out.GetOutputBytesDelta()*8) / seconds)
			out.OutputBitrateBps = &bitrate
		}
	}
	return merged
}

func sumOptional(a, b *int64) *int64 {
	if a == nil && b == nil {
		return nil
	}
	var sum int64
	if a != nil {
		sum += *a
	}
	if b != nil {
		sum += *b
	}
	return &sum
}
