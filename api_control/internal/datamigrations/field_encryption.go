package datamigrations

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	fieldcrypt "github.com/Livepeer-FrameWorks/monorepo/pkg/crypto"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
)

const FieldEncryptionID = "commodore_field_encryption_v0_3_0"

type encryptedColumn struct {
	table   string
	id      string
	column  string
	purpose string
	idUUID  bool
}

const (
	fieldEncryptionQuarantineTable = "commodore.field_encryption_quarantine"
	fieldEncryptionDecryptError    = "decrypt_failed"
	fieldEncryptionAllowQuarantine = "FIELD_ENCRYPTION_ALLOW_QUARANTINE"
	fieldEncryptionRetryQuarantine = "FIELD_ENCRYPTION_REQUEUE_QUARANTINE"
	fieldEncryptionAckLegacyProbe  = "FIELD_ENCRYPTION_ACK_UNVERIFIED_LEGACY_KEY"
	fieldEncryptionLegacySecrets   = "FIELD_ENCRYPTION_LEGACY_SECRETS"
	fieldEncryptionLegacyProbeRows = 32
)

type fieldEncryptionCheckpoint struct {
	Column               int    `json:"column"`
	AfterID              string `json:"after_id,omitempty"`
	LegacyKeyFingerprint string `json:"legacy_key_fingerprint"`
	QuarantineRetryAfter int64  `json:"quarantine_retry_after_ms,omitempty"`
	QuarantineRetryToken string `json:"quarantine_retry_token,omitempty"`
	SweepFound           bool   `json:"sweep_found,omitempty"`
}

var encryptedColumns = []encryptedColumn{
	{table: "commodore.push_targets", id: "id", column: "target_uri", purpose: "push-target-uri", idUUID: true},
	{table: "commodore.stream_pull_sources", id: "stream_id", column: "source_uri_enc", purpose: "pull-source-uri", idUUID: true},
	{table: "commodore.streams", id: "id", column: "playback_webhook_secret_enc", purpose: "playback-webhook-secret", idUUID: true},
	{table: "commodore.vod_assets", id: "vod_hash", column: "playback_webhook_secret_enc", purpose: "playback-webhook-secret"},
	{table: "commodore.clips", id: "clip_hash", column: "playback_webhook_secret_enc", purpose: "playback-webhook-secret"},
	{table: "commodore.dvr_recordings", id: "dvr_hash", column: "playback_webhook_secret_enc", purpose: "playback-webhook-secret"},
}

func registerFieldEncryption() {
	datamigrate.Register(datamigrate.Migration{
		ID: FieldEncryptionID, Service: "commodore", IntroducedIn: "v0.3.0",
		RequiredBeforePhase: "postdeploy",
		Description:         "re-encrypt application fields with the dedicated versioned field keyring",
		Run:                 runFieldEncryption,
		Verify:              verifyFieldEncryption,
	})
}

func fieldKeyring(purpose string) (*fieldcrypt.FieldKeyring, error) {
	active := []byte(strings.TrimSpace(os.Getenv("FIELD_ENCRYPTION_KEY")))
	if len(active) == 0 {
		return nil, fmt.Errorf("FIELD_ENCRYPTION_KEY is required")
	}
	previous, err := fieldcrypt.ParseFieldKeySet(os.Getenv("FIELD_ENCRYPTION_PREVIOUS_KEYS"))
	if err != nil {
		return nil, err
	}
	explicitLegacy, err := fieldcrypt.ParseLegacyFieldSecrets(os.Getenv(fieldEncryptionLegacySecrets))
	if err != nil {
		return nil, err
	}
	legacy := []byte(strings.TrimSpace(os.Getenv("JWT_SECRET")))
	legacyKeys := make([][]byte, 0, len(previous)+len(explicitLegacy)+1)
	legacyKeys = append(legacyKeys, legacy)
	legacyKeys = append(legacyKeys, explicitLegacy...)
	legacyKeys = append(legacyKeys, sortedFieldKeySecrets(previous)...)
	return fieldcrypt.NewFieldKeyring(
		strings.TrimSpace(envDefault("FIELD_ENCRYPTION_KEY_ID", "primary")),
		active, previous, legacyKeys, purpose,
	)
}

func envDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func runFieldEncryption(ctx context.Context, db datamigrate.DB, opts datamigrate.RunOptions) (datamigrate.Progress, error) {
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = 500
	}
	checkpoint := fieldEncryptionCheckpoint{}
	if len(opts.Checkpoint) > 0 && string(opts.Checkpoint) != "{}" {
		if err := json.Unmarshal(opts.Checkpoint, &checkpoint); err != nil {
			return datamigrate.Progress{}, fmt.Errorf("decode field-encryption checkpoint: %w", err)
		}
	}
	legacySecret := strings.TrimSpace(os.Getenv("JWT_SECRET"))
	if legacySecret == "" {
		return datamigrate.Progress{}, errors.New("JWT_SECRET is required to fence legacy field decryption")
	}
	if _, err := fieldKeyring(encryptedColumns[0].purpose); err != nil {
		return datamigrate.Progress{}, fmt.Errorf("configure field-encryption keyring: %w", err)
	}
	legacyFingerprint := fieldEncryptionLegacyKeyFingerprint(legacySecret)
	retryToken := quarantineRetryToken()
	if checkpoint.LegacyKeyFingerprint == "" {
		if err := validateInitialLegacyFieldKey(ctx, db); err != nil {
			return datamigrate.Progress{}, err
		}
		checkpoint.LegacyKeyFingerprint = legacyFingerprint
		if retryToken != "" {
			retryAfter, err := fieldEncryptionDatabaseEpochMillis(ctx, db)
			if err != nil {
				return datamigrate.Progress{}, err
			}
			checkpoint.QuarantineRetryAfter = retryAfter
			checkpoint.QuarantineRetryToken = retryToken
		}
		encoded, err := json.Marshal(checkpoint)
		if err != nil {
			return datamigrate.Progress{}, fmt.Errorf("encode initial field-encryption checkpoint: %w", err)
		}
		// Persist the key identity before touching any ciphertext. If the process
		// restarts with a different JWT_SECRET, the next invocation fails closed
		// before it can quarantine otherwise recoverable legacy rows.
		if !opts.DryRun {
			return datamigrate.Progress{Checkpoint: encoded}, nil
		}
	}
	if checkpoint.LegacyKeyFingerprint == fieldEncryptionFingerprint(legacySecret) {
		// Upgrade checkpoints written before legacy-key fingerprints were keyed.
		checkpoint.LegacyKeyFingerprint = legacyFingerprint
	}
	if checkpoint.LegacyKeyFingerprint != "" && checkpoint.LegacyKeyFingerprint != legacyFingerprint {
		previous, err := fieldcrypt.ParseFieldKeySet(os.Getenv("FIELD_ENCRYPTION_PREVIOUS_KEYS"))
		if err != nil {
			return datamigrate.Progress{}, err
		}
		explicitLegacy, err := fieldcrypt.ParseLegacyFieldSecrets(os.Getenv(fieldEncryptionLegacySecrets))
		if err != nil {
			return datamigrate.Progress{}, err
		}
		legacyCandidates := append([][]byte{[]byte(legacySecret)}, explicitLegacy...)
		fingerprintKeys := [][]byte{[]byte(strings.TrimSpace(os.Getenv("FIELD_ENCRYPTION_KEY")))}
		for _, secret := range sortedFieldKeySecrets(previous) {
			legacyCandidates = append(legacyCandidates, secret)
			fingerprintKeys = append(fingerprintKeys, secret)
		}
		priorAvailable := false
		for _, secret := range legacyCandidates {
			if fieldEncryptionFingerprint(string(secret)) == checkpoint.LegacyKeyFingerprint {
				priorAvailable = true
				break
			}
			for _, fingerprintKey := range fingerprintKeys {
				if fieldEncryptionLegacyKeyFingerprintWithKey(string(secret), fingerprintKey) == checkpoint.LegacyKeyFingerprint {
					priorAvailable = true
					break
				}
			}
			if priorAvailable {
				break
			}
		}
		if !priorAvailable {
			return datamigrate.Progress{}, errors.New("JWT_SECRET changed while legacy field encryption migration was in progress; restore it or provide the prior secret in FIELD_ENCRYPTION_LEGACY_SECRETS")
		}
	}
	checkpoint.LegacyKeyFingerprint = legacyFingerprint
	if retryToken != "" && retryToken != checkpoint.QuarantineRetryToken {
		retryAfter, err := fieldEncryptionDatabaseEpochMillis(ctx, db)
		if err != nil {
			return datamigrate.Progress{}, err
		}
		checkpoint.QuarantineRetryAfter = retryAfter
		checkpoint.QuarantineRetryToken = retryToken
		checkpoint.Column = 0
		checkpoint.AfterID = ""
		checkpoint.SweepFound = false
	}
	remaining := batchSize
	var progress datamigrate.Progress
	done := false
	for checkpoint.Column < len(encryptedColumns) {
		if remaining == 0 {
			break
		}
		spec := encryptedColumns[checkpoint.Column]
		limit := remaining
		cipher, err := fieldKeyring(spec.purpose)
		if err != nil {
			return progress, fmt.Errorf("configure %s keyring: %w", spec.purpose, err)
		}
		query := fieldEncryptionCandidateQuery(spec)
		type candidate struct{ id, stored string }
		candidates, err := func() ([]candidate, error) {
			rows, queryErr := db.QueryContext(ctx, query, limit, cipher.ActiveEnvelopePrefix(), spec.table, spec.column, checkpoint.AfterID, checkpoint.QuarantineRetryAfter)
			if queryErr != nil {
				return nil, fmt.Errorf("select %s.%s: %w", spec.table, spec.column, queryErr)
			}
			defer rows.Close() //nolint:errcheck // a scan/iteration error is more actionable than a redundant close error
			var found []candidate
			for rows.Next() {
				var row candidate
				if scanErr := rows.Scan(&row.id, &row.stored); scanErr != nil {
					return nil, fmt.Errorf("scan %s.%s: %w", spec.table, spec.column, scanErr)
				}
				found = append(found, row)
			}
			if rowsErr := rows.Err(); rowsErr != nil {
				return nil, fmt.Errorf("iterate %s.%s: %w", spec.table, spec.column, rowsErr)
			}
			return found, nil
		}()
		if err != nil {
			return progress, err
		}
		if len(candidates) > 0 {
			checkpoint.SweepFound = true
		}
		for _, row := range candidates {
			checkpoint.AfterID = row.id
			progress.Scanned++
			plain, err := cipher.Decrypt(row.stored)
			if err != nil {
				if !opts.DryRun {
					if quarantineErr := quarantineFieldEncryptionFailure(ctx, db, spec, row); quarantineErr != nil {
						return progress, fmt.Errorf("quarantine undecryptable %s.%s row %s: %w", spec.table, spec.column, row.id, quarantineErr)
					}
				}
				progress.Errors++
				progress.Skipped++
				continue
			}
			stored, err := cipher.Encrypt(plain)
			if err != nil {
				return progress, fmt.Errorf("encrypt %s.%s row %s: %w", spec.table, spec.column, row.id, err)
			}
			if opts.DryRun {
				progress.Changed++
				continue
			}
			update := fmt.Sprintf(`WITH suppress AS MATERIALIZED (SELECT set_config('frameworks.suppress_media_authority_refresh', 'on', true) AS enabled) UPDATE %s SET %s = $1 FROM suppress WHERE %s::text = $2 AND %s = $3 AND suppress.enabled = 'on'`, spec.table, spec.column, spec.id, spec.column)
			result, err := db.ExecContext(ctx, update, stored, row.id, row.stored)
			if err != nil {
				return progress, fmt.Errorf("update %s.%s row %s: %w", spec.table, spec.column, row.id, err)
			}
			changed, err := result.RowsAffected()
			if err != nil {
				return progress, fmt.Errorf("count %s.%s update: %w", spec.table, spec.column, err)
			}
			progress.Changed += changed
			if changed == 0 {
				progress.Skipped++
				continue
			}
			if clearErr := clearFieldEncryptionQuarantine(ctx, db, spec, row.id); clearErr != nil {
				return progress, fmt.Errorf("clear quarantine for %s.%s row %s: %w", spec.table, spec.column, row.id, clearErr)
			}
		}
		remaining -= len(candidates)
		if len(candidates) < limit {
			checkpoint.Column++
			checkpoint.AfterID = ""
			if checkpoint.Column >= len(encryptedColumns) {
				if checkpoint.SweepFound {
					// A second empty sweep catches rows inserted behind the
					// keyset cursor. If final verification still races a writer,
					// persist a restartable checkpoint rather than a terminal one.
					checkpoint.Column = 0
					checkpoint.SweepFound = false
				} else {
					done = true
					checkpoint.Column = 0
					break
				}
			}
		}
	}
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		return progress, fmt.Errorf("encode field-encryption checkpoint: %w", err)
	}
	if done && !opts.DryRun {
		if err := pruneOrphanFieldEncryptionQuarantine(ctx, db); err != nil {
			return progress, err
		}
	}
	progress.Checkpoint = encoded
	progress.Done = done
	return progress, nil
}

func sortedFieldKeySecrets(keys map[string][]byte) [][]byte {
	ids := make([]string, 0, len(keys))
	for id := range keys {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([][]byte, 0, len(ids))
	for _, id := range ids {
		out = append(out, keys[id])
	}
	return out
}

// validateInitialLegacyFieldKey prevents a wrong starting JWT_SECRET from
// becoming the persisted migration authority. Plaintext and v3 rows do not
// prove the legacy key; only an authenticated v1/v2 decrypt does.
func validateInitialLegacyFieldKey(ctx context.Context, db datamigrate.DB) error {
	foundLegacy := false
	for _, spec := range encryptedColumns {
		matched, observed, err := func() (bool, bool, error) {
			query := fmt.Sprintf(`SELECT %s FROM %s WHERE %s IS NOT NULL AND (LEFT(%s, 7) = 'enc:v1:' OR LEFT(%s, 7) = 'enc:v2:') ORDER BY %s LIMIT $1`, spec.column, spec.table, spec.column, spec.column, spec.column, spec.id)
			rows, err := db.QueryContext(ctx, query, fieldEncryptionLegacyProbeRows)
			if err != nil {
				return false, false, fmt.Errorf("probe legacy %s.%s: %w", spec.table, spec.column, err)
			}
			defer func() { _ = rows.Close() }()
			cipher, err := fieldKeyring(spec.purpose)
			if err != nil {
				return false, false, fmt.Errorf("configure %s legacy-key probe: %w", spec.purpose, err)
			}
			observed := false
			for rows.Next() {
				var stored string
				if err := rows.Scan(&stored); err != nil {
					return false, observed, fmt.Errorf("scan legacy %s.%s: %w", spec.table, spec.column, err)
				}
				observed = true
				if _, err := cipher.Decrypt(stored); err == nil {
					return true, observed, nil
				}
			}
			if err := rows.Err(); err != nil {
				return false, observed, fmt.Errorf("iterate legacy %s.%s: %w", spec.table, spec.column, err)
			}
			return false, observed, nil
		}()
		if err != nil {
			return err
		}
		foundLegacy = foundLegacy || observed
		if matched {
			return nil
		}
	}
	if foundLegacy {
		if strings.TrimSpace(os.Getenv(fieldEncryptionAckLegacyProbe)) == "I_ACCEPT_UNVERIFIED_LEGACY_KEY" {
			return nil
		}
		return errors.New("JWT_SECRET and configured previous keys cannot decrypt any sampled legacy field; refusing to arm migration fence")
	}
	return nil
}

func fieldEncryptionCandidateQuery(spec encryptedColumn) string {
	keysetPredicate := fmt.Sprintf("source.%s > $5", spec.id)
	if spec.idUUID {
		keysetPredicate = fmt.Sprintf("($5 = '' OR source.%s > NULLIF($5, '')::uuid)", spec.id)
	}
	return fmt.Sprintf(`SELECT source.%s::text, source.%s FROM %s AS source WHERE source.%s IS NOT NULL AND %s AND LEFT(source.%s, char_length($2)) <> $2 AND NOT EXISTS (SELECT 1 FROM %s AS quarantine WHERE quarantine.table_name = $3 AND quarantine.column_name = $4 AND quarantine.row_id = source.%s::text AND quarantine.ciphertext_fingerprint = encode(digest(source.%s, 'sha256'), 'hex') AND ($6::bigint = 0 OR quarantine.last_observed_at >= to_timestamp($6::double precision / 1000.0))) ORDER BY source.%s LIMIT $1`, spec.id, spec.column, spec.table, spec.column, keysetPredicate, spec.column, fieldEncryptionQuarantineTable, spec.id, spec.column, spec.id)
}

func quarantineRetryToken() string {
	value := strings.TrimSpace(os.Getenv(fieldEncryptionRetryQuarantine))
	if value == "" || value == "0" || strings.EqualFold(value, "false") {
		return ""
	}
	return value
}

func fieldEncryptionDatabaseEpochMillis(ctx context.Context, db datamigrate.DB) (int64, error) {
	var epochMillis int64
	if err := db.QueryRowContext(ctx, `SELECT floor(extract(epoch FROM clock_timestamp()) * 1000)::bigint`).Scan(&epochMillis); err != nil {
		return 0, fmt.Errorf("start field-encryption quarantine retry epoch: %w", err)
	}
	return epochMillis, nil
}

func fieldEncryptionLegacyKeyFingerprint(secret string) string {
	return fieldEncryptionLegacyKeyFingerprintWithKey(secret, []byte(strings.TrimSpace(os.Getenv("FIELD_ENCRYPTION_KEY"))))
}

func fieldEncryptionLegacyKeyFingerprintWithKey(secret string, key []byte) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("frameworks-field-encryption-legacy-fence-v1\x00"))
	_, _ = mac.Write([]byte(secret))
	return fmt.Sprintf("hmac-sha256:%x", mac.Sum(nil))
}

func fieldEncryptionFingerprint(stored string) string {
	sum := sha256.Sum256([]byte(stored))
	return fmt.Sprintf("%x", sum[:])
}

func quarantineFieldEncryptionFailure(ctx context.Context, db datamigrate.DB, spec encryptedColumn, row struct{ id, stored string }) error {
	const query = `INSERT INTO commodore.field_encryption_quarantine (table_name, column_name, row_id, ciphertext_fingerprint, purpose, error_code, last_observed_at) VALUES ($1, $2, $3, $4, $5, $6, clock_timestamp()) ON CONFLICT (table_name, column_name, row_id, ciphertext_fingerprint) DO UPDATE SET attempts = commodore.field_encryption_quarantine.attempts + 1, last_observed_at = clock_timestamp(), purpose = EXCLUDED.purpose, error_code = EXCLUDED.error_code`
	_, err := db.ExecContext(ctx, query, spec.table, spec.column, row.id, fieldEncryptionFingerprint(row.stored), spec.purpose, fieldEncryptionDecryptError)
	return err
}

func clearFieldEncryptionQuarantine(ctx context.Context, db datamigrate.DB, spec encryptedColumn, rowID string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM commodore.field_encryption_quarantine WHERE table_name = $1 AND column_name = $2 AND row_id = $3`, spec.table, spec.column, rowID)
	return err
}

func pruneOrphanFieldEncryptionQuarantine(ctx context.Context, db datamigrate.DB) error {
	for _, spec := range encryptedColumns {
		query := fmt.Sprintf(`DELETE FROM %s AS quarantine WHERE quarantine.table_name = $1 AND quarantine.column_name = $2 AND NOT EXISTS (SELECT 1 FROM %s AS source WHERE source.%s::text = quarantine.row_id AND source.%s IS NOT NULL AND quarantine.ciphertext_fingerprint = encode(digest(source.%s, 'sha256'), 'hex'))`, fieldEncryptionQuarantineTable, spec.table, spec.id, spec.column, spec.column)
		if _, err := db.ExecContext(ctx, query, spec.table, spec.column); err != nil {
			return fmt.Errorf("prune %s.%s orphan quarantine: %w", spec.table, spec.column, err)
		}
	}
	return nil
}

func verifyFieldEncryption(ctx context.Context, db datamigrate.DB) error {
	var quarantined int64
	for _, spec := range encryptedColumns {
		cipher, err := fieldKeyring(spec.purpose)
		if err != nil {
			return fmt.Errorf("configure %s keyring: %w", spec.purpose, err)
		}
		query := fmt.Sprintf(`SELECT COUNT(*) FROM %s AS source WHERE source.%s IS NOT NULL AND LEFT(source.%s, char_length($1)) <> $1 AND NOT EXISTS (SELECT 1 FROM %s AS quarantine WHERE quarantine.table_name = $2 AND quarantine.column_name = $3 AND quarantine.row_id = source.%s::text AND quarantine.ciphertext_fingerprint = encode(digest(source.%s, 'sha256'), 'hex'))`, spec.table, spec.column, spec.column, fieldEncryptionQuarantineTable, spec.id, spec.column)
		var remaining int64
		if err := db.QueryRowContext(ctx, query, cipher.ActiveEnvelopePrefix(), spec.table, spec.column).Scan(&remaining); err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("verify %s.%s: %w", spec.table, spec.column, err)
		}
		if remaining != 0 {
			return fmt.Errorf("%s.%s has %d legacy rows", spec.table, spec.column, remaining)
		}
		activeQuarantineQuery := fmt.Sprintf(`SELECT COUNT(*) FROM %s AS quarantine WHERE quarantine.table_name = $1 AND quarantine.column_name = $2 AND EXISTS (SELECT 1 FROM %s AS source WHERE source.%s::text = quarantine.row_id AND source.%s IS NOT NULL AND quarantine.ciphertext_fingerprint = encode(digest(source.%s, 'sha256'), 'hex'))`, fieldEncryptionQuarantineTable, spec.table, spec.id, spec.column, spec.column)
		var activeForColumn int64
		if err := db.QueryRowContext(ctx, activeQuarantineQuery, spec.table, spec.column).Scan(&activeForColumn); err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("verify %s.%s quarantine: %w", spec.table, spec.column, err)
		}
		quarantined += activeForColumn
	}
	allowQuarantine := strings.EqualFold(strings.TrimSpace(os.Getenv(fieldEncryptionAllowQuarantine)), "true") || strings.TrimSpace(os.Getenv(fieldEncryptionAllowQuarantine)) == "1"
	if quarantined > 0 && !allowQuarantine {
		return fmt.Errorf("field encryption has %d quarantined rows; repair them or explicitly acknowledge with %s=true", quarantined, fieldEncryptionAllowQuarantine)
	}
	return nil
}
