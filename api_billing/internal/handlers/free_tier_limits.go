package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"frameworks/api_billing/internal/database/purserdb"
	"math"
)

// FreeTierLimits is the target tier plus the subscription's reviewed overlays.
// Missing or malformed Free caps cannot be interpreted as unlimited service.
type FreeTierLimits struct {
	StorageBytes  int64
	RetentionDays int32
}

func loadFreeTierLimits(ctx context.Context, db purserdb.DBTX, tierID, subscriptionID string) (FreeTierLimits, error) {
	q := purserdb.New(db)
	rows, err := q.ListTierEntitlements(ctx, tierID)
	if err != nil {
		return FreeTierLimits{}, err
	}
	values := map[string]string{}
	for _, row := range rows {
		values[row.Key] = row.Value
	}
	overrides, err := q.ListSubscriptionEntitlementOverrides(ctx, subscriptionID)
	if err != nil {
		return FreeTierLimits{}, err
	}
	for _, row := range overrides {
		values[row.Key] = row.Value
	}
	read := func(key string, maximum int64) (int64, error) {
		raw, ok := values[key]
		if !ok {
			return 0, fmt.Errorf("free limit %s is missing", key)
		}
		var n *int64
		if decodeErr := json.Unmarshal([]byte(raw), &n); decodeErr != nil || n == nil || *n < 0 || *n > maximum {
			return 0, fmt.Errorf("free limit %s is not a valid non-negative integer", key)
		}
		return *n, nil
	}
	gib, err := read("storage_limit_gb", math.MaxInt64/(1<<30))
	if err != nil {
		return FreeTierLimits{}, err
	}
	days, err := read("recording_retention_days", math.MaxInt32)
	if err != nil {
		return FreeTierLimits{}, err
	}
	return FreeTierLimits{StorageBytes: gib * (1 << 30), RetentionDays: int32(days)}, nil
}

var ErrFreeTierNotEligible = errors.New("free limits or existing financial commitments are incompatible")

// CheckFreeTierChoiceTx is a compatibility diagnostic for reviewed operator changes.
// Automatic fallback applies Free limits per operation and does not use this diagnostic.
func (s *Service) CheckFreeTierChoiceTx(ctx context.Context, tx *sql.Tx, tenantID, tierID, subscriptionID string) error {
	if s == nil || s.freeTierEligibility == nil {
		return errors.New("free resource proof unavailable")
	}
	held, err := purserdb.New(tx).FreeTierChoiceHasFinancialHold(ctx, tenantID)
	if err != nil {
		return err
	}
	if held {
		return ErrFreeTierNotEligible
	}
	limits, err := loadFreeTierLimits(ctx, tx, tierID, subscriptionID)
	if err != nil {
		return err
	}
	eligible, err := s.freeTierEligibility(ctx, tenantID, limits)
	if err != nil {
		return err
	}
	if !eligible {
		return ErrFreeTierNotEligible
	}
	return nil
}

// ReadFreeTierLimits resolves the target catalog and reviewed subscription overlays.
func ReadFreeTierLimits(ctx context.Context, db purserdb.DBTX, tierID, subscriptionID string) (FreeTierLimits, error) {
	return loadFreeTierLimits(ctx, db, tierID, subscriptionID)
}
