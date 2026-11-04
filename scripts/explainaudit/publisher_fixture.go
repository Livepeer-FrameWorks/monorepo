package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"time"
)

// These JSON entry points require an execution identity and predecessor state.
// An empty object can either fail validation or successfully explain a no-op.
type publisherFixture struct {
	name, tenant, streamID, node, execution, generation, eventID string
	now                                                          time.Time
}

func publisherFixtureFor(queryID string, now time.Time) *publisherFixture {
	switch queryID {
	case "foghorn:PreparePublisherAdmission", "foghorn:ConfirmPublisherAdmission", "foghorn:SettlePublisherAdmission", "foghorn:MarkPublisherLeasesReleased":
	default:
		return nil
	}
	rng := rand.New(rand.NewSource(int64(hashString("publisher fixture:" + queryID))))
	return &publisherFixture{name: strings.TrimPrefix(queryID, "foghorn:"), tenant: uuidFrom(rng), streamID: uuidFrom(rng),
		node: "audit-" + uuidFrom(rng), execution: uuidFrom(rng), generation: uuidFrom(rng), eventID: uuidFrom(rng), now: now}
}

func (f *publisherFixture) identity() map[string]any {
	return map[string]any{"tenant_id": f.tenant, "stream_id": f.streamID, "internal_name": "audit-" + f.streamID,
		"node_id": f.node, "trigger_uuid": f.execution, "generation": f.generation}
}

func (f *publisherFixture) event(kind string) map[string]any {
	// Both stream events encode stream_id as protobuf field 1 (length-delimited).
	payload := append([]byte{0x0a, byte(len(f.streamID))}, []byte(f.streamID)...)
	return map[string]any{"ID": f.eventID, "TenantID": f.tenant, "AggregateID": f.streamID, "Source": "foghorn",
		"Type": kind, "AggregateVersion": 1, "Time": f.now.Format(time.RFC3339Nano), "Actor": map[string]any{},
		"Data": base64.StdEncoding.EncodeToString(payload)}
}

func (f *publisherFixture) prepareRequest() map[string]any {
	p := f.identity()
	p["connector_pid"], p["started_at_millis"] = 12345, f.now.UnixMilli()
	p["ingest_cluster_id"], p["strict_lease_owner"] = "audit-cell", true
	p["admission_window_ms"] = 60000
	p["authority"] = map[string]any{"CapacityMaxStreams": 1, "ProcessesJSON": "{}", "MediaAuthorityID": "audit-authority",
		"MediaAuthorityVersion": 1, "TenantAuthorityVersion": 1}
	p["connected_event"] = f.event("stream.connected")
	return p
}

func (f *publisherFixture) settleRequest() map[string]any {
	p := f.identity()
	p["reason"], p["admission_window_ms"] = "admission_lapsed", 60000
	event := f.event("stream.idle")
	// A separate ID permits both connected and idle events in the fixture transaction.
	event["ID"] = f.streamID
	p["idle_event"] = event
	return p
}

func publisherJSON(value any) string {
	b, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func (f *publisherFixture) request() string {
	switch f.name {
	case "PreparePublisherAdmission":
		return publisherJSON(f.prepareRequest())
	case "ConfirmPublisherAdmission":
		p := f.identity()
		p["source_revision"], p["broadcast_live"] = int64(4503599627370497), true
		p["push_targets"], p["decklog_trigger"], p["peer_clusters"] = "", "", ""
		return publisherJSON(p)
	case "SettlePublisherAdmission":
		return publisherJSON(f.settleRequest())
	default:
		return publisherJSON([]map[string]string{{"tenant_id": f.tenant, "generation": f.generation}})
	}
}

func (f *publisherFixture) setup(ctx context.Context, conn *sql.Conn) error {
	if f.name == "PreparePublisherAdmission" {
		return nil
	}
	var prepared string
	if err := conn.QueryRowContext(ctx, `SELECT foghorn.prepare_publisher_admission_v1($1::jsonb)::text`, publisherJSON(f.prepareRequest())).Scan(&prepared); err != nil {
		return fmt.Errorf("publisher fixture prepare: %w", err)
	}
	var result struct {
		Outcome    int    `json:"outcome"`
		Generation string `json:"generation"`
	}
	if err := json.Unmarshal([]byte(prepared), &result); err != nil || result.Outcome != 1 || result.Generation != f.generation {
		return fmt.Errorf("publisher fixture prepare rejected: %s", prepared)
	}
	if f.name == "ConfirmPublisherAdmission" {
		return nil
	}
	if _, err := conn.ExecContext(ctx, `UPDATE foghorn.ingest_sessions SET started_at=NOW()-INTERVAL '10 minutes'
WHERE tenant_id=$1::uuid AND id=$2::uuid`, f.tenant, f.generation); err != nil {
		return err
	}
	if f.name == "MarkPublisherLeasesReleased" {
		var settled bool
		if err := conn.QueryRowContext(ctx, `SELECT foghorn.settle_publisher_admission_v1($1::jsonb)`, publisherJSON(f.settleRequest())).Scan(&settled); err != nil {
			return fmt.Errorf("publisher fixture settle: %w", err)
		}
		if !settled {
			return fmt.Errorf("publisher fixture settlement rejected")
		}
	}
	return nil
}

func (f *publisherFixture) verify(ctx context.Context, conn *sql.Conn) error {
	var condition string
	switch f.name {
	case "PreparePublisherAdmission":
		condition = `s.projection_state='pending' AND s.source_revision IS NOT NULL AND s.ended_at IS NULL
AND EXISTS(SELECT 1 FROM foghorn.domain_event_outbox e WHERE e.tenant_id=s.tenant_id AND e.event_id=$3::uuid)`
	case "ConfirmPublisherAdmission":
		condition = `s.projection_state='active' AND s.ended_at IS NULL
AND EXISTS(SELECT 1 FROM foghorn.ingest_admission_effects e WHERE e.tenant_id=s.tenant_id AND e.source_generation=s.id AND e.broadcast_live)`
	case "SettlePublisherAdmission":
		condition = `s.ended_at IS NOT NULL AND s.ended_reason='admission_lapsed'
AND EXISTS(SELECT 1 FROM foghorn.ingest_offline_effects e WHERE e.tenant_id=s.tenant_id AND e.source_generation=s.id)
AND EXISTS(SELECT 1 FROM foghorn.domain_event_outbox e WHERE e.tenant_id=s.tenant_id AND e.event_id=$3::uuid)`
	default:
		condition = `s.ended_at IS NOT NULL AND s.publisher_lease_released_at IS NOT NULL`
	}
	args := []any{f.tenant, f.generation}
	switch f.name {
	case "PreparePublisherAdmission":
		args = append(args, f.eventID)
	case "SettlePublisherAdmission":
		args = append(args, f.streamID)
	}
	var transitioned bool
	err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM foghorn.ingest_sessions s
WHERE s.tenant_id=$1::uuid AND s.id=$2::uuid AND `+condition+`)`, args...).Scan(&transitioned)
	if err != nil {
		return fmt.Errorf("publisher fixture verification: %w", err)
	}
	if !transitioned {
		return fmt.Errorf("publisher fixture %s did not perform its durable transition", f.name)
	}
	return nil
}
