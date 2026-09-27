package control

import (
	"context"
	"fmt"
	"strings"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

// IngestNodeLostAfter is how long a node with open ingest sessions may stay without a control
// connection to any replica, and without any evidence that it still publishes, before its sessions
// end as node_lost. It matches the lost-allocation windows of other schedulers (Nomad
// disconnect.lost_after, the Kubernetes 300 s not-ready toleration): long enough to ride out a
// control-plane partition or a Foghorn rollout, short enough that a crashed edge frees its streams.
const IngestNodeLostAfter = 5 * time.Minute

// ingestLifeEvidenceMaxAge bounds how old a pulled copy's stats may be to count as evidence. Helmsman
// reports stream stats every 10 s, so a copy not refreshed for a minute belongs to an edge that
// stopped reporting it.
const ingestLifeEvidenceMaxAge = time.Minute

// ingestNodeLostLeaseRole names the Redis lease that makes one replica the tracker of absent nodes.
// The absence clocks live in that replica's memory; a replica that gains the lease starts them anew.
const ingestNodeLostLeaseRole = "ingest-node-lost"

// NodePresenceFunc reports whether nodeID has a control connection to any replica in this cell (its
// conn_owner key exists). A non-nil error means presence is unknown and callers must not treat it as
// absence.
type NodePresenceFunc func(ctx context.Context, nodeID string) (present bool, err error)

// IngestLifeEvidenceFunc reports whether a connected node in this cell currently serves a live pulled
// copy of one of streams whose source is nodeID. With one publisher per stream such a copy can only be
// fed by that node, so it proves the node still publishes although its control connection is gone.
// witness names the node that reported the copy and stream the stream it copies.
type IngestLifeEvidenceFunc func(ctx context.Context, nodeID string, streams []string) (witness, stream string, found bool, err error)

// IngestNodeAbsence holds, per node, when the leader first saw the node without a control connection
// and without evidence of life. It lives only in the leader's memory: a new leader starts every clock
// again, which can only postpone an end.
type IngestNodeAbsence map[string]time.Time

// IngestNodeLostDeps are the lookups one lost-node pass consults.
type IngestNodeLostDeps struct {
	Present  NodePresenceFunc
	Guard    NodeRetireGuardFunc
	Evidence IngestLifeEvidenceFunc
}

type lostNodeSession struct {
	sessionID, tenantID, internalName string
}

// ReapLostNodeIngestSessionsOnce runs one lost-node pass over every open, projected ingest session.
// Per node with open sessions:
//
//   - a control connection on any replica, or unknown presence, clears nothing and keeps the node's
//     sessions (a lookup failure is logged and never read as absence);
//   - evidence of life (a connected edge serving a live pulled copy of one of its streams) restarts
//     the node's absence clock;
//   - otherwise the clock starts at the first such pass; once IngestNodeLostAfter has elapsed, the
//     node retirement guard is taken (it confirms conn_owner is still absent and keeps the node from
//     registering until release) and each session ends as node_lost through RetireIngestSession.
//
// absence is updated in place and pruned of nodes that no longer have open sessions. Returns the
// number of sessions ended.
func ReapLostNodeIngestSessionsOnce(ctx context.Context, deps IngestNodeLostDeps, absence IngestNodeAbsence, now time.Time, logger logging.Logger) (int, error) {
	if db == nil || absence == nil {
		return 0, nil
	}
	if deps.Present == nil || deps.Guard == nil || deps.Evidence == nil {
		return 0, fmt.Errorf("lost-node ingest pass requires presence, guard and evidence lookups")
	}
	// Cell-scoped administrative scan over Foghorn's own schema: the pass reconciles every node of the
	// cell, and each row's tenant_id scopes the retirement it drives.
	rows, err := foghorndb.New(db).ListOpenProjectedIngestSessions(ctx)
	if err != nil {
		return 0, fmt.Errorf("list open ingest sessions: %w", err)
	}
	byNode := make(map[string][]lostNodeSession)
	var nodes []string
	for _, row := range rows {
		if _, seen := byNode[row.NodeID]; !seen {
			nodes = append(nodes, row.NodeID)
		}
		byNode[row.NodeID] = append(byNode[row.NodeID], lostNodeSession{sessionID: row.SessionID, tenantID: row.TenantID, internalName: row.StreamInternalName})
	}
	for nodeID := range absence {
		if _, open := byNode[nodeID]; !open {
			delete(absence, nodeID)
		}
	}

	ended := 0
	for _, nodeID := range nodes {
		sessions := byNode[nodeID]
		present, presenceErr := deps.Present(ctx, nodeID)
		if presenceErr != nil {
			logger.WithError(presenceErr).WithField("node_id", nodeID).Warn("Lost-node ingest pass: node control presence unknown; keeping its sessions")
			continue
		}
		if present {
			delete(absence, nodeID)
			continue
		}
		streams := make([]string, 0, len(sessions))
		for _, s := range sessions {
			streams = append(streams, s.internalName)
		}
		witness, stream, alive, evidenceErr := deps.Evidence(ctx, nodeID, streams)
		if evidenceErr != nil {
			logger.WithError(evidenceErr).WithField("node_id", nodeID).Warn("Lost-node ingest pass: evidence lookup failed; keeping the node's sessions and its absence clock")
			continue
		}
		if alive {
			if first, tracked := absence[nodeID]; tracked {
				logger.WithFields(logging.Fields{
					"node_id": nodeID, "witness_node_id": witness, "internal_name": stream,
					"absent_for": now.Sub(first).String(),
				}).Info("Lost-node ingest pass: node without control connection still feeds a pulled copy; absence clock restarted")
			}
			absence[nodeID] = now
			continue
		}
		first, tracked := absence[nodeID]
		if !tracked {
			absence[nodeID] = now
			logger.WithFields(logging.Fields{"node_id": nodeID, "open_sessions": len(sessions)}).
				Info("Lost-node ingest pass: node has open ingest sessions and no control connection; absence clock started")
			continue
		}
		absentFor := now.Sub(first)
		if absentFor < IngestNodeLostAfter {
			continue
		}
		release, guardErr := deps.Guard(ctx, nodeID)
		if guardErr != nil {
			logger.WithError(guardErr).WithField("node_id", nodeID).Warn("Lost-node ingest pass: node retirement guard unavailable; keeping the node's sessions")
			continue
		}
		if release == nil {
			logger.WithField("node_id", nodeID).Info("Lost-node ingest pass: node registered again, or another retirement holds its guard; keeping its sessions")
			delete(absence, nodeID)
			continue
		}
		for _, s := range sessions {
			fields := logging.Fields{
				"node_id": nodeID, "internal_name": s.internalName, "ingest_generation": s.sessionID,
				"absent_for": absentFor.String(),
			}
			retired, retireErr := RetireIngestSession(ctx, s.sessionID, s.tenantID, s.internalName, IngestEndedNodeLost, logger)
			if retireErr != nil {
				logger.WithError(retireErr).WithFields(fields).Warn("Lost-node ingest pass: failed to end session of a lost node; the next pass retries")
				continue
			}
			if retired {
				ended++
				logger.WithFields(fields).Warn("Ended ingest session: its node has had no control connection and no evidence of life past the lost-node window")
			}
		}
		release()
		delete(absence, nodeID)
	}
	return ended, nil
}

// NodePresenceLookup is the production NodePresenceFunc over the conn_owner store.
func NodePresenceLookup(ctx context.Context, nodeID string) (bool, error) {
	rs := GetRedisStore()
	if rs == nil {
		return false, errConnOwnerUnavailable
	}
	owner, err := rs.GetConnOwner(ctx, nodeID)
	if err != nil {
		return false, err
	}
	return owner.InstanceID != "", nil
}

// IngestLifeEvidenceLookup is the production IngestLifeEvidenceFunc. A copy counts when it is on
// another node, is a replicated (pulled) instance with an input or a playable buffer, was reported
// within ingestLifeEvidenceMaxAge, and its node holds a control connection. A copy this cell pulls
// through the stream registry from a named source node other than nodeID is fed by someone else and
// does not count.
func IngestLifeEvidenceLookup(ctx context.Context, nodeID string, streams []string) (string, string, bool, error) {
	rs := GetRedisStore()
	if rs == nil {
		return "", "", false, errConnOwnerUnavailable
	}
	manager := state.DefaultManager()
	now := time.Now()
	presence := make(map[string]bool)
	for _, sessionStream := range streams {
		internalName := mist.ExtractInternalName(sessionStream)
		for witness, inst := range manager.GetStreamInstances(internalName) {
			if witness == nodeID || !inst.Replicated || (inst.Inputs <= 0 && !inst.Playable) {
				continue
			}
			if inst.LastUpdate.IsZero() || now.Sub(inst.LastUpdate) > ingestLifeEvidenceMaxAge {
				continue
			}
			if registry := StreamRegistryInstance; registry != nil {
				if pull, ok := registry.LocalReplicationForNode(ctx, internalName, witness); ok {
					if source := strings.TrimSpace(pull.PullSourceNodeID); source != "" && source != nodeID {
						continue
					}
				}
			}
			connected, known := presence[witness]
			if !known {
				owner, err := rs.GetConnOwner(ctx, witness)
				if err != nil {
					return "", "", false, err
				}
				connected = owner.InstanceID != ""
				presence[witness] = connected
			}
			if connected {
				return witness, internalName, true, nil
			}
		}
	}
	return "", "", false, nil
}

// IngestNodeLostLeaseHeld reports whether this replica holds (or has just taken) the lost-node
// tracking lease. The lease outlives two job intervals so a healthy leader keeps it between passes.
func IngestNodeLostLeaseHeld(ctx context.Context, ttl time.Duration) (bool, error) {
	rs := GetRedisStore()
	if rs == nil {
		return false, errConnOwnerUnavailable
	}
	return rs.TryAcquireLease(ctx, ingestNodeLostLeaseRole, GetInstanceID(), ttl)
}
