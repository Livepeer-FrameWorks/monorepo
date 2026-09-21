//go:build schema_verify

package commodoredb

import (
	"context"
	"testing"
	"time"
)

func TestFieldEncryptionSuppressesEveryMediaAuthorityTrigger_RealPG(t *testing.T) {
	db := startCommodoreQueryCatalogRealPG(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const (
		tenantID = "71000000-0000-4000-8000-000000000001"
		userID   = "72000000-0000-4000-8000-000000000001"
		streamID = "73000000-0000-4000-8000-000000000001"
		targetID = "74000000-0000-4000-8000-000000000001"
		vodID    = "75000000-0000-4000-8000-000000000001"
	)
	seed := []struct {
		statement string
		args      []any
	}{
		{`INSERT INTO commodore.users (id, tenant_id, email, password_hash) VALUES ($1, $2, 'encryption-suppression@example.test', 'x')`, []any{userID, tenantID}},
		{`INSERT INTO commodore.streams (id, tenant_id, user_id, stream_key, playback_id, internal_name, title) VALUES ($1, $2, $3, 'suppression-stream-key', 'suppression-playback', 'suppression-live', 'Suppression')`, []any{streamID, tenantID, userID}},
		{`INSERT INTO commodore.push_targets (id, tenant_id, stream_id, platform, name, target_uri) VALUES ($1, $2, $3, 'youtube', 'Suppression target', 'enc:v3:old:target')`, []any{targetID, tenantID, streamID}},
		{`INSERT INTO commodore.vod_assets (id, tenant_id, user_id, stream_id, vod_hash, internal_name, playback_id, filename) VALUES ($1, $2, $3, $4, 'suppression-vod-hash', 'suppression-vod', 'suppression-vod-playback', 'suppression.mp4')`, []any{vodID, tenantID, userID, streamID}},
	}
	for _, row := range seed {
		if _, err := db.ExecContext(ctx, row.statement, row.args...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM commodore.media_authority_refresh_obligations`); err != nil {
		t.Fatal(err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT set_config('frameworks.suppress_media_authority_refresh', 'on', true)`); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	for _, statement := range []string{
		`UPDATE commodore.streams SET playback_webhook_secret_enc = 'enc:v3:new:stream' WHERE id = '` + streamID + `'`,
		`UPDATE commodore.push_targets SET target_uri = 'enc:v3:new:target' WHERE id = '` + targetID + `'`,
		`UPDATE commodore.vod_assets SET playback_webhook_secret_enc = 'enc:v3:new:vod' WHERE id = '` + vodID + `'`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var suppressed int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.media_authority_refresh_obligations`).Scan(&suppressed); err != nil {
		t.Fatal(err)
	}
	if suppressed != 0 {
		t.Fatalf("field-only re-encryption enqueued %d media-authority refreshes", suppressed)
	}

	for _, update := range []struct {
		statement string
		id        string
	}{
		{`UPDATE commodore.streams SET playback_webhook_secret_enc = 'enc:v3:next:stream' WHERE id = $1`, streamID},
		{`UPDATE commodore.push_targets SET target_uri = 'enc:v3:next:target' WHERE id = $1`, targetID},
		{`UPDATE commodore.vod_assets SET playback_webhook_secret_enc = 'enc:v3:next:vod' WHERE id = $1`, vodID},
	} {
		if _, err := db.ExecContext(ctx, update.statement, update.id); err != nil {
			t.Fatal(err)
		}
	}
	// Events fold per target, so the three changes are counted by revision: a
	// push target refreshes its stream's obligation rather than adding a row.
	var active int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(sum(revision), 0) FROM commodore.media_authority_refresh_obligations`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 3 {
		t.Fatalf("media-authority triggers are not active outside the migration transaction: got %d folded events", active)
	}
}
