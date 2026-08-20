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
		    evidence_json = EXCLUDED.evidence_json
	`, tenantID, kind, network, referenceType, referenceID, amountCents, detail, database.JSONText(evidenceJSON))
	if err != nil && logger != nil {
		logger.WithError(err).WithFields(logging.Fields{
			"tenant_id": tenantID, "kind": kind, "reference_id": referenceID,
		}).Error("Failed to persist crypto accounting anomaly")
	}
}
