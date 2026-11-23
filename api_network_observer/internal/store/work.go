package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
)

func (s *Store) Poll(ctx context.Context, request obs.WorkRequest, ip string, location obs.Location, maxObservers int, now time.Time) (obs.Work, error) {
	work := obs.Work{Catalog: []obs.Resource{}, Assignments: []obs.Assignment{}, DNSIntervalSeconds: 300, ServerTime: now, ControlVersion: 2, IngestBatchRecords: 64, IngestBatchBytes: 380 << 10}
	err := database.WithRetryablePostgresTx(ctx, s.DB, nil, func(tx *sql.Tx) error {
		var raw []byte
		var observer obs.Observer
		err := tx.QueryRowContext(ctx, "SELECT document FROM network_observer.observers WHERE tenant_id=$1 AND id=$2 FOR UPDATE", s.TenantID, request.ObserverID).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			// Only registration shares the short quota-row lock with receipts.
			if _, insertErr := tx.ExecContext(ctx, `INSERT INTO network_observer.inbox_limits(tenant_id,observer_id) VALUES($1,'') ON CONFLICT DO NOTHING`, s.TenantID); insertErr != nil {
				return insertErr
			}
			var budget string
			if lockErr := tx.QueryRowContext(ctx, "SELECT observer_id FROM network_observer.inbox_limits WHERE tenant_id=$1 AND observer_id='' FOR UPDATE", s.TenantID).Scan(&budget); lockErr != nil {
				return lockErr
			}
			// Another first poll for this identity may have registered it while we waited.
			err = tx.QueryRowContext(ctx, "SELECT document FROM network_observer.observers WHERE tenant_id=$1 AND id=$2 FOR UPDATE", s.TenantID, request.ObserverID).Scan(&raw)
			if errors.Is(err, sql.ErrNoRows) {
				var count int
				if countErr := tx.QueryRowContext(ctx, "SELECT count(*) FROM network_observer.observers WHERE tenant_id=$1 AND document->>'kind'='prober'", s.TenantID).Scan(&count); countErr != nil {
					return countErr
				}
				if count >= maxObservers {
					return errors.New("observer limit")
				}
				observer = obs.Observer{ID: request.ObserverID, Kind: "prober"}
			} else if err != nil {
				return err
			} else if err = json.Unmarshal(raw, &observer); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if err = json.Unmarshal(raw, &observer); err != nil {
			return err
		}
		observer.Region = request.Region
		observer.LastSeen = now
		if location.Valid() || observer.Location.Source == "configured" || observer.SourceIP != ip || !observer.Location.Valid() {
			if observer.Location != location || observer.SourceIP != ip {
				observer.LocationUpdatedAt = now
			}
			observer.Location = location
		}
		observer.SourceIP = ip
		if request.Delivery != nil && (observer.Delivery == nil || request.Delivery.Sequence > observer.Delivery.Sequence) {
			observer.Delivery = request.Delivery
		}
		encoded, err := json.Marshal(observer)
		if err != nil {
			return err
		}
		if _, writeErr := tx.ExecContext(ctx, "INSERT INTO network_observer.observers(tenant_id,id,document) VALUES($1,$2,$3::jsonb) ON CONFLICT(tenant_id,id) DO UPDATE SET document=EXCLUDED.document", s.TenantID, request.ObserverID, string(encoded)); writeErr != nil {
			return writeErr
		}
		work.Catalog, err = s.workCatalog(ctx, tx)
		if err != nil {
			return err
		}
		work.Assignments, err = s.workAssignments(ctx, tx, request.ObserverID, now)
		return err
	})
	return work, err
}

func (s *Store) workCatalog(ctx context.Context, tx *sql.Tx) ([]obs.Resource, error) {
	rows, err := tx.QueryContext(ctx, "SELECT document FROM network_observer.resources WHERE tenant_id=$1 AND document->>'active'='true' ORDER BY id", s.TenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []obs.Resource{}
	for rows.Next() {
		var raw []byte
		var resource obs.Resource
		if scanErr := rows.Scan(&raw); scanErr != nil {
			return nil, scanErr
		}
		if decodeErr := json.Unmarshal(raw, &resource); decodeErr != nil {
			return nil, decodeErr
		}
		result = append(result, resource)
	}
	return result, rows.Err()
}
func (s *Store) workAssignments(ctx context.Context, tx *sql.Tx, observerID string, now time.Time) ([]obs.Assignment, error) {
	rows, err := tx.QueryContext(ctx, "SELECT document FROM network_observer.assignments WHERE tenant_id=$1 AND document->>'observer_id'=$2 AND document->>'completed'='false' AND (document->>'deadline')::timestamptz>$3 ORDER BY id", s.TenantID, observerID, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []obs.Assignment{}
	for rows.Next() {
		var raw []byte
		var assignment obs.Assignment
		if scanErr := rows.Scan(&raw); scanErr != nil {
			return nil, scanErr
		}
		if decodeErr := json.Unmarshal(raw, &assignment); decodeErr != nil {
			return nil, decodeErr
		}
		result = append(result, assignment)
	}
	return result, rows.Err()
}
