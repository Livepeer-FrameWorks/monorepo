package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"frameworks/api_balancing/internal/database/foghorndb"
	"frameworks/api_balancing/internal/domainevents"
)

// MarkIngestSessionPlayable records the first playable buffer of the stream's
// active ingest session and enqueues stream.live in the same transaction. Only
// a buffer from the session's own node matches, and only once per session, so
// replica nodes and repeated or replayed buffer triggers change nothing and
// emit nothing. eventMillis fences a delayed trigger from an earlier session on
// the same node; zero disables the fence. It reports whether this call marked
// the session.
func MarkIngestSessionPlayable(ctx context.Context, tenantID, nodeID, internalName string, eventMillis int64) (bool, error) {
	if db == nil {
		return false, errors.New("mark ingest session playable: no database configured")
	}
	if tenantID == "" || nodeID == "" || internalName == "" {
		return false, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin mark ingest session playable: %w", err)
	}
	defer rollbackQuiet(tx)
	marked, err := foghorndb.New(tx).MarkIngestSessionPlayable(ctx, foghorndb.MarkIngestSessionPlayableParams{
		TenantID: tenantID, StreamInternalName: internalName, NodeID: nodeID, EventUnixMillis: eventMillis,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("mark ingest session playable: %w", err)
	}
	if err := domainevents.StreamLive(ctx, tx, tenantID, marked.StreamID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit ingest session playable: %w", err)
	}
	return true, nil
}
