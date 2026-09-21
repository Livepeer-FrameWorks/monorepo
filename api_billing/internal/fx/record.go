package fx

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"frameworks/api_billing/internal/database/purserdb"

	"github.com/shopspring/decimal"
)

// Record is the FX evidence stored on a money row: the amount in the currency
// the payer was charged in, the EUR amount that reached the ledger, and the
// rate, source, and reference date of the conversion.
type Record struct {
	OriginalMinor    int64
	OriginalCurrency string
	EURMinor         int64
	UnitsPerEUR      decimal.Decimal
	Source           string
	ReferenceDate    time.Time
}

// ErrUnavailable wraps every error that means no usable reference rate exists
// for a conversion, so callers can refuse the operation and retry later.
var ErrUnavailable = errors.New("fx: reference rate unavailable")

// QuoteToEUR converts amountMinor of currency to EUR at the rate for on.
func QuoteToEUR(ctx context.Context, db purserdb.DBTX, currency string, amountMinor int64, on time.Time) (Record, error) {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	rate, err := Lookup(ctx, db, currency, on)
	if err != nil {
		return Record{}, unavailable(err)
	}
	return RecordToEUR(amountMinor, rate)
}

// QuoteFromEUR converts eurMinor to currency at the rate for on.
func QuoteFromEUR(ctx context.Context, db purserdb.DBTX, currency string, eurMinor int64, on time.Time) (Record, error) {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	rate, err := Lookup(ctx, db, currency, on)
	if err != nil {
		return Record{}, unavailable(err)
	}
	return RecordFromEUR(eurMinor, rate)
}

// RecordToEUR builds the record for amountMinor of rate.Currency.
func RecordToEUR(amountMinor int64, rate Rate) (Record, error) {
	eur, err := ToEUR(amountMinor, rate)
	if err != nil {
		return Record{}, err
	}
	return recordFor(amountMinor, eur, rate), nil
}

// RecordFromEUR builds the record for eurMinor presented in rate.Currency.
func RecordFromEUR(eurMinor int64, rate Rate) (Record, error) {
	original, err := FromEUR(eurMinor, rate)
	if err != nil {
		return Record{}, err
	}
	return recordFor(original, eurMinor, rate), nil
}

func recordFor(originalMinor, eurMinor int64, rate Rate) Record {
	source := rate.Source
	if rate.Currency == EUR {
		source = SourceIdentity
	}
	if source == "" {
		source = SourceECB
	}
	return Record{
		OriginalMinor:    originalMinor,
		OriginalCurrency: rate.Currency,
		EURMinor:         eurMinor,
		UnitsPerEUR:      rate.UnitsPerEUR,
		Source:           source,
		ReferenceDate:    Date(rate.ReferenceDate),
	}
}

// Rate returns the rate the record was converted with.
func (r Record) Rate() Rate {
	return Rate{Currency: r.OriginalCurrency, ReferenceDate: r.ReferenceDate, UnitsPerEUR: r.UnitsPerEUR, Source: r.Source}
}

// Part returns the record for originalPart of the original amount, after
// earlier parts totalling priorOriginal and priorEUR were taken. A part that
// completes the original amount takes the EUR the earlier parts left, so parts
// that together make up the original amount sum to exactly the recorded EUR.
// Intermediate parts bring the cumulative allocation to its rounded share,
// without exceeding the remaining EUR. Rounding cannot make the final part negative.
func (r Record) Part(originalPart, priorOriginal, priorEUR int64) (Record, error) {
	if r.OriginalMinor <= 0 {
		return Record{}, fmt.Errorf("fx: cannot take a part of a record with original amount %d", r.OriginalMinor)
	}
	if originalPart < 0 || priorOriginal < 0 || priorOriginal > r.OriginalMinor || originalPart > r.OriginalMinor-priorOriginal || priorEUR < 0 || priorEUR > r.EURMinor {
		return Record{}, errors.New("fx: part or prior allocation is outside the original record")
	}
	part := r
	part.OriginalMinor = originalPart
	switch {
	case r.Source == SourceIdentity:
		part.EURMinor = originalPart
	case priorOriginal+originalPart == r.OriginalMinor:
		part.EURMinor = r.EURMinor - priorEUR
	default:
		value := new(big.Rat).SetFrac(big.NewInt(r.EURMinor), big.NewInt(r.OriginalMinor))
		value.Mul(value, new(big.Rat).SetInt64(priorOriginal+originalPart))
		part.EURMinor = max(0, min(r.EURMinor-priorEUR, roundHalfAwayFromZero(value)-priorEUR))
	}
	return part, nil
}

// Scale returns the record for a receipt that is numerator/denominator of the
// quoted amount, scaling both amounts at the quoted rate.
func (r Record) Scale(numerator, denominator *big.Int) (Record, error) {
	if denominator == nil || denominator.Sign() <= 0 || numerator == nil || numerator.Sign() <= 0 {
		return Record{}, errors.New("fx: scale needs positive amounts")
	}
	if numerator.Cmp(denominator) == 0 {
		return r, nil
	}
	ratio := new(big.Rat).SetFrac(numerator, denominator)
	scaled := r
	scaled.EURMinor = roundHalfAwayFromZero(new(big.Rat).Mul(new(big.Rat).SetInt64(r.EURMinor), ratio))
	scaled.OriginalMinor = roundHalfAwayFromZero(new(big.Rat).Mul(new(big.Rat).SetInt64(r.OriginalMinor), ratio))
	if r.Source == SourceIdentity {
		scaled.OriginalMinor = scaled.EURMinor
	}
	return scaled, nil
}

// ErrMissingRecord means a money row has no FX fields.
var ErrMissingRecord = errors.New("fx: money row has no FX fields")

// FromColumns parses FX fields read with empty-string and zero fallbacks for
// rows written before FX fields existed. An empty source means no record.
func FromColumns(originalMinor int64, originalCurrency string, eurMinor int64, unitsPerEUR, source string, referenceDate time.Time) (Record, error) {
	if strings.TrimSpace(source) == "" {
		return Record{}, ErrMissingRecord
	}
	units, err := decimal.NewFromString(strings.TrimSpace(unitsPerEUR))
	if err != nil {
		return Record{}, fmt.Errorf("fx: stored rate %q: %w", unitsPerEUR, err)
	}
	return Record{
		OriginalMinor:    originalMinor,
		OriginalCurrency: strings.ToUpper(strings.TrimSpace(originalCurrency)),
		EURMinor:         eurMinor,
		UnitsPerEUR:      units,
		Source:           source,
		ReferenceDate:    Date(referenceDate),
	}, nil
}

// UnitsText is the rate as bound to NUMERIC parameters.
func (r Record) UnitsText() string {
	return r.UnitsPerEUR.String()
}

func unavailable(err error) error {
	if errors.Is(err, ErrNoRate) || errors.Is(err, ErrStaleRate) || errors.Is(err, ErrUnsupportedCurrency) {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return err
}
