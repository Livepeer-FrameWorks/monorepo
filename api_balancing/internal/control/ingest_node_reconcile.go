package control

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"
)

// Ended reasons written by node reconciliation and takeover.
const (
	// IngestEndedAbsentOnReregister: the node re-registered without listing this generation among
	// the publishers it still holds.
	IngestEndedAbsentOnReregister = "absent_on_reregister"
	// IngestEndedSupersededByNewNode: a publisher for the same stream was admitted on another node
	// while this session's node had no control connection.
	IngestEndedSupersededByNewNode = "superseded_by_new_node"
	// ingestEndedPlacementClaimLost: Commodore refused the placement renewal because another
	// publisher holds the stream's claim (RetireIngestSessionByClaim).
	ingestEndedPlacementClaimLost = "placement_claim_lost"
)

// NodeIngestDrainFunc sends a drain command to one node (SendDrainStream in production).
type NodeIngestDrainFunc func(ctx context.Context, nodeID string, req *ipcpb.DrainStreamRequest) error

// NodeIngestReconcileResult counts what one registration's reconciliation did.
type NodeIngestReconcileResult struct {
	Kept    int // open sessions the node still holds
	Ended   int // open sessions the node no longer holds, ended absent_on_reregister
	Stopped int // generations the node holds that another publisher took over, told to stop
}

// IngestRegistrationCutoff reads the database clock for a registration. Session rows carry the
// database's started_at, so comparing against this value (not Foghorn's wall clock) separates
// sessions minted before the registration from those its own connection mints afterwards.
func IngestRegistrationCutoff(ctx context.Context) (time.Time, error) {
	if db == nil {
		return time.Time{}, errors.New("ingest registration cutoff: no database configured")
	}
	return foghorndb.New(db).IngestRegistrationCutoff(ctx)
}

// ReconcileNodeIngestSessions settles a (re)registered node's ingest sessions against the publisher
// generations the node reports it still holds. A control connection can drop and come back without
// Mist or its publishers noticing, so the registration is where the state becomes clear again:
//
//   - an open session whose generation the node lists continues untouched;
//   - an open session the node does not list ends through the normal offline path
//     (absent_on_reregister). Only sessions minted before cutoff are considered; admissions over the
//     new connection are not yet in the node's list;
//   - a listed generation whose session another publisher took over (takeover on another node, or a
//     placement claim lost to another cluster) is told to stop with a drain fenced on that exact
//     generation, so the node cannot keep publishing a stream it no longer owns.
//
// current reports whether the registration that produced reported is still the node's connection;
// a newer registration reconciles on its own, so this one stops.
func ReconcileNodeIngestSessions(ctx context.Context, nodeID string, reported []*ipcpb.LiveIngestGeneration, cutoff time.Time, drain NodeIngestDrainFunc, current func() bool, logger logging.Logger) (NodeIngestReconcileResult, error) {
	var result NodeIngestReconcileResult
	if db == nil {
		return result, errors.New("reconcile node ingest sessions: no database configured")
	}
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" || cutoff.IsZero() {
		return result, errors.New("reconcile node ingest sessions requires node and cutoff")
	}
	stillCurrent := func() bool { return current == nil || current() }

	byGeneration := make(map[string]*ipcpb.LiveIngestGeneration, len(reported))
	generationIDs := make([]string, 0, len(reported))
	for _, generation := range reported {
		id := strings.TrimSpace(generation.GetGeneration())
		if _, err := uuid.Parse(id); err != nil {
			logger.WithFields(logging.Fields{
				"node_id": nodeID, "runtime_name": generation.GetRuntimeName(), "ingest_generation": id,
			}).Warn("Ignoring reported live ingest generation that is not a session id")
			continue
		}
		if _, dup := byGeneration[id]; !dup {
			generationIDs = append(generationIDs, id)
		}
		byGeneration[id] = generation
	}

	q := foghorndb.New(db)
	open, err := q.ListNodeProjectedIngestSessionsBefore(ctx, foghorndb.ListNodeProjectedIngestSessionsBeforeParams{
		NodeID: nodeID, StartedBefore: cutoff,
	})
	if err != nil {
		return result, fmt.Errorf("list node ingest sessions: %w", err)
	}
	for _, session := range open {
		if _, held := byGeneration[session.SessionID]; held {
			result.Kept++
			continue
		}
		if !stillCurrent() {
			logger.WithField("node_id", nodeID).Info("Node re-registered again; leaving ingest reconciliation to the newer registration")
			return result, nil
		}
		fields := logging.Fields{
			"node_id": nodeID, "internal_name": session.StreamInternalName, "ingest_generation": session.SessionID,
		}
		ended, retireErr := RetireIngestSession(ctx, session.SessionID, session.TenantID, session.StreamInternalName, IngestEndedAbsentOnReregister, logger)
		if retireErr != nil {
			logger.WithError(retireErr).WithFields(fields).Warn("Failed to end ingest session the re-registered node no longer holds; the next registration retries")
			continue
		}
		if ended {
			result.Ended++
			logger.WithFields(fields).Info("Ended ingest session: the re-registered node does not hold its publisher")
		}
	}

	if len(generationIDs) == 0 {
		return result, nil
	}
	rows, err := q.ListNodeIngestGenerations(ctx, foghorndb.ListNodeIngestGenerationsParams{NodeID: nodeID, SessionIds: generationIDs})
	if err != nil {
		return result, fmt.Errorf("look up reported ingest generations: %w", err)
	}
	known := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		known[row.SessionID] = struct{}{}
		if !row.Ended {
			continue
		}
		report := byGeneration[row.SessionID]
		fields := logging.Fields{
			"node_id": nodeID, "internal_name": row.StreamInternalName, "ingest_generation": row.SessionID,
			"ended_reason": row.EndedReason, "active_generation": row.ActiveGeneration, "active_node_id": row.ActiveNodeID,
		}
		lostOwnership := row.EndedReason == IngestEndedSupersededByNewNode || row.EndedReason == ingestEndedPlacementClaimLost ||
			(row.ActiveGeneration != "" && row.ActiveGeneration != row.SessionID)
		if !lostOwnership {
			// The session ended on its own evidence (close, stream end, runtime absence). The node's
			// Mist absence reconciliation retires its local record; nothing else owns the stream.
			logger.WithFields(fields).Info("Node lists an ingest generation that already ended with no successor; leaving it to the node's runtime reconciliation")
			continue
		}
		runtimeName := strings.TrimSpace(report.GetRuntimeName())
		if !runtimeNameMatchesStream(runtimeName, row.StreamInternalName) {
			fields["runtime_name"] = runtimeName
			logger.WithFields(fields).Warn("Not stopping superseded ingest generation: reported runtime name does not match its stream")
			continue
		}
		if drain == nil {
			logger.WithFields(fields).Warn("Not stopping superseded ingest generation: no drain dispatcher configured")
			continue
		}
		if !stillCurrent() {
			logger.WithField("node_id", nodeID).Info("Node re-registered again; leaving ingest reconciliation to the newer registration")
			return result, nil
		}
		// The drain obligation's identity is the generation that replaced this one when there is
		// one, so the acknowledgement settles that generation's prior-owner drain leg. Helmsman
		// executes the drain only while its local runtime is still this exact generation.
		obligation := row.ActiveGeneration
		if obligation == "" {
			obligation = row.SessionID
		}
		reason := "ingest_ownership_lost:" + row.EndedReason
		if row.ActiveNodeID != "" {
			reason += ":now_on_node:" + row.ActiveNodeID
		}
		if sendErr := drain(ctx, nodeID, &ipcpb.DrainStreamRequest{
			RuntimeName: runtimeName, Reason: reason, SourceGeneration: obligation, PriorOwnerSourceGeneration: row.SessionID,
		}); sendErr != nil {
			logger.WithError(sendErr).WithFields(fields).Warn("Failed to tell the node to stop a superseded ingest generation; the next registration retries")
			continue
		}
		result.Stopped++
		logger.WithFields(fields).Warn("Told the re-registered node to stop its ingest publisher: another publisher took the stream over while the node was unreachable")
	}
	for _, id := range generationIDs {
		if _, ok := known[id]; !ok {
			logger.WithFields(logging.Fields{
				"node_id": nodeID, "runtime_name": byGeneration[id].GetRuntimeName(), "ingest_generation": id,
			}).Warn("Node lists an ingest generation this cell has no session for; leaving it to the node's runtime reconciliation")
		}
	}
	return result, nil
}

// scheduleNodeIngestReconcile runs ReconcileNodeIngestSessions for a registration that carries a
// generation inventory. It must be called from the Register handler before the receive loop reads
// the connection's next message: the cutoff is read synchronously so every PUSH_REWRITE on the new
// connection mints after it.
func scheduleNodeIngestReconcile(nodeID string, registered *conn, register *ipcpb.Register, logger logging.Logger) {
	if !register.GetLiveIngestGenerationsReported() {
		logger.WithField("node_id", nodeID).Info("Registration carries no live ingest inventory; the node's ingest sessions are kept as they are")
		return
	}
	cutoffCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	cutoff, err := IngestRegistrationCutoff(cutoffCtx)
	cancel()
	if err != nil {
		logger.WithError(err).WithField("node_id", nodeID).Warn("Skipping ingest session reconciliation for this registration: database clock unavailable")
		return
	}
	reported := register.GetLiveIngestGenerations()
	current := func() bool {
		registry.mu.RLock()
		defer registry.mu.RUnlock()
		return registry.conns[nodeID] == registered
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		result, err := ReconcileNodeIngestSessions(ctx, nodeID, reported, cutoff, SendDrainStream, current, logger)
		fields := logging.Fields{
			"node_id": nodeID, "reported": len(reported), "kept": result.Kept, "ended": result.Ended, "stopped": result.Stopped,
		}
		if err != nil {
			logger.WithError(err).WithFields(fields).Warn("Ingest session reconciliation for registration failed; the next registration retries")
			return
		}
		logger.WithFields(fields).Info("Reconciled node ingest sessions against its registration")
	}()
}

// runtimeNameMatchesStream accepts the bare internal name or a Mist wildcard runtime name
// ("live+<internal>") for it.
func runtimeNameMatchesStream(runtimeName, internalName string) bool {
	if runtimeName == "" || internalName == "" {
		return false
	}
	if runtimeName == internalName {
		return true
	}
	plus := strings.IndexByte(runtimeName, '+')
	return plus > 0 && runtimeName[plus+1:] == internalName
}
