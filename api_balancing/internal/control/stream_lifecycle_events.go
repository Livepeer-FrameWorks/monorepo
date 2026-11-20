package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	"frameworks/api_balancing/internal/domainevents"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

// streamEventLookupTimeout bounds the registry lookup of a stream event's
// playback ID, which can fall through to Commodore on a cache miss.
const streamEventLookupTimeout = 2 * time.Second

// streamEventPlaybackID returns the public playback ID for the stream events of
// the source internalName. Admission records it in the shared stream registry,
// which every replica (including the reapers') reads. It is looked up before the
// event's transaction opens, and an unresolvable stream yields an empty ID:
// the lifecycle transition never waits on or fails for it.
func streamEventPlaybackID(ctx context.Context, internalName string) string {
	registry := StreamRegistryInstance
	if registry == nil || internalName == "" {
		return ""
	}
	lookupCtx, cancel := context.WithTimeout(ctx, streamEventLookupTimeout)
	defer cancel()
	entry, err := registry.ResolveSourceByInternalName(lookupCtx, internalName)
	if err != nil {
		return ""
	}
	return entry.PlaybackID
}

// MarkIngestSessionPlayable records the first playable buffer of the stream's
// active ingest session and enqueues stream.live in the same transaction. Only
// a buffer from the session's own node matches, and only once per session, so
// replica nodes and repeated or replayed buffer triggers change nothing and
// emit nothing. eventMillis fences a delayed trigger from an earlier session on
// the same node; zero disables the fence and records no playable event time,
// which leaves the session out of the STREAM_END reaper. It reports whether
// this call marked the session.
func MarkIngestSessionPlayable(ctx context.Context, tenantID, nodeID, internalName string, eventMillis int64, generation ...string) (bool, error) {
	if len(generation) > 1 {
		return false, errors.New("ambiguous publisher generation")
	}
	if db == nil {
		return false, errors.New("mark ingest session playable: no database configured")
	}
	if tenantID == "" || nodeID == "" || internalName == "" {
		return false, nil
	}
	marked := false
	playbackID := streamEventPlaybackID(ctx, internalName)
	err := database.WithRetryablePostgresTx(ctx, db, nil, func(tx *sql.Tx) error {
		var markedRow foghorndb.MarkIngestSessionPlayableRow
		var err error
		if len(generation) == 1 && generation[0] != "" {
			bound, markErr := foghorndb.New(tx).MarkBoundIngestSessionPlayable(ctx, foghorndb.MarkBoundIngestSessionPlayableParams{
				TenantID: tenantID, StreamInternalName: internalName, NodeID: nodeID, EventUnixMillis: eventMillis, Generation: generation[0]})
			markedRow.StreamID, markedRow.SessionID, err = bound.StreamID, bound.SessionID, markErr
		} else {
			markedRow, err = foghorndb.New(tx).MarkIngestSessionPlayable(ctx, foghorndb.MarkIngestSessionPlayableParams{
				TenantID: tenantID, StreamInternalName: internalName, NodeID: nodeID, EventUnixMillis: eventMillis})
		}
		if errors.Is(err, sql.ErrNoRows) {
			return errTxRollbackNoop
		}
		if err != nil {
			return fmt.Errorf("mark ingest session playable: %w", err)
		}
		// A session admitted without a public stream ID records its playable time for the
		// STREAM_END reaper but emits no stream.live, matching its absent stream.connected.
		if markedRow.StreamID != "" {
			if err := domainevents.StreamLive(ctx, tx, tenantID, markedRow.StreamID, playbackID, markedRow.SessionID); err != nil {
				return err
			}
		}
		marked = true
		return nil
	})
	if errors.Is(err, errTxRollbackNoop) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("commit ingest session playable: %w", err)
	}
	return marked, nil
}

// markStreamBufferEndedTx runs under the admission lock. The durable marker and
// outbox insert commit together, so retries cannot duplicate or end a newer session.
func markStreamBufferEndedTx(ctx context.Context, tx *sql.Tx, tenant, node, internal string, eventMillis int64, pull bool, playbackID string) error {
	q := foghorndb.New(tx)
	var streamID, sessionID string
	if pull {
		row, err := q.MarkPullBufferEnded(ctx, foghorndb.MarkPullBufferEndedParams{TenantID: tenant, NodeID: node, InternalName: internal, EventMillis: eventMillis})
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		streamID, sessionID = row.StreamID, row.SessionID
	} else {
		row, err := q.MarkPushBufferEnded(ctx, foghorndb.MarkPushBufferEndedParams{TenantID: tenant, NodeID: node, InternalName: internal, EventMillis: eventMillis})
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		streamID, sessionID = row.StreamID, row.SessionID
	}
	if streamID == "" {
		return nil
	}
	return domainevents.StreamEnded(ctx, tx, tenant, streamID, playbackID, sessionID)
}
