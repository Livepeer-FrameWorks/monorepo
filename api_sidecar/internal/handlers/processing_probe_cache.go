package handlers

import (
	"encoding/json"
	"errors"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func processingProbeIdentity(req *ipcpb.ProcessingJobRequest) string {
	if key := req.GetParams()[mist.ProcessingSourceKeyParam]; key != "" {
		return key
	}
	return mist.ProcessingSourceIdentity(req.GetTenantId(), req.GetJobId(), req.GetArtifactHash(), req.GetSourceUrl())
}

func cachedProcessingProbe(req *ipcpb.ProcessingJobRequest) (mist.ProcessingSourceProbe, bool) {
	probe, err := mist.DecodeProcessingSourceProbe(req.GetParams()[mist.ProcessingSourceProbeParam], processingProbeIdentity(req))
	return probe, err == nil
}

func hasCachedProcessingProbe(req *ipcpb.ProcessingJobRequest) bool {
	_, ok := cachedProcessingProbe(req)
	return ok
}

func saveProcessingProbe(req *ipcpb.ProcessingJobRequest, stream string, probe mist.ProcessingSourceProbe, send func(*ipcpb.ControlMessage)) error {
	data, err := json.Marshal(probe)
	if err != nil {
		return err
	}
	processingProcessOverridesMu.Lock()
	record, ok := processingOverrideRecords[stream]
	if !ok || record.JobID != req.GetJobId() {
		processingProcessOverridesMu.Unlock()
		return errors.New("processing source probe lost its owner")
	}
	record.SourceProbe = string(data)
	err = persistProcessingOverrideLocked(record)
	processingProcessOverridesMu.Unlock()
	if err != nil {
		return &processingAdmissionError{err: err}
	}
	if send != nil {
		send(&ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_ProcessingJobResult{ProcessingJobResult: &ipcpb.ProcessingJobResult{
			JobId: req.GetJobId(), Status: "cache_update", Outputs: map[string]string{"artifact_hash": req.GetArtifactHash(), mist.ProcessingSourceProbeParam: string(data)},
		}}})
	}
	return nil
}
