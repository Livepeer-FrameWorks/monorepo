//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"sync"
	"testing"

	fieldcrypt "github.com/Livepeer-FrameWorks/monorepo/pkg/crypto"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	eventoutbox "github.com/Livepeer-FrameWorks/monorepo/pkg/events/outbox"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	eventspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	"github.com/sirupsen/logrus"
)

// flakyPublisher records every event it is handed and reports a failure for
// the IDs in lostAck once each, as a publish whose acknowledgement was lost.
type flakyPublisher struct {
	mu        sync.Mutex
	attempts  []string
	delivered []*eventspb.DomainEvent
	lostAck   map[string]bool
}

func (p *flakyPublisher) PublishDomainEvents(_ context.Context, batch *eventspb.DomainEventBatch) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, ev := range batch.GetEvents() {
		p.attempts = append(p.attempts, ev.GetId())
		if _, _, err := events.Validate(ev); err != nil {
			return err
		}
	}
	for _, ev := range batch.GetEvents() {
		if p.lostAck[ev.GetId()] {
			delete(p.lostAck, ev.GetId())
			return errors.New("acknowledgement lost")
		}
	}
	p.delivered = append(p.delivered, batch.GetEvents()...)
	return nil
}

type commodoreEventRow struct {
	eventID, eventType, aggregateID, scope, tenantID, tokenHash string
	legacyType, legacyPayloadID                                 string
}

// commodoreEventRows returns every domain row joined to its legacy row on the
// shared event ID, in enqueue order.
func commodoreEventRows(t *testing.T, db *sql.DB) []commodoreEventRow {
	t.Helper()
	rows, err := db.Query(`
		SELECT d.event_id::text, d.event_type, d.aggregate_id, d.scope, COALESCE(d.tenant_id::text, ''),
		       d.actor_token_hash, COALESCE(l.event_type, ''), COALESCE(l.payload->>'eventId', '')
		FROM commodore.domain_event_outbox d
		LEFT JOIN commodore.service_event_outbox l ON l.event_id = d.event_id
		ORDER BY d.enqueued_at, d.event_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close() //nolint:errcheck
	var out []commodoreEventRow
	for rows.Next() {
		var r commodoreEventRow
		if err := rows.Scan(&r.eventID, &r.eventType, &r.aggregateID, &r.scope, &r.tenantID, &r.tokenHash, &r.legacyType, &r.legacyPayloadID); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCommodoreDomainEventOutbox_RealPG(t *testing.T) {
	db := startCommodoreRealPG(t)
	ctx := context.Background()
	const (
		tenantID = "10000000-0000-4000-8000-0000000000e1"
		userID   = "20000000-0000-4000-8000-0000000000e1"
		tokenID  = "30000000-0000-4000-8000-0000000000e1"
	)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO commodore.users (id, tenant_id, email, role) VALUES ($1::uuid, $2::uuid, 'events@example.com', 'owner')
	`, userID, tenantID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	pushKeys, err := fieldcrypt.NewFieldKeyring("active", []byte("active-field-key-material-32-bytes"), nil, nil, "push-target-uri")
	if err != nil {
		t.Fatal(err)
	}
	hasher := testTokenHasher(t)
	server := &CommodoreServer{db: db, logger: logrus.New(), tokenHasher: hasher, fieldEncryptor: pushKeys}
	userCtx := context.WithValue(context.Background(), ctxkeys.KeyUserID, userID)
	userCtx = context.WithValue(userCtx, ctxkeys.KeyTenantID, tenantID)
	userCtx = context.WithValue(userCtx, ctxkeys.KeyRole, "owner")
	// The stream is managed through an API token, so its events carry the
	// token's usage hash.
	tokenCtx := context.WithValue(userCtx, ctxkeys.KeyAuthType, "api_token")
	tokenCtx = context.WithValue(tokenCtx, ctxkeys.KeyAPITokenID, tokenID)

	created, err := server.CreateStream(tokenCtx, &commodorepb.CreateStreamRequest{Title: "Domain events"})
	if err != nil {
		t.Fatalf("CreateStream: %v", err)
	}
	streamID := created.GetId()
	if _, err := server.RefreshStreamKey(tokenCtx, &commodorepb.RefreshStreamKeyRequest{StreamId: streamID}); err != nil {
		t.Fatalf("RefreshStreamKey: %v", err)
	}

	// Every domain row has a legacy row under the same ID, and the legacy
	// payload carries that ID too.
	rows := commodoreEventRows(t, db)
	if len(rows) != 2 || rows[0].eventType != "stream.created" || rows[1].eventType != "stream.key_rotated" {
		t.Fatalf("domain rows = %+v, want stream.created then stream.key_rotated", rows)
	}
	wantLegacy := map[string]string{"stream.created": eventStreamCreated, "stream.key_rotated": eventStreamUpdated}
	wantHash := events.HashIdentifier([]byte("commodore-test-usage-hash-secret"), tokenID)
	for _, r := range rows {
		if r.legacyType != wantLegacy[r.eventType] || r.legacyPayloadID != r.eventID {
			t.Fatalf("legacy dual-write for %s = type %q payload id %q, want %q under %s", r.eventType, r.legacyType, r.legacyPayloadID, wantLegacy[r.eventType], r.eventID)
		}
		if r.aggregateID != streamID || r.scope != "tenant" || r.tenantID != tenantID {
			t.Fatalf("row %+v: want aggregate %s, tenant scope, tenant %s", r, streamID, tenantID)
		}
		if r.tokenHash != strconv.FormatUint(wantHash, 10) {
			t.Fatalf("row %s actor token hash = %q, want %d", r.eventID, r.tokenHash, wantHash)
		}
	}

	// A domain insert that fails rolls back the key rotation and its legacy
	// row with it.
	var keyBefore string
	if err := db.QueryRowContext(ctx, `SELECT stream_key FROM commodore.streams WHERE id = $1::uuid`, streamID).Scan(&keyBefore); err != nil {
		t.Fatal(err)
	}
	legacyBefore := countRows(t, db, `SELECT count(*) FROM commodore.service_event_outbox`)
	if _, err := db.ExecContext(ctx, `ALTER TABLE commodore.domain_event_outbox RENAME TO domain_event_outbox_unavailable`); err != nil {
		t.Fatal(err)
	}
	if _, err := server.RefreshStreamKey(tokenCtx, &commodorepb.RefreshStreamKeyRequest{StreamId: streamID}); err == nil {
		t.Fatal("RefreshStreamKey succeeded without its domain event")
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE commodore.domain_event_outbox_unavailable RENAME TO domain_event_outbox`); err != nil {
		t.Fatal(err)
	}
	var keyAfter string
	if err := db.QueryRowContext(ctx, `SELECT stream_key FROM commodore.streams WHERE id = $1::uuid`, streamID).Scan(&keyAfter); err != nil {
		t.Fatal(err)
	}
	if keyAfter != keyBefore || countRows(t, db, `SELECT count(*) FROM commodore.service_event_outbox`) != legacyBefore {
		t.Fatal("failed domain insert left the key rotation or its legacy row committed")
	}

	// Relay: the first stream event's acknowledgement is lost. Its second
	// event waits while the first is incomplete, and the redelivery carries
	// the same ID.
	publisher := &flakyPublisher{lostAck: map[string]bool{rows[0].eventID: true}}
	relay, err := eventoutbox.NewRelay(db, DomainEventSchema, publisher, logrus.New())
	if err != nil {
		t.Fatal(err)
	}
	if n, err := relay.DispatchOnce(ctx); err != nil || n != 1 {
		t.Fatalf("first dispatch claimed %d, err %v; want only the aggregate head", n, err)
	}
	if n, err := relay.DispatchOnce(ctx); err != nil || n != 0 {
		t.Fatalf("dispatch during the head's backoff claimed %d, err %v; the second event must wait", n, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE commodore.domain_event_outbox SET next_attempt_at = now() WHERE event_id = $1::uuid`, rows[0].eventID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := relay.DispatchOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(publisher.attempts) != 3 || publisher.attempts[0] != rows[0].eventID || publisher.attempts[1] != rows[0].eventID || publisher.attempts[2] != rows[1].eventID {
		t.Fatalf("publish attempts = %v, want %s twice then %s", publisher.attempts, rows[0].eventID, rows[1].eventID)
	}
	if _, msg, err := events.Validate(publisher.delivered[1]); err != nil {
		t.Fatal(err)
	} else if m, ok := msg.(*publicv1.StreamKeyRotated); !ok || m.GetStreamId() != streamID {
		t.Fatalf("delivered key rotation = %v", msg)
	}

	// Legacy dispatcher: a claim whose settlement never arrives is claimed
	// again after its lease and sends the same IDs as the domain rows.
	legacyIDs := func() []string {
		batch, claimErr := server.claimCommodoreServiceOutboxBatch(ctx)
		if claimErr != nil {
			t.Fatal(claimErr)
		}
		var ids []string
		for _, row := range batch {
			event, decodeErr := decodeCommodoreServiceOutboxRow(row)
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			ids = append(ids, event.GetEventId())
		}
		return ids
	}
	first := legacyIDs()
	if _, err := db.ExecContext(ctx, `UPDATE commodore.service_event_outbox SET claimed_at = now() - interval '2 minutes'`); err != nil {
		t.Fatal(err)
	}
	second := legacyIDs()
	if len(first) != 2 || len(second) != 2 || first[0] != rows[0].eventID || first[1] != rows[1].eventID ||
		second[0] != first[0] || second[1] != first[1] {
		t.Fatalf("legacy dispatch IDs = %v then %v, want the domain IDs %s, %s both times", first, second, rows[0].eventID, rows[1].eventID)
	}

	// UpdatePushTargetStatus records multistream.status_changed only when the
	// status actually changes.
	encURI, err := pushKeys.Encrypt("rtmp://live.example.com/app/key")
	if err != nil {
		t.Fatal(err)
	}
	const targetID = "40000000-0000-4000-8000-0000000000e1"
	if _, err := db.ExecContext(ctx, `
		INSERT INTO commodore.push_targets (id, tenant_id, stream_id, platform, name, target_uri)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'custom', 'Backup', $4)
	`, targetID, tenantID, streamID, encURI); err != nil {
		t.Fatalf("seed push target: %v", err)
	}
	serviceCtx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
	for _, statusValue := range []string{"pushing", "pushing", "idle"} {
		if _, err := server.UpdatePushTargetStatus(serviceCtx, &commodorepb.UpdatePushTargetStatusRequest{
			Id: targetID, TenantId: tenantID, Status: statusValue,
		}); err != nil {
			t.Fatalf("UpdatePushTargetStatus(%s): %v", statusValue, err)
		}
	}
	var transitions []string
	statusRows, err := db.QueryContext(ctx, `
		SELECT payload FROM commodore.domain_event_outbox
		WHERE event_type = 'multistream.status_changed' AND aggregate_id = $1 ORDER BY enqueued_at`, targetID)
	if err != nil {
		t.Fatal(err)
	}
	for statusRows.Next() {
		var payload []byte
		if err := statusRows.Scan(&payload); err != nil {
			t.Fatal(err)
		}
		_, msg, err := events.Decode("multistream.status_changed", payload)
		if err != nil {
			t.Fatal(err)
		}
		m := msg.(*publicv1.MultistreamStatusChanged)
		transitions = append(transitions, m.GetPreviousStatus().String()+">"+m.GetStatus().String())
		if m.GetTargetName() != "Backup" || m.GetStreamId() != streamID {
			t.Fatalf("status change payload = %v", m)
		}
	}
	_ = statusRows.Close()
	if len(transitions) != 2 ||
		transitions[0] != "MULTISTREAM_STATUS_IDLE>MULTISTREAM_STATUS_PUSHING" ||
		transitions[1] != "MULTISTREAM_STATUS_PUSHING>MULTISTREAM_STATUS_IDLE" {
		t.Fatalf("status transitions = %v, want idle>pushing then pushing>idle only", transitions)
	}
	if n := countRows(t, db, `
		SELECT count(*) FROM commodore.service_event_outbox l
		JOIN commodore.domain_event_outbox d ON d.event_id = l.event_id
		WHERE d.event_type = 'multistream.status_changed'`); n != 2 {
		t.Fatalf("legacy push_target_status rows sharing the domain IDs = %d, want 2", n)
	}

	// A repeated revoke changes nothing and records nothing.
	token, err := server.CreateAPIToken(userCtx, &commodorepb.CreateAPITokenRequest{TokenName: "ci"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := server.RevokeAPIToken(userCtx, &commodorepb.RevokeAPITokenRequest{TokenId: token.GetId()}); err != nil {
			t.Fatalf("RevokeAPIToken #%d: %v", i+1, err)
		}
	}
	if n := countRows(t, db, `SELECT count(*) FROM commodore.domain_event_outbox WHERE event_type = 'api_token.created' AND aggregate_id = $1`, token.GetId()); n != 1 {
		t.Fatalf("api_token.created rows = %d, want 1", n)
	}
	if n := countRows(t, db, `SELECT count(*) FROM commodore.domain_event_outbox WHERE event_type = 'api_token.revoked' AND aggregate_id = $1`, token.GetId()); n != 1 {
		t.Fatalf("api_token.revoked rows = %d, want 1", n)
	}

	// Commodore emits no platform-scoped type: every row carries its tenant.
	if n := countRows(t, db, `SELECT count(*) FROM commodore.domain_event_outbox WHERE scope <> 'tenant' OR tenant_id IS NULL`); n != 0 {
		t.Fatalf("%d rows without a tenant", n)
	}
	if n := countRows(t, db, `SELECT count(*) FROM commodore.domain_event_outbox d
		LEFT JOIN commodore.service_event_outbox l ON l.event_id = d.event_id WHERE l.id IS NULL`); n != 0 {
		t.Fatalf("%d domain rows without their legacy row", n)
	}
}
