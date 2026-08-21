package handlers

import (
	"context"
	"database/sql"
	"encoding/json"

	"frameworks/api_billing/internal/database/purserdb"

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
	err = purserdb.New(db).RecordCryptoAccountingAnomaly(ctx, purserdb.RecordCryptoAccountingAnomalyParams{
		TenantID: tenantID, Kind: kind, Network: network, ReferenceType: referenceType,
		ReferenceID: referenceID, AmountCents: amountCents, Detail: detail, EvidenceJson: evidenceJSON,
	})
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
	_, err := purserdb.New(db).ResolveCryptoAccountingAnomaly(ctx, purserdb.ResolveCryptoAccountingAnomalyParams{
		Note: sql.NullString{String: note, Valid: true}, Kind: kind,
		ReferenceType: referenceType, ReferenceID: referenceID,
	})
	if err != nil && logger != nil {
		logger.WithError(err).WithFields(logging.Fields{
			"kind": kind, "reference_type": referenceType, "reference_id": referenceID,
		}).Error("Failed to resolve crypto accounting anomaly")
	}
}
