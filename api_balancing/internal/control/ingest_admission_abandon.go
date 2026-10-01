package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// IngestEndedAdmissionAbandoned: the node answered the PUSH_REWRITE execution that minted the session
// to Mist without an accept, so Mist refused its publisher.
const IngestEndedAdmissionAbandoned = "admission_abandoned"

// ingestAdmissionTriggerLockKey is the advisory-lock key of one PUSH_REWRITE execution on one node.
// MintIngestSession and AbandonIngestAdmission both take it, so an abandonment either sees the
// execution's committed session or commits before the mint reads it.
func ingestAdmissionTriggerLockKey(nodeID, triggerUUID string) string {
	return "push-admission:" + strconv.Itoa(len(nodeID)) + ":" + nodeID + ":" + strconv.Itoa(len(triggerUUID)) + ":" + triggerUUID
}

// AbandonIngestAdmission settles a PUSH_REWRITE execution that nodeID forwarded for admission and then
// answered to Mist without an accept: the admission's outcome did not reach Helmsman in time, or
// Helmsman could not apply it. Mist refuses the publisher on that answer and never delivers the
// execution again, so any session the execution minted has no publisher, however its admission ended.
//
// The abandonment is recorded under the execution's lock, which makes it a fence: a mint of the
// execution that runs later is refused (MintIngestSession reports IngestSessionAlreadyEnded), and a
// mint that committed earlier is visible to the session lookup here. Each open session found is then
// ended through RetireIngestSession, whether it is still pending or already active; an admission still
// running for it fails its confirmation or projection against the ended row and is denied. Repeating
// the call is a no-op. Returns the number of sessions this call ended.
func AbandonIngestAdmission(ctx context.Context, nodeID, triggerUUID string, connectorPID int64, logger logging.Logger) (int, error) {
	if db == nil {
		return 0, errors.New("abandon ingest admission: no database configured")
	}
	nodeID, triggerUUID = strings.TrimSpace(nodeID), strings.TrimSpace(triggerUUID)
	if nodeID == "" || triggerUUID == "" || connectorPID <= 0 {
		return 0, fmt.Errorf("abandon ingest admission missing identity: node=%q trigger_uuid=%q pid=%d", nodeID, triggerUUID, connectorPID)
	}
	var open []foghorndb.ListOpenIngestSessionsByTriggerRow
	err := database.WithRetryablePostgresTx(ctx, db, nil, func(tx *sql.Tx) error {
		open = nil
		q := foghorndb.New(tx)
		if lockErr := q.LockIngestStream(ctx, ingestAdmissionTriggerLockKey(nodeID, triggerUUID)); lockErr != nil {
			return fmt.Errorf("lock abandoned ingest admission: %w", lockErr)
		}
		if recordErr := q.RecordIngestAdmissionAbandonment(ctx, foghorndb.RecordIngestAdmissionAbandonmentParams{
			NodeID: nodeID, StartTriggerUuid: triggerUUID, ConnectorPid: connectorPID,
		}); recordErr != nil {
			return fmt.Errorf("record abandoned ingest admission: %w", recordErr)
		}
		rows, listErr := q.ListOpenIngestSessionsByTrigger(ctx, foghorndb.ListOpenIngestSessionsByTriggerParams{
			NodeID: nodeID, StartTriggerUuid: triggerUUID,
		})
		if listErr != nil {
			return fmt.Errorf("list sessions of abandoned ingest admission: %w", listErr)
		}
		open = rows
		return nil
	})
	if err != nil {
		return 0, err
	}
	ended := 0
	var endErr error
	for _, session := range open {
		retired, retireErr := RetireIngestSession(ctx, session.SessionID, session.TenantID, session.StreamInternalName, IngestEndedAdmissionAbandoned, logger)
		if retireErr != nil {
			endErr = errors.Join(endErr, fmt.Errorf("end session %s of abandoned ingest admission: %w", session.SessionID, retireErr))
			continue
		}
		if retired {
			ended++
			logger.WithFields(logging.Fields{
				"node_id": nodeID, "internal_name": session.StreamInternalName, "ingest_generation": session.SessionID,
				"connector_pid": connectorPID,
			}).Warn("Ended ingest session: the node answered its PUSH_REWRITE to Mist without an accept, so the publisher was refused")
		}
	}
	return ended, endErr
}
