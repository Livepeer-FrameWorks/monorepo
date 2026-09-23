package datamigrations

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	fieldcrypt "github.com/Livepeer-FrameWorks/monorepo/pkg/crypto"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
)

type v3CiphertextArgument struct{}

func (v3CiphertextArgument) Match(value driver.Value) bool {
	stored, ok := value.(string)
	return ok && strings.HasPrefix(stored, "enc:v3:primary:")
}

func fieldEncryptionTestCheckpoint(t *testing.T, legacySecret string) []byte {
	t.Helper()
	encoded, err := json.Marshal(fieldEncryptionCheckpoint{
		LegacyKeyFingerprint: fieldEncryptionLegacyKeyFingerprint(testFieldEncryptionSettings(legacySecret), legacySecret),
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// testFieldEncryptionSettings is the loaded configuration with the test active
// key, the default key ID, and the given JWT_SECRET.
func testFieldEncryptionSettings(jwtSecret string) FieldEncryptionSettings {
	return FieldEncryptionSettings{
		ActiveKeyID: "primary",
		ActiveKey:   "active-field-key-material-32-bytes",
		JWTSecret:   jwtSecret,
	}
}

// The CLI requires a fresh backup before running an irreversible data migration; re-encrypted fields cannot be read
// by the previous release's key handling.
func TestFieldEncryptionIsRegisteredIrreversible(t *testing.T) {
	if datamigrate.Lookup(FieldEncryptionID) == nil {
		registerFieldEncryption(testFieldEncryptionSettings("jwt"))
	}
	m := datamigrate.Lookup(FieldEncryptionID)
	if m == nil || !m.Irreversible {
		t.Fatalf("%s must be registered as irreversible: %+v", FieldEncryptionID, m)
	}
}

func TestRunFieldEncryptionRewritesLegacyRowsWithActiveKey(t *testing.T) {
	settings := testFieldEncryptionSettings("legacy-jwt-key-material-32-bytes")
	settings.PreviousKeys = `{"previous":"previous-field-key-material-32-bytes"}`
	legacyCipher, err := fieldcrypt.DeriveFieldEncryptor([]byte("legacy-jwt-key-material-32-bytes"), "push-target-uri")
	if err != nil {
		t.Fatal(err)
	}
	legacyStored, err := legacyCipher.Encrypt("rtmp://example.test/live/key")
	if err != nil {
		t.Fatal(err)
	}
	previousRing, err := fieldcrypt.NewFieldKeyring("previous", []byte("previous-field-key-material-32-bytes"), nil, nil, "push-target-uri")
	if err != nil {
		t.Fatal(err)
	}
	previousStored, err := previousRing.Encrypt("rtmps://example.test/live/old-key")
	if err != nil {
		t.Fatal(err)
	}

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for i, spec := range encryptedColumns {
		remaining := 3
		rows := sqlmock.NewRows([]string{spec.id, spec.column})
		if i == 0 {
			rows.AddRow("target-1", legacyStored).AddRow("target-2", previousStored)
		} else {
			remaining = 1
		}
		query := fieldEncryptionCandidateQuery(spec)
		mock.ExpectQuery(regexp.QuoteMeta(query)).WithArgs(remaining, "enc:v3:primary:", spec.table, spec.column, "", int64(0)).WillReturnRows(rows)
		if i == 0 {
			update := fmt.Sprintf(`WITH suppress AS MATERIALIZED (SELECT set_config('frameworks.suppress_media_authority_refresh', 'on', true) AS enabled) UPDATE %s SET %s = $1 FROM suppress WHERE %s::text = $2 AND %s = $3 AND suppress.enabled = 'on'`, spec.table, spec.column, spec.id, spec.column)
			mock.ExpectExec(regexp.QuoteMeta(update)).WithArgs(v3CiphertextArgument{}, "target-1", legacyStored).
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM commodore.field_encryption_quarantine WHERE table_name = $1 AND column_name = $2 AND row_id = $3`)).
				WithArgs(spec.table, spec.column, "target-1").WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectExec(regexp.QuoteMeta(update)).WithArgs(v3CiphertextArgument{}, "target-2", previousStored).
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM commodore.field_encryption_quarantine WHERE table_name = $1 AND column_name = $2 AND row_id = $3`)).
				WithArgs(spec.table, spec.column, "target-2").WillReturnResult(sqlmock.NewResult(0, 0))
		}
	}
	// The first sweep found rows, so the migration makes one empty sweep from
	// the beginning before declaring completion. This catches concurrent rows
	// inserted behind the keyset cursor and leaves a restartable checkpoint if
	// the framework's final invariant check races another writer.
	for _, spec := range encryptedColumns {
		query := fieldEncryptionCandidateQuery(spec)
		mock.ExpectQuery(regexp.QuoteMeta(query)).WithArgs(1, "enc:v3:primary:", spec.table, spec.column, "", int64(0)).
			WillReturnRows(sqlmock.NewRows([]string{spec.id, spec.column}))
	}
	expectFieldEncryptionOrphanPrune(mock)

	progress, err := runFieldEncryption(context.Background(), db, datamigrate.RunOptions{BatchSize: 3, Checkpoint: fieldEncryptionTestCheckpoint(t, "legacy-jwt-key-material-32-bytes")}, settings)
	if err != nil {
		t.Fatalf("runFieldEncryption: %v", err)
	}
	if progress.Scanned != 2 || progress.Changed != 2 || !progress.Done {
		t.Fatalf("unexpected progress: %+v", progress)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRunFieldEncryptionQuarantinesCorruptRowAndContinues(t *testing.T) {
	settings := testFieldEncryptionSettings("legacy-jwt-key-material-32-bytes")
	legacyCipher, err := fieldcrypt.DeriveFieldEncryptor([]byte("legacy-jwt-key-material-32-bytes"), "push-target-uri")
	if err != nil {
		t.Fatal(err)
	}
	legacyStored, err := legacyCipher.Encrypt("rtmp://example.test/live/key")
	if err != nil {
		t.Fatal(err)
	}
	const corruptStored = "enc:v3:missing:not-valid-base64"

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	spec := encryptedColumns[0]
	query := fieldEncryptionCandidateQuery(spec)
	mock.ExpectQuery(regexp.QuoteMeta(query)).WithArgs(2, "enc:v3:primary:", spec.table, spec.column, "", int64(0)).
		WillReturnRows(sqlmock.NewRows([]string{spec.id, spec.column}).
			AddRow("target-bad", corruptStored).AddRow("target-good", legacyStored))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO commodore.field_encryption_quarantine (table_name, column_name, row_id, ciphertext_fingerprint, purpose, error_code, last_observed_at) VALUES ($1, $2, $3, $4, $5, $6, clock_timestamp()) ON CONFLICT (table_name, column_name, row_id, ciphertext_fingerprint) DO UPDATE SET attempts = commodore.field_encryption_quarantine.attempts + 1, last_observed_at = clock_timestamp(), purpose = EXCLUDED.purpose, error_code = EXCLUDED.error_code`)).
		WithArgs(spec.table, spec.column, "target-bad", fieldEncryptionFingerprint(corruptStored), spec.purpose, fieldEncryptionDecryptError).
		WillReturnResult(sqlmock.NewResult(1, 1))
	update := fmt.Sprintf(`WITH suppress AS MATERIALIZED (SELECT set_config('frameworks.suppress_media_authority_refresh', 'on', true) AS enabled) UPDATE %s SET %s = $1 FROM suppress WHERE %s::text = $2 AND %s = $3 AND suppress.enabled = 'on'`, spec.table, spec.column, spec.id, spec.column)
	mock.ExpectExec(regexp.QuoteMeta(update)).WithArgs(v3CiphertextArgument{}, "target-good", legacyStored).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM commodore.field_encryption_quarantine WHERE table_name = $1 AND column_name = $2 AND row_id = $3`)).
		WithArgs(spec.table, spec.column, "target-good").WillReturnResult(sqlmock.NewResult(0, 0))

	progress, err := runFieldEncryption(context.Background(), db, datamigrate.RunOptions{BatchSize: 2, Checkpoint: fieldEncryptionTestCheckpoint(t, "legacy-jwt-key-material-32-bytes")}, settings)
	if err != nil {
		t.Fatalf("runFieldEncryption: %v", err)
	}
	if progress.Scanned != 2 || progress.Changed != 1 || progress.Skipped != 1 || progress.Errors != 1 || progress.Done {
		t.Fatalf("unexpected progress: %+v", progress)
	}
	var checkpoint fieldEncryptionCheckpoint
	if err := json.Unmarshal(progress.Checkpoint, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if checkpoint.Column != 0 || checkpoint.AfterID != "target-good" || !checkpoint.SweepFound {
		t.Fatalf("keyset checkpoint did not retain exact batch position: %+v", checkpoint)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRunFieldEncryptionDryRunReportsWithoutWriting(t *testing.T) {
	settings := testFieldEncryptionSettings("legacy-jwt-key-material-32-bytes")
	legacyCipher, err := fieldcrypt.DeriveFieldEncryptor([]byte("legacy-jwt-key-material-32-bytes"), "push-target-uri")
	if err != nil {
		t.Fatal(err)
	}
	legacyStored, err := legacyCipher.Encrypt("rtmp://example.test/live/key")
	if err != nil {
		t.Fatal(err)
	}

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	spec := encryptedColumns[0]
	mock.ExpectQuery(regexp.QuoteMeta(fieldEncryptionCandidateQuery(spec))).
		WithArgs(2, "enc:v3:primary:", spec.table, spec.column, "", int64(0)).
		WillReturnRows(sqlmock.NewRows([]string{spec.id, spec.column}).
			AddRow("target-bad", "enc:v3:missing:not-valid-base64").
			AddRow("target-good", legacyStored))
	mock.ExpectQuery(regexp.QuoteMeta(fieldEncryptionCandidateQuery(spec))).
		WithArgs(2, "enc:v3:primary:", spec.table, spec.column, "target-good", int64(0)).
		WillReturnRows(sqlmock.NewRows([]string{spec.id, spec.column}).
			AddRow("target-late", "enc:v3:primary:not-valid-base64"))
	expectEmptyDryRunColumns(mock, 2, encryptedColumns[1:])

	// A persisted mid-sweep position must not narrow the preview.
	checkpoint, err := json.Marshal(fieldEncryptionCheckpoint{
		Column:               3,
		AfterID:              "zzzz",
		LegacyKeyFingerprint: fieldEncryptionLegacyKeyFingerprint(settings, "legacy-jwt-key-material-32-bytes"),
	})
	if err != nil {
		t.Fatal(err)
	}
	progress, err := runFieldEncryption(context.Background(), db, datamigrate.RunOptions{
		BatchSize:  2,
		DryRun:     true,
		Checkpoint: checkpoint,
	}, settings)
	if err != nil {
		t.Fatal(err)
	}
	if progress.Scanned != 3 || progress.Changed != 1 || progress.Errors != 2 || progress.Skipped != 2 || progress.Done {
		t.Fatalf("unexpected dry-run progress: %+v", progress)
	}
	wantFindings := []string{
		"undecryptable table=commodore.push_targets column=target_uri row_id=target-bad error=unknown_key_id key_id=missing",
		"undecryptable table=commodore.push_targets column=target_uri row_id=target-late error=authentication_failed key_id=primary",
	}
	if progress.FindingsTotal != 2 || fmt.Sprint(progress.Findings) != fmt.Sprint(wantFindings) {
		t.Fatalf("dry run did not name every failing row: total=%d findings=%q", progress.FindingsTotal, progress.Findings)
	}
	if len(progress.Summary) != len(encryptedColumns) ||
		progress.Summary[0] != "scanned table=commodore.push_targets column=target_uri rows=3 undecryptable=2" ||
		progress.Summary[1] != "scanned table=commodore.stream_pull_sources column=source_uri_enc rows=0 undecryptable=0" {
		t.Fatalf("dry run summary = %q", progress.Summary)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func expectEmptyDryRunColumns(mock sqlmock.Sqlmock, batchSize int, specs []encryptedColumn) {
	for _, spec := range specs {
		mock.ExpectQuery(regexp.QuoteMeta(fieldEncryptionCandidateQuery(spec))).
			WithArgs(batchSize, "enc:v3:primary:", spec.table, spec.column, "", int64(0)).
			WillReturnRows(sqlmock.NewRows([]string{spec.id, spec.column}))
	}
}

func TestFieldEncryptionFailureKindNeverEchoesFieldMaterial(t *testing.T) {
	known := map[string]bool{"primary": true}
	for stored, want := range map[string]string{
		"enc:v3:retired:c2VjcmV0":   "unknown_key_id key_id=retired",
		"enc:v3:primary:c2VjcmV0":   "authentication_failed key_id=primary",
		"enc:v3:bad key!:c2VjcmV0":  "malformed_envelope",
		"enc:v3:no-separator":       "malformed_envelope",
		"enc:v1:c2VjcmV0c2VjcmV0":   "legacy_key_mismatch",
		"enc:v2:c2VjcmV0c2VjcmV0":   "legacy_key_mismatch",
		"enc:v9:c2VjcmV0c2VjcmV0":   fieldEncryptionDecryptError,
		"plaintext-should-not-fail": fieldEncryptionDecryptError,
	} {
		if got := fieldEncryptionFailureKind(stored, known); got != want {
			t.Fatalf("fieldEncryptionFailureKind(%q) = %q, want %q", stored, got, want)
		}
	}
}

func TestRunFieldEncryptionDryRunCapsFindings(t *testing.T) {
	settings := testFieldEncryptionSettings("legacy-jwt-key-material-32-bytes")
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	spec := encryptedColumns[0]
	const failing = fieldEncryptionFindingsLimit + 5
	rows := sqlmock.NewRows([]string{spec.id, spec.column})
	for i := 0; i < failing; i++ {
		rows.AddRow(fmt.Sprintf("target-%03d", i), "enc:v3:retired:not-valid-base64")
	}
	mock.ExpectQuery(regexp.QuoteMeta(fieldEncryptionCandidateQuery(spec))).
		WithArgs(failing, "enc:v3:primary:", spec.table, spec.column, "", int64(0)).
		WillReturnRows(rows)
	mock.ExpectQuery(regexp.QuoteMeta(fieldEncryptionCandidateQuery(spec))).
		WithArgs(failing, "enc:v3:primary:", spec.table, spec.column, fmt.Sprintf("target-%03d", failing-1), int64(0)).
		WillReturnRows(sqlmock.NewRows([]string{spec.id, spec.column}))
	expectEmptyDryRunColumns(mock, failing, encryptedColumns[1:])

	progress, err := runFieldEncryption(context.Background(), db, datamigrate.RunOptions{
		BatchSize:  failing,
		DryRun:     true,
		Checkpoint: fieldEncryptionTestCheckpoint(t, "legacy-jwt-key-material-32-bytes"),
	}, settings)
	if err != nil {
		t.Fatal(err)
	}
	if progress.FindingsTotal != failing || len(progress.Findings) != fieldEncryptionFindingsLimit {
		t.Fatalf("findings total=%d shown=%d, want total=%d shown=%d", progress.FindingsTotal, len(progress.Findings), failing, fieldEncryptionFindingsLimit)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyFieldEncryptionRejectsRemainingLegacyRows(t *testing.T) {
	settings := testFieldEncryptionSettings("legacy-jwt-key-material-32-bytes")
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	spec := encryptedColumns[0]
	query := fmt.Sprintf(`SELECT COUNT(*) FROM %s AS source WHERE source.%s IS NOT NULL AND LEFT(source.%s, char_length($1)) <> $1 AND NOT EXISTS (SELECT 1 FROM %s AS quarantine WHERE quarantine.table_name = $2 AND quarantine.column_name = $3 AND quarantine.row_id = source.%s::text AND quarantine.ciphertext_fingerprint = encode(digest(source.%s, 'sha256'), 'hex'))`, spec.table, spec.column, spec.column, fieldEncryptionQuarantineTable, spec.id, spec.column)
	mock.ExpectQuery(regexp.QuoteMeta(query)).WithArgs("enc:v3:primary:", spec.table, spec.column).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	if err := verifyFieldEncryption(context.Background(), db, settings); err == nil || !strings.Contains(err.Error(), "has 1 legacy rows") {
		t.Fatalf("expected legacy-row verification failure, got %v", err)
	}
}

func TestVerifyFieldEncryptionRejectsUnacknowledgedQuarantine(t *testing.T) {
	settings := testFieldEncryptionSettings("legacy-jwt-key-material-32-bytes")
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for index, spec := range encryptedColumns {
		query := fmt.Sprintf(`SELECT COUNT(*) FROM %s AS source WHERE source.%s IS NOT NULL AND LEFT(source.%s, char_length($1)) <> $1 AND NOT EXISTS (SELECT 1 FROM %s AS quarantine WHERE quarantine.table_name = $2 AND quarantine.column_name = $3 AND quarantine.row_id = source.%s::text AND quarantine.ciphertext_fingerprint = encode(digest(source.%s, 'sha256'), 'hex'))`, spec.table, spec.column, spec.column, fieldEncryptionQuarantineTable, spec.id, spec.column)
		mock.ExpectQuery(regexp.QuoteMeta(query)).WithArgs("enc:v3:primary:", spec.table, spec.column).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
		activeQuery := fmt.Sprintf(`SELECT COUNT(*) FROM %s AS quarantine WHERE quarantine.table_name = $1 AND quarantine.column_name = $2 AND EXISTS (SELECT 1 FROM %s AS source WHERE source.%s::text = quarantine.row_id AND source.%s IS NOT NULL AND quarantine.ciphertext_fingerprint = encode(digest(source.%s, 'sha256'), 'hex'))`, fieldEncryptionQuarantineTable, spec.table, spec.id, spec.column, spec.column)
		count := 0
		if index == 0 {
			count = 3
		}
		mock.ExpectQuery(regexp.QuoteMeta(activeQuery)).WithArgs(spec.table, spec.column).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(count))
	}
	if err := verifyFieldEncryption(context.Background(), db, settings); err == nil || !strings.Contains(err.Error(), "3 quarantined rows") {
		t.Fatalf("expected quarantine verification failure, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRunFieldEncryptionRejectsUnpreservedJWTChange(t *testing.T) {
	settings := testFieldEncryptionSettings("new-legacy-jwt-key-material-32-bytes")
	checkpoint := fmt.Sprintf(`{"column":0,"legacy_key_fingerprint":%q}`, fieldEncryptionFingerprint("old-legacy-jwt-key-material-32-bytes"))
	if _, err := runFieldEncryption(context.Background(), nil, datamigrate.RunOptions{BatchSize: 2, Checkpoint: []byte(checkpoint)}, settings); err == nil || !strings.Contains(err.Error(), "JWT_SECRET changed") {
		t.Fatalf("expected legacy-key rotation fence, got %v", err)
	}
}

func TestRunFieldEncryptionPersistsKeyFenceBeforeScanning(t *testing.T) {
	settings := testFieldEncryptionSettings("legacy-jwt-key-material-32-bytes")
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	expectEmptyLegacyFieldProbe(mock)
	progress, err := runFieldEncryption(context.Background(), db, datamigrate.RunOptions{BatchSize: 2}, settings)
	if err != nil {
		t.Fatal(err)
	}
	if progress.Done || progress.Scanned != 0 {
		t.Fatalf("initial run must only arm the key fence: %+v", progress)
	}
	var checkpoint fieldEncryptionCheckpoint
	if err := json.Unmarshal(progress.Checkpoint, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if checkpoint.LegacyKeyFingerprint != fieldEncryptionLegacyKeyFingerprint(settings, "legacy-jwt-key-material-32-bytes") {
		t.Fatalf("legacy key fingerprint was not persisted: %+v", checkpoint)
	}
	if checkpoint.LegacyKeyFingerprint == fieldEncryptionFingerprint("legacy-jwt-key-material-32-bytes") {
		t.Fatal("legacy key fingerprint remained an offline SHA-256 oracle")
	}
}

func TestRunFieldEncryptionRejectsWrongInitialLegacyKey(t *testing.T) {
	settings := testFieldEncryptionSettings("wrong-legacy-jwt-key-material-32-bytes")
	legacyCipher, err := fieldcrypt.DeriveFieldEncryptor([]byte("correct-legacy-jwt-key-material-32-bytes"), encryptedColumns[0].purpose)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := legacyCipher.Encrypt("rtmp://example.test/live/key")
	if err != nil {
		t.Fatal(err)
	}
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i, spec := range encryptedColumns {
		query := fmt.Sprintf(`SELECT %s FROM %s WHERE %s IS NOT NULL AND (LEFT(%s, 7) = 'enc:v1:' OR LEFT(%s, 7) = 'enc:v2:') ORDER BY %s LIMIT $1`, spec.column, spec.table, spec.column, spec.column, spec.column, spec.id)
		rows := sqlmock.NewRows([]string{spec.column})
		if i == 0 {
			rows.AddRow(stored)
		}
		mock.ExpectQuery(regexp.QuoteMeta(query)).WithArgs(fieldEncryptionLegacyProbeRows).WillReturnRows(rows)
	}
	if _, err := runFieldEncryption(context.Background(), db, datamigrate.RunOptions{}, settings); err == nil || !strings.Contains(err.Error(), "cannot decrypt any sampled legacy field") {
		t.Fatalf("wrong initial legacy key armed migration fence: %v", err)
	}
}

func TestRunFieldEncryptionRejectsMissingJWTBeforeScanning(t *testing.T) {
	if _, err := runFieldEncryption(context.Background(), nil, datamigrate.RunOptions{}, testFieldEncryptionSettings("")); err == nil || !strings.Contains(err.Error(), "JWT_SECRET is required") {
		t.Fatalf("expected missing JWT fence, got %v", err)
	}
}

func TestFieldEncryptionQuarantineRequeueIsCheckpointed(t *testing.T) {
	settings := testFieldEncryptionSettings("legacy-jwt-key-material-32-bytes")
	settings.RequeueQuarantine = "repair-2026-09-06-a"
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	expectEmptyLegacyFieldProbe(mock)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT floor(extract(epoch FROM clock_timestamp()) * 1000)::bigint`)).
		WillReturnRows(sqlmock.NewRows([]string{"epoch_millis"}).AddRow(int64(1234)))
	progress, err := runFieldEncryption(context.Background(), db, datamigrate.RunOptions{}, settings)
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint fieldEncryptionCheckpoint
	if err := json.Unmarshal(progress.Checkpoint, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if checkpoint.QuarantineRetryAfter <= 0 {
		t.Fatalf("quarantine retry epoch was not checkpointed: %+v", checkpoint)
	}
	if checkpoint.QuarantineRetryToken != "repair-2026-09-06-a" {
		t.Fatalf("quarantine retry token was not checkpointed: %+v", checkpoint)
	}
	query := fieldEncryptionCandidateQuery(encryptedColumns[0])
	if !strings.Contains(query, "last_observed_at >= to_timestamp($6") {
		t.Fatalf("candidate query does not bound retries by the checkpoint epoch: %s", query)
	}
}

func TestFieldEncryptionQuarantineRequeueAcceptsANewToken(t *testing.T) {
	settings := testFieldEncryptionSettings("legacy-jwt-key-material-32-bytes")
	settings.RequeueQuarantine = "repair-b"
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT floor(extract(epoch FROM clock_timestamp()) * 1000)::bigint`)).
		WillReturnRows(sqlmock.NewRows([]string{"epoch_millis"}).AddRow(int64(1234)))
	for _, spec := range encryptedColumns {
		mock.ExpectQuery(regexp.QuoteMeta(fieldEncryptionCandidateQuery(spec))).
			WithArgs(1, "enc:v3:primary:", spec.table, spec.column, "", sqlmock.AnyArg()).
			WillReturnRows(sqlmock.NewRows([]string{spec.id, spec.column}))
	}
	expectFieldEncryptionOrphanPrune(mock)
	checkpoint, err := json.Marshal(fieldEncryptionCheckpoint{
		Column: 3, AfterID: "stale-position",
		LegacyKeyFingerprint: fieldEncryptionLegacyKeyFingerprint(settings, "legacy-jwt-key-material-32-bytes"),
		QuarantineRetryAfter: 1, QuarantineRetryToken: "repair-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	progress, err := runFieldEncryption(context.Background(), db, datamigrate.RunOptions{BatchSize: 1, Checkpoint: checkpoint}, settings)
	if err != nil {
		t.Fatal(err)
	}
	var got fieldEncryptionCheckpoint
	if err := json.Unmarshal(progress.Checkpoint, &got); err != nil {
		t.Fatal(err)
	}
	if got.QuarantineRetryToken != "repair-b" || got.QuarantineRetryAfter <= 1 || got.AfterID != "" {
		t.Fatalf("new retry token did not restart the keyset sweep: %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func expectEmptyLegacyFieldProbe(mock sqlmock.Sqlmock) {
	for _, spec := range encryptedColumns {
		query := fmt.Sprintf(`SELECT %s FROM %s WHERE %s IS NOT NULL AND (LEFT(%s, 7) = 'enc:v1:' OR LEFT(%s, 7) = 'enc:v2:') ORDER BY %s LIMIT $1`, spec.column, spec.table, spec.column, spec.column, spec.column, spec.id)
		mock.ExpectQuery(regexp.QuoteMeta(query)).WithArgs(fieldEncryptionLegacyProbeRows).WillReturnRows(sqlmock.NewRows([]string{spec.column}))
	}
}

func expectFieldEncryptionOrphanPrune(mock sqlmock.Sqlmock) {
	for _, spec := range encryptedColumns {
		query := fmt.Sprintf(`DELETE FROM %s AS quarantine WHERE quarantine.table_name = $1 AND quarantine.column_name = $2 AND NOT EXISTS (SELECT 1 FROM %s AS source WHERE source.%s::text = quarantine.row_id AND source.%s IS NOT NULL AND quarantine.ciphertext_fingerprint = encode(digest(source.%s, 'sha256'), 'hex'))`, fieldEncryptionQuarantineTable, spec.table, spec.id, spec.column, spec.column)
		mock.ExpectExec(regexp.QuoteMeta(query)).WithArgs(spec.table, spec.column).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}
}
