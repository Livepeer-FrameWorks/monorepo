package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	"frameworks/api_balancing/internal/domainevents"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"
)

type PullIngestObservation struct {
	TenantID, StreamID, InternalName, NodeID, ClusterID, EventID string
	BufferPID, EventMillis                                       int64
}

// AdmitPullUpstream reserves one tenant stream slot before revealing an upstream.
// The database serializes this decision with push admission; several direct nodes
// for the same pull stream share its cell-wide slot.
func AdmitPullUpstream(ctx context.Context, tenantID, streamID, internal, node, cluster string, maxStreams int32) (bool, error) {
	if db == nil {
		return false, errors.New("pull admission store unavailable")
	}
	raw, err := foghorndb.New(db).AdmitPullUpstream(ctx, foghorndb.AdmitPullUpstreamParams{TenantID: tenantID, StreamID: streamID, InternalName: internal, NodeID: node, ClusterID: cluster, MaxStreams: maxStreams})
	if err != nil {
		return false, fmt.Errorf("admit pull upstream: %w", err)
	}
	var result struct {
		Admitted bool `json:"admitted"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return false, fmt.Errorf("decode pull admission: %w", err)
	}
	return result.Admitted, nil
}

// ObservePlayablePullBuffer binds a FULL edge or periodic level to its exact
// node/buffer lifetime. Ended lifetimes and stale node samples cannot mint again.
func ObservePlayablePullBuffer(ctx context.Context, o PullIngestObservation) (bool, error) {
	if db == nil {
		return false, errors.New("pull session store unavailable")
	}
	if _, err := uuid.Parse(o.TenantID); err != nil {
		return false, errors.New("pull observation requires tenant identity")
	}
	if _, err := uuid.Parse(o.StreamID); err != nil {
		return false, errors.New("pull observation requires stream identity")
	}
	if strings.TrimSpace(o.NodeID) == "" || strings.TrimSpace(o.InternalName) == "" || o.BufferPID <= 0 || o.EventMillis <= 0 {
		return false, errors.New("pull observation missing buffer identity or event time")
	}
	fromFull := o.EventID != ""
	if !fromFull {
		o.EventID = uuid.NewSHA1(uuid.NameSpaceURL, []byte(fmt.Sprintf("frameworks:pull-buffer:%s:%s:%s:%d", o.TenantID, o.NodeID, o.InternalName, o.BufferPID))).String()
	}
	if StreamRegistryInstance == nil {
		return false, errors.New("pull relay registry unavailable")
	}
	if _, relay, err := StreamRegistryInstance.CurrentInboundPull(ctx, o.InternalName, o.NodeID); err != nil || relay {
		return false, err
	}
	playbackID := streamEventPlaybackID(ctx, o.InternalName)
	minted, missingAdmission := false, false
	err := database.WithRetryablePostgresTx(ctx, db, nil, func(tx *sql.Tx) error {
		minted, missingAdmission = false, false
		q := foghorndb.New(tx)
		if err := q.LockIngestStream(ctx, ingestStreamAdvisoryLockKey(o.TenantID, o.InternalName)); err != nil {
			return err
		}
		known, err := q.PullSessionForBuffer(ctx, foghorndb.PullSessionForBufferParams{TenantID: o.TenantID, InternalName: o.InternalName, NodeID: o.NodeID, BufferPid: sql.NullInt64{Int64: o.BufferPID, Valid: true}})
		if err == nil {
			// PIDs can repeat after a node restart or wrap. A fresh FULL edge,
			// or a new source admission after retirement, identifies a new buffer;
			// a periodic sample alone cannot revive a retired process.
			newFull := fromFull && known.StartEventID != o.EventID && o.EventMillis > known.PlayableAtUnixMillis
			if (!known.Ended && !newFull) || (known.Ended && !known.HasNewAdmission && !newFull) {
				return nil
			}
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		latest, err := q.LatestPullNodeEnd(ctx, foghorndb.LatestPullNodeEndParams{TenantID: o.TenantID, InternalName: o.InternalName, NodeID: o.NodeID})
		if err != nil {
			return err
		}
		if o.EventMillis <= latest {
			return nil
		}
		replaced, err := q.EndSupersededPullBuffers(ctx, foghorndb.EndSupersededPullBuffersParams{TenantID: o.TenantID, InternalName: o.InternalName, NodeID: o.NodeID, BufferPid: o.BufferPID, EventMillis: o.EventMillis, NewBufferFull: fromFull, EventID: o.EventID})
		if err != nil {
			return err
		}
		active, err := q.CountActivePullStreamSessions(ctx, foghorndb.CountActivePullStreamSessionsParams{TenantID: o.TenantID, InternalName: o.InternalName})
		if err != nil {
			return err
		}
		if len(replaced) > 0 && active == 0 {
			if eventErr := domainevents.StreamIdle(ctx, tx, o.TenantID, o.StreamID, playbackID, replaced[0].CellSessionID); eventErr != nil {
				return eventErr
			}
		}
		cellSessionID, err := q.BindPendingPullBuffer(ctx, foghorndb.BindPendingPullBufferParams{TenantID: o.TenantID, InternalName: o.InternalName, NodeID: o.NodeID, BufferPid: o.BufferPID, EventID: o.EventID, EventMillis: o.EventMillis})
		if errors.Is(err, sql.ErrNoRows) {
			cellSessionID, err = q.InsertPlayablePullBuffer(ctx, foghorndb.InsertPlayablePullBufferParams{TenantID: o.TenantID, StreamID: o.StreamID, InternalName: o.InternalName, NodeID: o.NodeID, ClusterID: o.ClusterID, BufferPid: o.BufferPID, EventID: o.EventID, EventMillis: o.EventMillis})
			missingAdmission = err == nil
		}
		if err != nil {
			return err
		}
		if active == 0 {
			if err := domainevents.StreamConnected(ctx, tx, o.TenantID, o.StreamID, playbackID, domainevents.IngestProtocol("pull"), cellSessionID); err != nil {
				return err
			}
			if err := domainevents.StreamLive(ctx, tx, o.TenantID, o.StreamID, playbackID, cellSessionID); err != nil {
				return err
			}
		}
		minted = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("observe playable pull: %w", err)
	}
	if missingAdmission && controlMetrics != nil && controlMetrics.PullAdmissionMisses != nil {
		controlMetrics.PullAdmissionMisses.WithLabelValues().Inc()
	}
	return minted, nil
}

// EndPullIngestSessionsForStreamEnd ends only this node's older playable buffers.
// Idle and routing cleanup become owed only when the cell's last direct buffer ends.
func EndPullIngestSessionsForStreamEnd(ctx context.Context, tenant, node, internal string, eventMillis int64, logger logging.Logger) (int, error) {
	return endPullIngestSessionsForBufferEnd(ctx, tenant, node, internal, eventMillis, logger, true)
}

func endPullIngestSessionsForBufferEnd(ctx context.Context, tenant, node, internal string, eventMillis int64, logger logging.Logger, emitEnded bool) (int, error) {
	if db == nil || eventMillis <= 0 {
		return 0, nil
	}
	playbackID := streamEventPlaybackID(ctx, internal)
	count := 0
	var claims []DVRStopClaim
	err := database.WithRetryablePostgresTx(ctx, db, nil, func(tx *sql.Tx) error {
		count = 0
		claims = nil
		q := foghorndb.New(tx)
		if err := q.LockIngestStream(ctx, ingestStreamAdvisoryLockKey(tenant, internal)); err != nil {
			return err
		}
		ended, err := q.EndPullBuffersForEvent(ctx, foghorndb.EndPullBuffersForEventParams{TenantID: tenant, NodeID: node, InternalName: internal, EventMillis: eventMillis})
		if err != nil {
			return err
		}
		if len(ended) == 0 {
			if emitEnded {
				return markStreamBufferEndedTx(ctx, tx, tenant, node, internal, eventMillis, true, playbackID)
			}
			return nil
		}
		claims, err = ClaimDVRStops(ctx, tx, endedSourceDVRWhereSQL, internal, node, tenant)
		if err != nil {
			return err
		}
		if err := finishPullRetirementTx(ctx, tx, tenant, ended[0].StreamID, internal, node, playbackID, ended[0].CellSessionID); err != nil {
			return err
		}
		count = len(ended)
		if emitEnded {
			return markStreamBufferEndedTx(ctx, tx, tenant, node, internal, eventMillis, true, playbackID)
		}
		return nil
	})
	if err == nil {
		DispatchDVRStops(claims, logger)
	}
	return count, err
}

func finishPullRetirementTx(ctx context.Context, tx *sql.Tx, tenant, stream, internal, node, playbackID, cellSessionID string) error {
	active, err := foghorndb.New(tx).CountActivePullStreamSessions(ctx, foghorndb.CountActivePullStreamSessionsParams{TenantID: tenant, InternalName: internal})
	if err != nil || active > 0 {
		return err
	}
	if eventErr := domainevents.StreamIdle(ctx, tx, tenant, stream, playbackID, cellSessionID); eventErr != nil {
		return eventErr
	}
	revision, err := nextSourceRevision(ctx, tx, tenant, internal)
	if err != nil {
		return err
	}
	return enqueueOfflineEffectTx(ctx, tx, tenant, internal, node, "", revision, OfflineEffectIntent{SetNodeOffline: true, BroadcastOffline: true})
}

func RetirePullIngestSession(ctx context.Context, tenant, internal, generation, reason string, pendingOnly bool, logger logging.Logger) (bool, error) {
	if db == nil {
		return false, errors.New("pull retirement store unavailable")
	}
	playbackID := streamEventPlaybackID(ctx, internal)
	retired := false
	var claims []DVRStopClaim
	err := database.WithRetryablePostgresTx(ctx, db, nil, func(tx *sql.Tx) error {
		retired = false
		claims = nil
		q := foghorndb.New(tx)
		if err := q.LockIngestStream(ctx, ingestStreamAdvisoryLockKey(tenant, internal)); err != nil {
			return err
		}
		ended, err := q.RetirePullIngestSession(ctx, foghorndb.RetirePullIngestSessionParams{TenantID: tenant, InternalName: internal, SessionID: generation, Reason: sql.NullString{String: reason, Valid: true}, EventMillis: time.Now().UnixMilli(), PendingOnly: pendingOnly})
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if ended.AdmissionState == "active" {
			claims, err = ClaimDVRStops(ctx, tx, endedSourceDVRWhereSQL, internal, ended.NodeID, tenant)
			if err != nil {
				return err
			}
			if err := finishPullRetirementTx(ctx, tx, tenant, ended.StreamID, internal, ended.NodeID, playbackID, ended.CellSessionID); err != nil {
				return err
			}
		}
		retired = true
		return nil
	})
	if err == nil {
		DispatchDVRStops(claims, logger)
	}
	return retired, err
}

// ReconcileNodePullSessions uses an authoritative registration inventory, never
// a heartbeat omission, to retire a buffer the restarted edge no longer holds.
func ReconcileNodePullSessions(ctx context.Context, node string, buffers []*ipcpb.LiveSourceBuffer, cutoff time.Time, current func() bool, logger logging.Logger) (int, error) {
	if db == nil {
		return 0, errors.New("pull reconciliation store unavailable")
	}
	held := make(map[string]map[int64]bool)
	for _, buffer := range buffers {
		if !strings.HasPrefix(buffer.GetRuntimeName(), "pull+") || buffer.GetBufferPid() <= 0 {
			continue
		}
		internal := strings.TrimPrefix(buffer.GetRuntimeName(), "pull+")
		if held[internal] == nil {
			held[internal] = make(map[int64]bool)
		}
		held[internal][buffer.GetBufferPid()] = true
	}
	rows, err := foghorndb.New(db).ListNodeOpenPullSessionsBefore(ctx, foghorndb.ListNodeOpenPullSessionsBeforeParams{NodeID: node, CutoffMillis: cutoff.UnixMilli()})
	if err != nil {
		return 0, err
	}
	ended := 0
	for _, row := range rows {
		if current != nil && !current() {
			break
		}
		if row.BufferPid.Valid && held[row.StreamInternalName][row.BufferPid.Int64] {
			continue
		}
		done, err := RetirePullIngestSession(ctx, row.TenantID, row.StreamInternalName, row.ID, IngestEndedAbsentOnReregister, false, logger)
		if err != nil {
			return ended, err
		}
		if done {
			ended++
		}
	}
	return ended, nil
}

func ReapLapsedPullAdmissions(ctx context.Context, logger logging.Logger) error {
	if db == nil {
		return nil
	}
	rows, err := foghorndb.New(db).ListLapsedPullAdmissions(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := RetirePullIngestSession(ctx, row.TenantID, row.StreamInternalName, row.ID, "admission_lapsed", true, logger); err != nil {
			logger.WithError(err).WithField("internal_name", row.StreamInternalName).Warn("Failed to retire lapsed pull admission")
		}
	}
	return nil
}
