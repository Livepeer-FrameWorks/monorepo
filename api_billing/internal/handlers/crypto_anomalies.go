package handlers

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func recordCryptoAccountingAnomaly(
	ctx context.Context,
	db *sql.DB,
	logger logging.Logger,
	tenantID, kind, network, referenceType, referenceID string,
	amountCents int64,
	detail string,
	evidence map[string]any,
) {
	evidenceJSON, err := json.Marshal(evidence)
	if err != nil {
		evidenceJSON = []byte(`{}`)
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO purser.crypto_accounting_anomalies (
			tenant_id, kind, network, reference_type, reference_id,
			amount_cents, currency, detail, evidence_json
		) VALUES ($1, $2, $3, $4, $5, $6, 'EUR', $7, $8)
		ON CONFLICT (kind, reference_type, reference_id) DO UPDATE
		SET last_seen_at = NOW(),
		    occurrences = purser.crypto_accounting_anomalies.occurrences + 1,
		    detail = EXCLUDED.detail,
		    evidence_json = EXCLUDED.evidence_json,
		    status = 'open',
		    resolved_at = NULL,
		    resolved_by = NULL,
		    resolution_note = NULL
	`, tenantID, kind, network, referenceType, referenceID, amountCents, detail, database.JSONText(evidenceJSON))
	if err != nil && logger != nil {
		logger.WithError(err).WithFields(logging.Fields{
			"tenant_id": tenantID, "kind": kind, "reference_id": referenceID,
		}).Error("Failed to persist crypto accounting anomaly")
	}
}

func resolveCryptoAccountingAnomaly(
	ctx context.Context,
	db *sql.DB,
	logger logging.Logger,
	kind, referenceType, referenceID, note string,
) {
	_, err := db.ExecContext(ctx, `
		UPDATE purser.crypto_accounting_anomalies
		SET status = 'resolved', resolved_at = NOW(), resolution_note = $4
		WHERE kind = $1 AND reference_type = $2 AND reference_id = $3
		  AND status = 'open'
	`, kind, referenceType, referenceID, note)
	if err != nil && logger != nil {
		logger.WithError(err).WithFields(logging.Fields{
			"kind": kind, "reference_type": referenceType, "reference_id": referenceID,
		}).Error("Failed to resolve crypto accounting anomaly")
	}
}
