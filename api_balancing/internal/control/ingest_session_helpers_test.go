package control

import (
	"context"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// CreateIngestSession mints a session without a public stream ID, so it emits
// no stream lifecycle events. Tests that exercise those events call
// MintIngestSession with StreamID set.
func CreateIngestSession(ctx context.Context, tenantID, nodeID, internalName string, connectorPID int64, triggerUUID string, startedAtMillis int64, dvrIntent []byte, ingestClusterID string, logger logging.Logger, authority ...IngestAuthoritySnapshot) (string, IngestSessionOutcome, error) {
	req := IngestSessionRequest{
		TenantID: tenantID, NodeID: nodeID, InternalName: internalName,
		ConnectorPID: connectorPID, TriggerUUID: triggerUUID, StartedAtMillis: startedAtMillis,
		DVRIntent: dvrIntent, IngestClusterID: ingestClusterID,
	}
	if len(authority) > 0 {
		req.Authority = &authority[0]
	}
	return MintIngestSession(ctx, req, logger)
}
