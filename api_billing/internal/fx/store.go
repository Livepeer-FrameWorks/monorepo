package fx

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"frameworks/api_billing/internal/database/purserdb"

	"github.com/shopspring/decimal"
)

// Upsert stores rates in purser.fx_rates, replacing a stored rate for the same
// currency and reference date.
func Upsert(ctx context.Context, db purserdb.DBTX, rates []Rate, fetchedAt time.Time) error {
	queries := purserdb.New(db)
	for _, rate := range rates {
		if rate.Currency != USD && rate.Currency != GBP {
			return fmt.Errorf("%w: %q", ErrUnsupportedCurrency, rate.Currency)
		}
		if !rate.UnitsPerEUR.IsPositive() {
			return fmt.Errorf("fx: %s rate on %s must be positive", rate.Currency, rate.ReferenceDate.Format(time.DateOnly))
		}
		if err := queries.UpsertFXRate(ctx, purserdb.UpsertFXRateParams{
			Currency:      rate.Currency,
			ReferenceDate: Date(rate.ReferenceDate),
			UnitsPerEur:   rate.UnitsPerEUR.String(),
			FetchedAt:     fetchedAt.UTC(),
		}); err != nil {
			return fmt.Errorf("store %s rate on %s: %w", rate.Currency, rate.ReferenceDate.Format(time.DateOnly), err)
		}
	}
	return nil
}

// Lookup returns the latest stored rate for currency on or before on. It
// returns ErrNoRate when none exists and ErrStaleRate when the latest one is
// more than MaxReferenceAgeDays calendar days before on. EUR returns the
// identity rate for on.
func Lookup(ctx context.Context, db purserdb.DBTX, currency string, on time.Time) (Rate, error) {
	if currency == EUR {
		return Identity(on), nil
	}
	if currency != USD && currency != GBP {
		return Rate{}, fmt.Errorf("%w: %q", ErrUnsupportedCurrency, currency)
	}
	row, err := purserdb.New(db).GetFXRateOnOrBefore(ctx, purserdb.GetFXRateOnOrBeforeParams{
		Currency: currency,
		OnDate:   Date(on),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Rate{}, fmt.Errorf("%w: %s on %s", ErrNoRate, currency, Date(on).Format(time.DateOnly))
	}
	if err != nil {
		return Rate{}, fmt.Errorf("look up %s rate: %w", currency, err)
	}
	units, err := decimal.NewFromString(row.UnitsPerEur)
	if err != nil {
		return Rate{}, fmt.Errorf("stored %s rate %q: %w", currency, row.UnitsPerEur, err)
	}
	rate := Rate{Currency: row.Currency, ReferenceDate: Date(row.ReferenceDate), UnitsPerEUR: units, Source: row.Source}
	if age := AgeDays(rate.ReferenceDate, on); age > MaxReferenceAgeDays {
		return Rate{}, fmt.Errorf("%w: latest %s rate is from %s, %d days before %s", ErrStaleRate,
			currency, rate.ReferenceDate.Format(time.DateOnly), age, Date(on).Format(time.DateOnly))
	}
	return rate, nil
}

// LatestReferenceDates returns the newest stored reference date per currency.
func LatestReferenceDates(ctx context.Context, db purserdb.DBTX) (map[string]time.Time, error) {
	rows, err := purserdb.New(db).ListLatestFXRateReferenceDates(ctx)
	if err != nil {
		return nil, fmt.Errorf("list latest reference dates: %w", err)
	}
	latest := make(map[string]time.Time, len(rows))
	for _, row := range rows {
		latest[row.Currency] = Date(row.ReferenceDate)
	}
	return latest, nil
}
