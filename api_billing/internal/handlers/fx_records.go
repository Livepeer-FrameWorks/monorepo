package handlers

import (
	"errors"
	"time"

	"frameworks/api_billing/internal/fx"
)

// storedFXRecord returns the FX record read from a row's FX columns, or nil
// when the row was written before FX fields existed.
func storedFXRecord(originalMinor int64, originalCurrency string, eurMinor int64, unitsPerEUR, source string, referenceDate time.Time) (*fx.Record, error) {
	record, err := fx.FromColumns(originalMinor, originalCurrency, eurMinor, unitsPerEUR, source, referenceDate)
	if errors.Is(err, fx.ErrMissingRecord) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &record, nil
}
