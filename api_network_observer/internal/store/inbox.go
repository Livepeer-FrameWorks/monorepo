package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lib/pq"
)

var (
	ErrConflict  = errors.New("event ID conflict")
	ErrInboxFull = errors.New("observation inbox capacity reached")
)

type InboxItem struct {
	ID, Observer, Kind string
	Payload            []byte
}

// Pending is called under the tenant's inventory lock; rank keeps each observer progressing.
func (s *Store) Pending(ctx context.Context, tx *sql.Tx) ([]InboxItem, error) {
	rows, err := tx.QueryContext(ctx, `SELECT i.id,i.observer_id,i.kind,i.payload FROM network_observer.observation_inbox i JOIN (
	 SELECT ranked.id,ranked.position,ranked.received_at,coalesce(l.last_processed_at,'epoch'::timestamptz) AS last_processed_at FROM (
	  SELECT id,observer_id,received_at,row_number() OVER(PARTITION BY observer_id ORDER BY received_at,id) AS position
	  FROM network_observer.observation_inbox WHERE tenant_id=$1 AND status='pending'
	 ) ranked LEFT JOIN network_observer.inbox_limits l ON l.tenant_id=$1 AND l.observer_id=ranked.observer_id
	 ORDER BY ranked.position,last_processed_at,ranked.received_at,ranked.id LIMIT 64
	) selected ON i.tenant_id=$1 AND i.id=selected.id ORDER BY selected.position,selected.last_processed_at,selected.received_at,i.id`, s.TenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []InboxItem
	for rows.Next() {
		var item InboxItem
		if err := rows.Scan(&item.ID, &item.Observer, &item.Kind, &item.Payload); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) Complete(ctx context.Context, tx *sql.Tx, items []InboxItem, codes []string) error {
	if len(items) != len(codes) {
		return fmt.Errorf("inbox disposition count mismatch")
	}
	if len(items) == 0 {
		return nil
	}
	ids := make([]string, len(items))
	type delta struct{ records, bytes int64 }
	deltas := map[string]delta{}
	for i, item := range items {
		ids[i] = item.ID
		for _, observer := range []string{"", item.Observer} {
			d := deltas[observer]
			d.records++
			d.bytes += int64(len(item.Payload))
			deltas[observer] = d
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE network_observer.observation_inbox i
	 SET status=CASE WHEN d.code='' THEN 'applied' ELSE 'rejected' END,error_code=d.code,
	 payload=NULL,completed_at=now()
	 FROM unnest($2::uuid[],$3::text[]) d(id,code) WHERE i.tenant_id=$1 AND i.id=d.id AND i.status='pending'`, s.TenantID, pq.Array(ids), pq.Array(codes)); err != nil {
		return err
	}
	var platform string
	if err := tx.QueryRowContext(ctx, "SELECT observer_id FROM network_observer.inbox_limits WHERE tenant_id=$1 AND observer_id='' FOR UPDATE", s.TenantID).Scan(&platform); err != nil {
		return err
	}
	var observers []string
	var records, bytes []int64
	for observer, d := range deltas {
		observers = append(observers, observer)
		records = append(records, d.records)
		bytes = append(bytes, d.bytes)
	}
	_, err := tx.ExecContext(ctx, `UPDATE network_observer.inbox_limits l SET pending_records=l.pending_records-d.records,pending_bytes=l.pending_bytes-d.bytes,last_processed_at=now()
	 FROM unnest($2::text[],$3::bigint[],$4::bigint[]) d(observer_id,records,bytes) WHERE l.tenant_id=$1 AND l.observer_id=d.observer_id`, s.TenantID, pq.Array(observers), pq.Array(records), pq.Array(bytes))
	return err
}

// Prune never removes a deduplication receipt while its observation is pending.
func (s *Store) Prune(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM network_observer.observation_inbox WHERE tenant_id=$1 AND completed_at<now()-interval '30 days' AND status!='pending'`, s.TenantID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM network_observer.ingest_receipts r WHERE r.tenant_id=$1 AND r.received_at<now()-interval '30 days' AND NOT EXISTS(SELECT 1 FROM network_observer.observation_inbox i WHERE i.tenant_id=$1 AND i.id=r.id)`, s.TenantID)
	return err
}
