package control

import (
	"context"
	"strings"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	"frameworks/api_balancing/internal/state"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// transcodeDegradedStreamKind maps a Mist stream name to the bounded metric label.
func transcodeDegradedStreamKind(stream string) string {
	switch {
	case strings.HasPrefix(stream, "live+"):
		return "live"
	case strings.HasPrefix(stream, "pull+"):
		return "pull"
	case strings.HasPrefix(stream, "processing+"):
		return "processing"
	}
	return "other"
}

// ingestSessionTenantForStream resolves the tenant owning an active ingest stream;
// replaceable in tests.
var ingestSessionTenantForStream = func(internalName string) string {
	if st := state.DefaultManager().GetStreamState(internalName); st != nil {
		return st.TenantID
	}
	return ""
}

// markIngestSessionTranscodeDegraded records the degradation on the node's active
// ingest session; replaceable in tests.
var markIngestSessionTranscodeDegraded = func(ctx context.Context, tenantID, nodeID, internalName, reason string) (int64, error) {
	if db == nil {
		return 0, nil
	}
	return foghorndb.New(db).MarkIngestSessionTranscodeDegraded(ctx, foghorndb.MarkIngestSessionTranscodeDegradedParams{
		Reason: reason, TenantID: tenantID, StreamInternalName: internalName, NodeID: nodeID,
	})
}

// processStreamTranscodeDegraded records a Helmsman report that Mist replaced a
// hard-failed transcode process. Live and pull streams mark their active ingest
// session; processing jobs report their own outcome through the job result.
func processStreamTranscodeDegraded(msg *ipcpb.StreamTranscodeDegraded, nodeID string, logger logging.Logger) {
	if msg == nil {
		return
	}
	stream := strings.TrimSpace(msg.GetStream())
	kind := transcodeDegradedStreamKind(stream)
	failedType := strings.TrimSpace(msg.GetFailedProcessType())
	if failedType == "" {
		failedType = "unknown"
	}
	incStreamTranscodeDegraded(failedType, kind, msg.GetReplacementCount() > 0)
	fields := logging.Fields{
		"node_id":             nodeID,
		"stream":              stream,
		"stream_kind":         kind,
		"failed_process_type": failedType,
		"reason":              msg.GetReason(),
		"replacement_count":   msg.GetReplacementCount(),
	}
	logger.WithFields(fields).Warn("Stream transcode degraded to local processing")

	if kind != "live" && kind != "pull" {
		return
	}
	internalName := strings.TrimPrefix(strings.TrimPrefix(stream, "live+"), "pull+")
	tenantID := ingestSessionTenantForStream(internalName)
	if tenantID == "" || nodeID == "" || internalName == "" {
		logger.WithFields(fields).Debug("No tenant known for degraded stream; ingest session not marked")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	marked, err := markIngestSessionTranscodeDegraded(ctx, tenantID, nodeID, internalName, msg.GetReason())
	if err != nil {
		logger.WithError(err).WithFields(fields).Warn("Failed to record transcode degradation on the ingest session")
		return
	}
	if marked == 0 {
		logger.WithFields(fields).Info("No active ingest session on the reporting node for the degraded stream")
	}
}
