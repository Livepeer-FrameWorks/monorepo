package ledger

import (
	"context"
	"database/sql"
)

// PruneResult counts the rows one Prune pass deleted.
type PruneResult struct {
	Events         int64
	TestDeliveries int64
	Notifications  int64
	Secrets        int64
}

// Prune deletes ledger rows older than Retention in batches of batchSize:
// events with their deliveries and attempts (cascaded), except an event that
// still has a pending delivery; test deliveries; sent notification emails;
// and expired previous signing secrets. It loops until a batch comes back
// short, so one call empties the backlog.
func (s *Store) Prune(ctx context.Context, batchSize int) (PruneResult, error) {
	if batchSize <= 0 {
		batchSize = 1000
	}
	var result PruneResult
	retention := Retention.Seconds()
	for {
		n, err := s.pruneEventBatch(ctx, retention, batchSize)
		if err != nil {
			return result, err
		}
		result.Events += n
		if n < int64(batchSize) || ctx.Err() != nil {
			break
		}
	}
	steps := []struct {
		counter *int64
		query   string
		args    []any
	}{
		{&result.TestDeliveries, `
			DELETE FROM bosun.webhook_deliveries d
			USING (
				SELECT tenant_id, id FROM bosun.webhook_deliveries
				WHERE kind = 'test' AND created_at < now() - make_interval(secs => $1)
				ORDER BY created_at
				LIMIT $2
			) old
			WHERE d.tenant_id = old.tenant_id AND d.id = old.id`, []any{retention, batchSize}},
		{&result.Notifications, `
			DELETE FROM bosun.webhook_notification_outbox n
			USING (
				SELECT tenant_id, id FROM bosun.webhook_notification_outbox
				WHERE completed_at IS NOT NULL AND completed_at < now() - make_interval(secs => $1)
				ORDER BY completed_at
				LIMIT $2
			) old
			WHERE n.tenant_id = old.tenant_id AND n.id = old.id`, []any{retention, batchSize}},
		{&result.Secrets, `
			DELETE FROM bosun.webhook_endpoint_secrets s
			USING (
				SELECT tenant_id, id FROM bosun.webhook_endpoint_secrets
				WHERE state = 'previous' AND expires_at <= now()
				LIMIT $1
			) old
			WHERE s.tenant_id = old.tenant_id AND s.id = old.id`, []any{batchSize}},
	}
	for _, step := range steps {
		for {
			res, err := s.DB.ExecContext(ctx, step.query, step.args...)
			if err != nil {
				return result, err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return result, err
			}
			*step.counter += n
			if n < int64(batchSize) || ctx.Err() != nil {
				break
			}
		}
	}
	return result, ctx.Err()
}

// pruneEventBatch deletes up to batchSize events older than retention whose
// deliveries are all finished, with their deliveries and attempts (cascaded).
// A replay returns a finished delivery to pending under its endpoint's row
// lock, so the batch follows the same order: it picks candidates, locks their
// endpoints, and only then deletes the candidates that still have no pending
// delivery. A replay that committed first keeps its event; one that waits for
// the lock finds its delivery gone.
func (s *Store) pruneEventBatch(ctx context.Context, retention float64, batchSize int) (int64, error) {
	var deleted int64
	txErr := s.inTx(ctx, func(tx *sql.Tx) error {
		deleted = 0
		tenants, eventIDs, err := pruneEventCandidates(ctx, tx, retention, batchSize)
		if err != nil {
			return err
		}
		if len(eventIDs) == 0 {
			return nil
		}
		tenantArray, eventArray := arrayLiteral(tenants), arrayLiteral(eventIDs)
		if _, err = tx.ExecContext(ctx, `
			SELECT e.id FROM bosun.webhook_endpoints e
			WHERE (e.tenant_id, e.id) IN (
				SELECT d.tenant_id, d.endpoint_id FROM bosun.webhook_deliveries d
				JOIN unnest((($1)::text)::uuid[], (($2)::text)::uuid[]) AS c(tenant_id, event_id)
				  ON d.tenant_id = c.tenant_id AND d.event_id = c.event_id)
			ORDER BY e.id
			FOR SHARE`, tenantArray, eventArray); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `
			DELETE FROM bosun.webhook_events e
			USING unnest((($1)::text)::uuid[], (($2)::text)::uuid[]) AS c(tenant_id, event_id)
			WHERE e.tenant_id = c.tenant_id AND e.event_id = c.event_id
			  AND NOT EXISTS (
				SELECT 1 FROM bosun.webhook_deliveries d
				WHERE d.tenant_id = e.tenant_id AND d.event_id = e.event_id AND d.status = 'pending')`,
			tenantArray, eventArray)
		if err != nil {
			return err
		}
		deleted, err = res.RowsAffected()
		return err
	})
	return deleted, txErr
}

// pruneEventCandidates returns, oldest first, up to batchSize events older
// than retention with no pending delivery, as parallel tenant and event ID
// slices.
func pruneEventCandidates(ctx context.Context, tx *sql.Tx, retention float64, batchSize int) ([]string, []string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT ev.tenant_id, ev.event_id FROM bosun.webhook_events ev
		WHERE ev.received_at < now() - make_interval(secs => $1)
		  AND NOT EXISTS (
			SELECT 1 FROM bosun.webhook_deliveries d
			WHERE d.tenant_id = ev.tenant_id AND d.event_id = ev.event_id AND d.status = 'pending')
		ORDER BY ev.received_at
		LIMIT $2`, retention, batchSize)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	var tenants, eventIDs []string
	for rows.Next() {
		var tenantID, eventID string
		if err := rows.Scan(&tenantID, &eventID); err != nil {
			return nil, nil, err
		}
		tenants = append(tenants, tenantID)
		eventIDs = append(eventIDs, eventID)
	}
	return tenants, eventIDs, rows.Err()
}
