//go:build schema_verify

package datamigrations

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
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

	t.Setenv("FIELD_ENCRYPTION_KEY_ID", "primary")
	t.Setenv("FIELD_ENCRYPTION_KEY", "active-field-key-material-32-bytes")
	t.Setenv("FIELD_ENCRYPTION_PREVIOUS_KEYS", "")
	t.Setenv("JWT_SECRET", "legacy-jwt-key-material-32-bytes")
	armed, err := runFieldEncryption(context.Background(), db, datamigrate.RunOptions{BatchSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	progress, err := runFieldEncryption(context.Background(), db, datamigrate.RunOptions{BatchSize: 2, Checkpoint: armed.Checkpoint})
	if err != nil {
		t.Fatal(err)
	}
	if !progress.Done || progress.Scanned != 0 {
		t.Fatalf("empty native-keyset sweep did not complete: %+v", progress)
	}
	if err := verifyFieldEncryption(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	t.Run("deleted source row does not strand quarantine verification", func(t *testing.T) {
		spec := encryptedColumns[0]
		if _, err := db.Exec(`INSERT INTO commodore.field_encryption_quarantine
			(table_name, column_name, row_id, ciphertext_fingerprint, purpose)
			VALUES ($1, $2, $3, $4, $5)`, spec.table, spec.column,
			"ffffffff-ffff-4fff-8fff-ffffffffffff", "orphaned-ciphertext", spec.purpose); err != nil {
			t.Fatal(err)
		}
		if err := verifyFieldEncryption(context.Background(), db); err != nil {
			t.Fatalf("orphan quarantine row blocked verification: %v", err)
		}
		var remaining int
		if err := db.QueryRow(`SELECT COUNT(*) FROM commodore.field_encryption_quarantine WHERE row_id = $1`, "ffffffff-ffff-4fff-8fff-ffffffffffff").Scan(&remaining); err != nil {
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
		if _, clearErr := db.Exec(`DELETE FROM commodore.media_authority_refresh_inbox`); clearErr != nil {
			t.Fatal(clearErr)
		}
		armed, armErr := runFieldEncryption(context.Background(), db, datamigrate.RunOptions{BatchSize: 10})
		if armErr != nil {
			t.Fatal(armErr)
		}
		progress, migrateErr := runFieldEncryption(context.Background(), db, datamigrate.RunOptions{BatchSize: 10, Checkpoint: armed.Checkpoint})
		if migrateErr != nil {
			t.Fatal(migrateErr)
		}
		if progress.Changed == 0 {
			t.Fatalf("legacy push target was not rewritten: %+v", progress)
		}
		var refreshes int
		if countErr := db.QueryRow(`SELECT count(*) FROM commodore.media_authority_refresh_inbox`).Scan(&refreshes); countErr != nil {
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
	})
}
