package grpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/database/foghorndb"
	"frameworks/api_balancing/internal/jobs"
	"frameworks/api_balancing/internal/state"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/streamident"
)

// prepareDVRSourceHandoff records the writer transition before sending it. The
// artifact lock orders this against a claimed stop and late writer reports.
func (s *FoghornGRPCServer) prepareDVRSourceHandoff(ctx context.Context, req *sharedpb.StartDVRRequest, sourceNode, hash, raw string) (bool, error) {
	var dispatch jobs.DVRStartDispatch
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "null" {
		return false, nil
	}
	if decodeErr := json.Unmarshal([]byte(raw), &dispatch); decodeErr != nil {
		return false, decodeErr
	}
	if dispatch.SourceNodeID != sourceNode || dispatch.IngestGeneration == "" || req.GetIngestGeneration() == "" || dispatch.IngestGeneration == req.GetIngestGeneration() {
		return false, nil
	}
	if _, isolated := streamident.ParseRuntimeBinding(dispatch.SourceRuntimeName); !isolated {
		return false, nil
	}
	node := state.DefaultManager().GetNodeState(dispatch.NodeID)
	if node == nil {
		return false, nil
	}
	if _, capabilityErr := control.DestinationPublisherRuntimeFence(ctx, dispatch.NodeID, node.ClusterID); capabilityErr != nil {
		return false, capabilityErr
	}
	runtime, err := dvrBoundSourceStreamName(ctx, req, sourceNode)
	if err != nil {
		return false, err
	}
	if _, isolated := streamident.ParseRuntimeBinding(runtime); !isolated {
		return false, nil
	}
	previousRuntime, previousGeneration := dispatch.SourceRuntimeName, dispatch.IngestGeneration
	dispatch.PreviousSourceRuntimeName, dispatch.SourceRuntimeName = previousRuntime, runtime
	dispatch.IngestGeneration, dispatch.State = req.GetIngestGeneration(), jobs.DVRDispatchStateSourceHandoffPending
	dispatch.DispatchedAt = time.Now().Unix()
	if dispatch.NodeID != sourceNode {
		dispatch.SourceBaseURL = control.BuildDTSCURI(sourceNode, runtime, s.logger)
		if dispatch.SourceBaseURL == "" {
			return false, errors.New("recording handoff source has no DTSC endpoint")
		}
	} else {
		dispatch.SourceBaseURL = ""
	}
	encoded, err := json.Marshal(dispatch)
	if err != nil {
		return false, err
	}
	prepared := false
	err = s.withArtifactLifecycleTx(ctx, func(tx *sql.Tx) error {
		q := foghorndb.New(tx)
		if lockErr := q.AcquireDVRStartLock(ctx, control.DVRStartLockKey(req.InternalName, sourceNode)); lockErr != nil {
			return lockErr
		}
		_, prepareErr := q.PrepareDVRSourceHandoff(ctx, foghorndb.PrepareDVRSourceHandoffParams{
			ArtifactHash: hash, TenantID: req.TenantId, NewGeneration: req.GetIngestGeneration(), OldGeneration: previousGeneration,
			OldRuntime: previousRuntime, NewRuntime: runtime, SourceNodeID: sourceNode, DispatchJson: string(encoded),
		})
		if errors.Is(prepareErr, sql.ErrNoRows) {
			return nil
		}
		prepared = prepareErr == nil
		return prepareErr
	})
	if err != nil || !prepared {
		return prepared, err
	}
	return true, nil
}

func (s *FoghornGRPCServer) dispatchDVRSourceHandoff(hash string, dispatch jobs.DVRStartDispatch) {
	err := control.SendDVRStart(dispatch.NodeID, &ipcpb.DVRStartRequest{
		DvrHash: hash, RequestId: hash, InternalName: dispatch.InternalName, StreamId: dispatch.StreamID,
		SourceRuntimeName: dispatch.SourceRuntimeName, PreviousSourceRuntimeName: dispatch.PreviousSourceRuntimeName,
		SourceBaseUrl: dispatch.SourceBaseURL, CommandGeneration: control.DVRStartCommandGeneration,
		Config: &ipcpb.DVRConfig{Enabled: true, Format: "ts", SegmentDuration: dispatch.SegmentSeconds,
			DvrWindowSeconds: dispatch.WindowSeconds, MaxEntries: dispatch.MaxEntries},
	})
	if err != nil {
		s.logger.WithError(err).WithField("dvr_hash", hash).Warn("Recording source handoff remains pending for recovery")
	}
}
