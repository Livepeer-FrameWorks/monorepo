package handlers

import (
	"context"
	"errors"
	"time"

	"frameworks/api_sidecar/internal/appconfig"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func admitLiveReplacement(evt ProcessReplaceEvent, policy string) (string, error) {
	client := mist.NewClient(logger, appconfig.MistClient())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	response, err := client.GetActiveStreamsContext(ctx)
	if err != nil {
		return "", &processingAdmissionError{err: err}
	}
	active, ok := response["active_streams"].(map[string]any)
	if !ok {
		return "", &processingAdmissionError{err: errors.New("live source inventory unavailable")}
	}
	data, ok := active[evt.StreamName].(map[string]any)
	if !ok {
		return "", &processingAdmissionError{err: errors.New("live source is not active")}
	}
	outputs := extractActiveStreamMetadata(data)
	source, _ := sourceFromReadinessOutputs(outputs)
	if source.Width <= 0 || source.Height <= 0 {
		return "", &processingAdmissionError{err: errors.New("live source dimensions unavailable")}
	}
	return reserveLiveReplacement(evt.StreamName, policy, source, readinessVideoTracks(outputs), processingMemorySnapshot)
}

// Live replacements share the durable memory ledger with upload jobs. Their
// configs replace just one process in a running buffer, so they must never
// replace its complete STREAM_PROCESS policy on a later buffer start. A rung
// one of the live tracks retains (an E-RTMP multitrack rendition) reserves
// nothing, as for uploads.
func reserveLiveReplacement(stream, policy string, source mist.SourceMediaInfo, tracks []mist.TrackInfo, snapshot func() (uint64, uint64, error)) (string, error) {
	policy, err := mist.EnsureRotatedSourceRendition(policy, source)
	if err != nil {
		return "", err
	}
	job := "live:" + stream
	processingProcessOverridesMu.Lock()
	record, exists := processingOverrideRecords[stream]
	if exists && !record.ReplacementOnly {
		processingProcessOverridesMu.Unlock()
		return "", errors.New("live replacement conflicts with a processing job")
	}
	if !exists {
		record = processingOverrideRecord{Version: 1, StreamName: stream, ProcessesJSON: "[]", JobID: job, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(processingOverrideMaxLifetime), ReplacementOnly: true}
	}
	err = persistProcessingOverrideLocked(record)
	processingProcessOverridesMu.Unlock()
	if err != nil {
		return "", err
	}
	h := &ProcessingJobHandler{memorySnapshot: snapshot}
	admitted, err := h.admitProcessingPolicy(logger.WithField("stream", stream), &ipcpb.ProcessingJobRequest{JobId: job}, stream, policy, source, tracks, nil)
	if err != nil {
		clearProcessingProcessOverride(stream, job)
	}
	return admitted, err
}
