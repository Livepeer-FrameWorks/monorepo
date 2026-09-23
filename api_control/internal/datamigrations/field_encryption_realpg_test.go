//go:build schema_verify

package datamigrations

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	fieldcrypt "github.com/Livepeer-FrameWorks/monorepo/pkg/crypto"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	_ "github.com/lib/pq"
)

func TestFieldEncryptionKeysetSweepAndVerify_RealPG(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-field-encryption-realpg-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatal(err)
	}
	if output, runErr := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); runErr != nil {
		t.Fatalf("docker run: %v\n%s", runErr, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := dockerpg.WaitReady(db, name); err != nil {
		t.Fatal(err)
	}
	schema, err := dbsql.Content.ReadFile("schema/commodore.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}

	settings := testFieldEncryptionSettings("legacy-jwt-key-material-32-bytes")
	armed, err := runFieldEncryption(context.Background(), db, datamigrate.RunOptions{BatchSize: 2}, settings)
	if err != nil {
		t.Fatal(err)
	}
	spec := encryptedColumns[0]
	const orphanRowID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
	if _, err := db.Exec(`INSERT INTO commodore.field_encryption_quarantine
		(table_name, column_name, row_id, ciphertext_fingerprint, purpose)
		VALUES ($1, $2, $3, $4, $5)`, spec.table, spec.column,
		orphanRowID, "orphaned-ciphertext", spec.purpose); err != nil {
		t.Fatal(err)
	}
	progress, err := runFieldEncryption(context.Background(), db, datamigrate.RunOptions{BatchSize: 2, Checkpoint: armed.Checkpoint}, settings)
	if err != nil {
		t.Fatal(err)
	}
	if !progress.Done || progress.Scanned != 0 {
		t.Fatalf("empty native-keyset sweep did not complete: %+v", progress)
	}
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyFieldEncryption(context.Background(), tx, settings); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	t.Run("writable sweep prunes deleted source quarantine", func(t *testing.T) {
		var remaining int
		if err := db.QueryRow(`SELECT COUNT(*) FROM commodore.field_encryption_quarantine WHERE row_id = $1`, orphanRowID).Scan(&remaining); err != nil {
			t.Fatal(err)
		}
		if remaining != 0 {
			t.Fatalf("orphan quarantine rows remaining = %d", remaining)
		}
	})

	t.Run("migration update suppresses real media authority trigger", func(t *testing.T) {
		const (
			tenantID = "81000000-0000-4000-8000-000000000001"
			userID   = "82000000-0000-4000-8000-000000000001"
			streamID = "83000000-0000-4000-8000-000000000001"
			targetID = "84000000-0000-4000-8000-000000000001"
		)
		legacy, deriveErr := fieldcrypt.DeriveFieldEncryptor([]byte("legacy-jwt-key-material-32-bytes"), "push-target-uri")
		if deriveErr != nil {
			t.Fatal(deriveErr)
		}
		stored, encryptErr := legacy.Encrypt("rtmps://example.test/live/private-key")
		if encryptErr != nil {
			t.Fatal(encryptErr)
		}
		for _, seed := range []struct {
			query string
			args  []any
		}{
			{`INSERT INTO commodore.users (id, tenant_id, email, password_hash) VALUES ($1, $2, 'migration-suppress@example.test', 'x')`, []any{userID, tenantID}},
			{`INSERT INTO commodore.streams (id, tenant_id, user_id, stream_key, playback_id, internal_name, title) VALUES ($1, $2, $3, 'migration-suppress-key', 'migration-suppress-playback', 'migration-suppress-live', 'Migration suppress')`, []any{streamID, tenantID, userID}},
			{`INSERT INTO commodore.push_targets (id, tenant_id, stream_id, platform, name, target_uri) VALUES ($1, $2, $3, 'custom', 'Migration suppress', $4)`, []any{targetID, tenantID, streamID, stored}},
		} {
			if _, seedErr := db.ExecContext(context.Background(), seed.query, seed.args...); seedErr != nil {
				t.Fatal(seedErr)
			}
		}
		if _, clearErr := db.Exec(`DELETE FROM commodore.media_authority_refresh_obligations`); clearErr != nil {
			t.Fatal(clearErr)
		}
		armed, armErr := runFieldEncryption(context.Background(), db, datamigrate.RunOptions{BatchSize: 10}, settings)
		if armErr != nil {
			t.Fatal(armErr)
		}
		progress, migrateErr := runFieldEncryption(context.Background(), db, datamigrate.RunOptions{BatchSize: 10, Checkpoint: armed.Checkpoint}, settings)
		if migrateErr != nil {
			t.Fatal(migrateErr)
		}
		if progress.Changed == 0 {
			t.Fatalf("legacy push target was not rewritten: %+v", progress)
		}
		var refreshes int
		if countErr := db.QueryRow(`SELECT count(*) FROM commodore.media_authority_refresh_obligations`).Scan(&refreshes); countErr != nil {
			t.Fatal(countErr)
		}
		if refreshes != 0 {
			t.Fatalf("migration update enqueued %d media-authority refreshes", refreshes)
		}
	})

	t.Run("quarantine retry epoch re-admits only older failures", func(t *testing.T) {
		const stored = "legacy-ciphertext"
		spec := encryptedColumn{
			table:   "commodore.field_encryption_retry_fixture",
			id:      "id",
			column:  "secret",
			purpose: "retry-test",
		}
		if _, err := db.Exec(`CREATE TABLE commodore.field_encryption_retry_fixture (
				id TEXT PRIMARY KEY,
				secret TEXT NOT NULL
			)`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO commodore.field_encryption_retry_fixture (id, secret)
			VALUES ('row-1', $1)`, stored); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO commodore.field_encryption_quarantine (
				table_name, column_name, row_id, ciphertext_fingerprint,
				purpose, error_code, last_observed_at
			) VALUES ($1, $2, 'row-1', $3, $4, 'decrypt_failed', NOW() - INTERVAL '1 day')`,
			spec.table, spec.column, fieldEncryptionFingerprint(stored), spec.purpose); err != nil {
			t.Fatal(err)
		}

		candidateExists := func(retryAfter int64) bool {
			t.Helper()
			var id, candidate string
			err := db.QueryRow(
				fieldEncryptionCandidateQuery(spec),
				10, "enc:v3:primary:", spec.table, spec.column, "", retryAfter,
			).Scan(&id, &candidate)
			if err == sql.ErrNoRows {
				return false
			}
			if err != nil {
				t.Fatal(err)
			}
			return id == "row-1" && candidate == stored
		}

		if candidateExists(0) {
			t.Fatal("ordinary sweep unexpectedly re-admitted quarantined ciphertext")
		}
		retryAfter, err := fieldEncryptionDatabaseEpochMillis(context.Background(), db)
		if err != nil {
			t.Fatal(err)
		}
		if !candidateExists(retryAfter) {
			t.Fatal("retry sweep did not re-admit quarantine observed before its epoch")
		}
		if err := quarantineFieldEncryptionFailure(context.Background(), db, spec, struct{ id, stored string }{"row-1", stored}); err != nil {
			t.Fatal(err)
		}
		if candidateExists(retryAfter) {
			t.Fatal("retry sweep re-admitted a failure already observed during its epoch")
		}
		if _, err := db.Exec(`DELETE FROM commodore.field_encryption_quarantine WHERE table_name = $1`, spec.table); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("dry run names undecryptable rows and verify lists quarantine", func(t *testing.T) {
		const (
			tenantID = "91000000-0000-4000-8000-000000000001"
			userID   = "92000000-0000-4000-8000-000000000001"
			streamID = "93000000-0000-4000-8000-000000000001"
			firstID  = "90000000-0000-4000-8000-000000000001"
			targetID = "94000000-0000-4000-8000-000000000001"
		)
		retired, err := fieldcrypt.NewFieldKeyring("retired", []byte("retired-field-key-material-32-bytes"), nil, nil, "push-target-uri")
		if err != nil {
			t.Fatal(err)
		}
		stored, err := retired.Encrypt("rtmps://example.test/live/unknown-key-secret")
		if err != nil {
			t.Fatal(err)
		}
		for _, seed := range []struct {
			query string
			args  []any
		}{
			{`INSERT INTO commodore.users (id, tenant_id, email, password_hash) VALUES ($1, $2, 'unknown-key@example.test', 'x')`, []any{userID, tenantID}},
			{`INSERT INTO commodore.streams (id, tenant_id, user_id, stream_key, playback_id, internal_name, title) VALUES ($1, $2, $3, 'unknown-key-key', 'unknown-key-playback', 'unknown-key-live', 'Unknown key')`, []any{streamID, tenantID, userID}},
			// Sorts ahead of targetID and decrypts, so with --batch-size 1 the
			// failing row is only reached on the second page.
			{`INSERT INTO commodore.push_targets (id, tenant_id, stream_id, platform, name, target_uri) VALUES ($1, $2, $3, 'custom', 'Plaintext', 'rtmps://example.test/live/plaintext')`, []any{firstID, tenantID, streamID}},
			{`INSERT INTO commodore.push_targets (id, tenant_id, stream_id, platform, name, target_uri) VALUES ($1, $2, $3, 'custom', 'Unknown key', $4)`, []any{targetID, tenantID, streamID, stored}},
		} {
			if _, seedErr := db.ExecContext(context.Background(), seed.query, seed.args...); seedErr != nil {
				t.Fatal(seedErr)
			}
		}

		// Register a copy bound to this test's settings under a distinct ID; the
		// report must come from the production registration.
		if datamigrate.Lookup(FieldEncryptionID) == nil {
			registerFieldEncryption(settings)
		}
		registered := datamigrate.Lookup(FieldEncryptionID)
		if registered.Report == nil {
			t.Fatal("field-encryption migration registers no verify report")
		}
		testID := FieldEncryptionID + "_realpg_output"
		if datamigrate.Lookup(testID) == nil {
			datamigrate.Register(datamigrate.Migration{
				ID: testID, Service: "commodore", IntroducedIn: registered.IntroducedIn,
				Run: func(ctx context.Context, migrationDB datamigrate.DB, opts datamigrate.RunOptions) (datamigrate.Progress, error) {
					return runFieldEncryption(ctx, migrationDB, opts, settings)
				},
				Verify: func(ctx context.Context, migrationDB datamigrate.DB) error {
					return verifyFieldEncryption(ctx, migrationDB, settings)
				},
				Report: registered.Report,
			})
		}
		openDB := func() (*sql.DB, error) { return db, nil }

		var dryOut bytes.Buffer
		if err := datamigrate.HandleRun(context.Background(), openDB, &dryOut, []string{testID, "--batch-size", "1", "--dry-run"}); err != nil {
			t.Fatalf("dry run: %v\n%s", err, dryOut.String())
		}
		wantFinding := fmt.Sprintf("undecryptable table=commodore.push_targets column=target_uri row_id=%s error=unknown_key_id key_id=retired", targetID)
		if !strings.Contains(dryOut.String(), wantFinding) || !strings.Contains(dryOut.String(), "findings: showing 1 of 1") ||
			!strings.Contains(dryOut.String(), "scanned table=commodore.push_targets column=target_uri rows=2 undecryptable=1") ||
			!strings.Contains(dryOut.String(), "scanned table=commodore.dvr_recordings column=playback_webhook_secret_enc rows=0 undecryptable=0") {
			t.Fatalf("dry run did not name the undecryptable row:\n%s", dryOut.String())
		}
		if strings.Contains(dryOut.String(), stored) || strings.Contains(dryOut.String(), "unknown-key-secret") {
			t.Fatalf("dry run leaked field material:\n%s", dryOut.String())
		}
		var quarantined int
		if err := db.QueryRow(`SELECT COUNT(*) FROM commodore.field_encryption_quarantine WHERE row_id = $1`, targetID).Scan(&quarantined); err != nil {
			t.Fatal(err)
		}
		var current string
		if err := db.QueryRow(`SELECT target_uri FROM commodore.push_targets WHERE id = $1`, targetID).Scan(&current); err != nil {
			t.Fatal(err)
		}
		if quarantined != 0 || current != stored {
			t.Fatalf("dry run wrote: quarantine rows=%d value changed=%v", quarantined, current != stored)
		}

		progress := datamigrate.Progress{}
		for attempt := 0; !progress.Done; attempt++ {
			if attempt > 5 {
				t.Fatalf("real run did not finish: %+v", progress)
			}
			progress, err = runFieldEncryption(context.Background(), db, datamigrate.RunOptions{BatchSize: 100, Checkpoint: progress.Checkpoint}, settings)
			if err != nil {
				t.Fatal(err)
			}
		}

		var verifyOut bytes.Buffer
		verifyErr := datamigrate.HandleVerify(context.Background(), openDB, &verifyOut, []string{testID})
		if verifyErr == nil || !strings.Contains(verifyErr.Error(), "1 quarantined rows") {
			t.Fatalf("verify did not fail on the quarantined row: %v\n%s", verifyErr, verifyOut.String())
		}
		wantRow := fmt.Sprintf("table=commodore.push_targets column=target_uri row_id=%s error_code=decrypt_failed attempts=1 last_observed_at=", targetID)
		if !strings.Contains(verifyOut.String(), "field-encryption quarantine: showing 1 of 1") || !strings.Contains(verifyOut.String(), wantRow) {
			t.Fatalf("verify did not list the quarantined row:\n%s", verifyOut.String())
		}
	})
}
