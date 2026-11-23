package coordinator

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"

	"frameworks/api_network_observer/internal/store"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

func (a *API) Gateway(ctx context.Context, message kafka.Message) error {
	var event kafka.AnalyticsEvent
	if err := json.Unmarshal(message.Value, &event); err != nil {
		return err
	}
	if event.EventType != "orchestrator_discovery_observed" && event.EventType != "orchestrator_state_update" && event.EventType != "orchestrator_observer_report" {
		return nil
	}
	d := event.Data
	if d["producer_kind"] != "gateway" || fmt.Sprint(d["cluster_owner_tenant_id"]) != a.Store.TenantID || fmt.Sprint(d["network_id"]) != a.Engine.Network || fmt.Sprint(d["environment"]) != a.Engine.Environment {
		return nil
	}
	if _, err := uuid.Parse(event.EventID); err != nil {
		return err
	}
	id, ok := d["gateway_id"].(string)
	if !ok || id == "" || len(id) > 128 {
		return errors.New("invalid gateway identity")
	}
	sum := sha256.Sum256(message.Value)
	return a.Store.Receive(ctx, event.EventID, hex.EncodeToString(sum[:]), "gateway:"+id, "gateway", message.Value)
}

// Process applies a fair group atomically with its outbox and final dispositions.
func (a *API) Process(ctx context.Context) error { _, err := a.ProcessBatch(ctx); return err }

func (a *API) ProcessBatch(ctx context.Context) (int, error) {
	count := 0
	err := a.Store.Update(ctx, func(s *store.State, tx *sql.Tx) error {
		items, err := a.Store.Pending(ctx, tx)
		if err != nil {
			return err
		}
		count = len(items)
		codes := make([]string, len(items))
		var events []encodedEvent
		now := time.Now().UTC()
		for i, item := range items {
			if item.Kind == "gateway" {
				var event kafka.AnalyticsEvent
				if json.Unmarshal(item.Payload, &event) != nil {
					codes[i] = "GATEWAY_PAYLOAD_INVALID"
					continue
				}
				if err := a.applyGateway(s, event); err != nil {
					codes[i] = rejectionCode(err)
				}
				continue
			}
			var b obs.Batch
			if json.Unmarshal(item.Payload, &b) != nil {
				codes[i] = "BATCH_INVALID"
				continue
			}
			view := *s
			view.Assignments = maps.Clone(s.Assignments)
			if err := a.resolveAssignments(&view, b, now); err != nil {
				codes[i] = rejectionCode(err)
				continue
			}
			if err := a.Engine.Validate(&view, b, now); err != nil {
				codes[i] = rejectionCode(err)
				continue
			}
			encoded, err := a.encodeBatch(&view, b)
			if err != nil {
				codes[i] = "TELEMETRY_INVALID"
				continue
			}
			// Page by expanded output before mutation; even an empty outbox
			// cannot accept an unbounded expansion of individually valid records.
			if len(events) > 0 && len(events)+len(encoded) > 4096 {
				items, codes = items[:i], codes[:i]
				count = i
				break
			}
			if err := a.Engine.Apply(&view, b, now); err != nil {
				return err
			}
			s.Assignments = view.Assignments
			events = append(events, encoded...)
		}
		if len(events) > 0 {
			var outboxCount int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM network_observer.telemetry_outbox WHERE tenant_id=$1", a.Store.TenantID).Scan(&outboxCount); err != nil {
				return err
			}
			if outboxCount+len(events) > 100000 {
				return errors.New("telemetry outbox capacity reached")
			}
			ids := make([]string, len(events))
			payloads := make(pq.ByteaArray, len(events))
			for i, event := range events {
				ids[i] = event.ID
				payloads[i] = event.Payload
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO network_observer.telemetry_outbox(tenant_id,id,payload)
			 SELECT $1,e.id,e.payload FROM unnest($2::uuid[],$3::bytea[]) e(id,payload) ON CONFLICT(tenant_id,id) DO NOTHING`, a.Store.TenantID, pq.Array(ids), payloads); err != nil {
				return err
			}
		}
		if err := a.Store.Complete(ctx, tx, items, codes); err != nil {
			return err
		}
		a.Engine.Schedule(s, now)
		return nil
	})
	return count, err
}
