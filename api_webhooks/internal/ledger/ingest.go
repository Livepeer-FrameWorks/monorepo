package ledger

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
)

// IncomingEvent is a public tenant event read from domain.events.
type IncomingEvent struct {
	ID         string
	TenantID   string
	Type       string
	SchemaName string
	Subject    string
	Payload    []byte
	OccurredAt time.Time
}

// RecordResult says what RecordEvent stored.
type RecordResult struct {
	// NoSubscriber is true when no endpoint receives the event; nothing was
	// written.
	NoSubscriber bool
	// Duplicate is true when the event ID was already stored, from the local
	// topic, a mirrored copy, or a redelivery; nothing was written.
	Duplicate bool
	// Deliveries is the number of deliveries created.
	Deliveries int
}

// RecordEvent stores the event and one pending delivery per enabled endpoint
// of its tenant that subscribes to its type and was created no later than the
// event occurred, in one transaction. An event no endpoint receives is not
// stored, and an event ID already stored writes nothing. The caller commits
// its Kafka offset only after this returns nil.
//
// The subscribing endpoints are read FOR SHARE, so disabling one waits for
// this transaction and then skips the deliveries it created.
func (s *Store) RecordEvent(ctx context.Context, ev IncomingEvent) (RecordResult, error) {
	var result RecordResult
	txErr := s.inTx(ctx, func(tx *sql.Tx) error {
		result = RecordResult{}
		endpointIDs, err := subscribedEndpoints(ctx, tx, ev)
		if err != nil {
			return err
		}
		if len(endpointIDs) == 0 {
			result.NoSubscriber = true
			return nil
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO bosun.webhook_events (tenant_id, event_id, event_type, schema_name, subject, payload, occurred_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT DO NOTHING`,
			ev.TenantID, ev.ID, ev.Type, ev.SchemaName, ev.Subject, ev.Payload, ev.OccurredAt.UTC())
		if err != nil {
			return err
		}
		inserted, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if inserted == 0 {
			result.Duplicate = true
			return nil
		}
		for _, endpointID := range endpointIDs {
			if _, err = tx.ExecContext(ctx, `
				INSERT INTO bosun.webhook_deliveries (id, tenant_id, endpoint_id, event_id, event_type, kind)
				VALUES ($1, $2, $3, $4, $5, 'event')
				ON CONFLICT DO NOTHING`,
				uuid.Must(uuid.NewV7()).String(), ev.TenantID, endpointID, ev.ID, ev.Type); err != nil {
				return err
			}
		}
		result.Deliveries = len(endpointIDs)
		return nil
	})
	return result, txErr
}

// subscribedEndpoints returns, share-locked, the enabled endpoints of the
// event's tenant that subscribe to its type and existed when it occurred.
func subscribedEndpoints(ctx context.Context, tx *sql.Tx, ev IncomingEvent) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id FROM bosun.webhook_endpoints
		WHERE tenant_id = $1 AND status = 'enabled' AND created_at <= $2
		  AND ($3 = ANY(event_types) OR '*' = ANY(event_types))
		ORDER BY id
		FOR SHARE`, ev.TenantID, ev.OccurredAt.UTC(), ev.Type)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
