package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

type Receipt struct {
	ID, Digest, Observer, Kind string
	Payload                    []byte
}

func (s *Store) Receive(ctx context.Context, id, digest, observer, kind string, raw []byte) error {
	codes, err := s.ReceiveMany(ctx, []Receipt{{ID: id, Digest: digest, Observer: observer, Kind: kind, Payload: raw}})
	if err != nil {
		return err
	}
	if codes[0] != "" {
		return ErrConflict
	}
	return nil
}

// ReceiveMany amortizes cross-region delivery and quota locking without changing record bytes.
func (s *Store) ReceiveMany(ctx context.Context, records []Receipt) ([]string, error) {
	codes := make([]string, len(records))
	unique := map[string]Receipt{}
	observer := ""
	for i, record := range records {
		id, err := uuid.Parse(record.ID)
		if err != nil {
			return nil, err
		}
		record.ID = id.String()
		records[i].ID = record.ID
		if record.Observer == "" {
			return nil, errors.New("missing observer")
		}
		if observer == "" {
			observer = record.Observer
		}
		if observer != record.Observer {
			return nil, errors.New("mixed observers")
		}
		if _, ok := unique[record.ID]; !ok {
			unique[record.ID] = record
		}
	}
	var ids, digests []string
	for id := range unique {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		digests = append(digests, unique[id].Digest)
	}
	if len(ids) == 0 {
		return codes, nil
	}
	err := database.WithRetryablePostgresTx(ctx, s.DB, nil, func(tx *sql.Tx) error {
		for i := range codes {
			codes[i] = ""
		}
		inserted, err := s.insertReceipts(ctx, tx, ids, digests)
		if err != nil {
			return err
		}
		existing := map[string]string{}
		var previousIDs []string
		for _, id := range ids {
			if !inserted[id] {
				previousIDs = append(previousIDs, id)
			}
		}
		if len(previousIDs) > 0 {
			existing, err = s.receiptDigests(ctx, tx, previousIDs)
			if err != nil {
				return err
			}
			if len(existing) != len(previousIDs) {
				return errors.New("receipt expired during retry; retry original records")
			}
		}
		for i, record := range records {
			canonical := existing[record.ID]
			if inserted[record.ID] {
				canonical = unique[record.ID].Digest
			}
			if canonical != record.Digest {
				codes[i] = "EVENT_ID_CONFLICT"
			}
		}
		var addedIDs, observers, kinds []string
		var payloads pq.ByteaArray
		var bytes int64
		for _, id := range ids {
			if !inserted[id] {
				continue
			}
			record := unique[id]
			addedIDs = append(addedIDs, id)
			observers = append(observers, record.Observer)
			kinds = append(kinds, record.Kind)
			payloads = append(payloads, record.Payload)
			bytes += int64(len(record.Payload))
		}
		if len(addedIDs) == 0 {
			return nil
		}
		for _, budget := range []struct {
			id          string
			rows, bytes int64
		}{{"", 100000, 128 << 20}, {observer, 20000, 32 << 20}} {
			var accepted string
			quotaErr := tx.QueryRowContext(ctx, `INSERT INTO network_observer.inbox_limits(tenant_id,observer_id,pending_records,pending_bytes)
			 VALUES($1,$2,$3,$4) ON CONFLICT(tenant_id,observer_id) DO UPDATE
			 SET pending_records=network_observer.inbox_limits.pending_records+EXCLUDED.pending_records,pending_bytes=network_observer.inbox_limits.pending_bytes+EXCLUDED.pending_bytes
			 WHERE network_observer.inbox_limits.pending_records+EXCLUDED.pending_records<=$5 AND network_observer.inbox_limits.pending_bytes+EXCLUDED.pending_bytes<=$6
			 RETURNING observer_id`, s.TenantID, budget.id, len(addedIDs), bytes, budget.rows, budget.bytes).Scan(&accepted)
			if errors.Is(quotaErr, sql.ErrNoRows) {
				return ErrInboxFull
			}
			if quotaErr != nil {
				return quotaErr
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO network_observer.observation_inbox(tenant_id,id,observer_id,kind,payload)
		 SELECT $1,r.id,r.observer_id,r.kind,r.payload FROM unnest($2::uuid[],$3::text[],$4::text[],$5::bytea[]) r(id,observer_id,kind,payload)`, s.TenantID, pq.Array(addedIDs), pq.Array(observers), pq.Array(kinds), payloads)
		return err
	})
	return codes, err
}

func (s *Store) insertReceipts(ctx context.Context, tx *sql.Tx, ids, digests []string) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `INSERT INTO network_observer.ingest_receipts(tenant_id,id,digest)
 SELECT $1,r.id,r.digest FROM unnest($2::uuid[],$3::text[]) r(id,digest) ORDER BY r.id
 ON CONFLICT(tenant_id,id) DO NOTHING RETURNING id`, s.TenantID, pq.Array(ids), pq.Array(digests))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	inserted := map[string]bool{}
	for rows.Next() {
		var id string
		if scanErr := rows.Scan(&id); scanErr != nil {
			return nil, scanErr
		}
		inserted[id] = true
	}
	return inserted, rows.Err()
}
func (s *Store) receiptDigests(ctx context.Context, tx *sql.Tx, ids []string) (map[string]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id,digest FROM network_observer.ingest_receipts WHERE tenant_id=$1 AND id=ANY($2::uuid[])", s.TenantID, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]string{}
	for rows.Next() {
		var id, digest string
		if scanErr := rows.Scan(&id, &digest); scanErr != nil {
			return nil, scanErr
		}
		result[id] = digest
	}
	return result, rows.Err()
}
