// Package fx holds Purser's euro foreign exchange reference rates: the ECB
// feeds that supply them, the purser.fx_rates store, and the conversions that
// money paths apply between EUR and the USD and GBP presentment currencies.
//
// A rate is the ECB quote for one reference date: units of the currency that
// one euro buys. Lookups return the latest rate on or before a date and refuse
// a reference date more than MaxReferenceAgeDays calendar days older than the
// date asked for; there is no fallback to an older or cached rate.
package fx

import (
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/shopspring/decimal"
)

// MaxReferenceAgeDays is the oldest reference date, in calendar days before
// the conversion date, that a lookup accepts. It spans a weekend plus the
// longest run of TARGET holidays (Good Friday to Easter Monday).
const MaxReferenceAgeDays = 5

const (
	EUR = "EUR"
	USD = "USD"
	GBP = "GBP"
)

// Rate sources recorded on rates and on the money rows converted with them.
const (
	SourceECB      = "ecb"
	SourceIdentity = "identity"
)

var (
	// ErrNoRate means no stored rate exists on or before the requested date.
	ErrNoRate = errors.New("fx: no reference rate on or before the requested date")
	// ErrStaleRate means the latest stored rate is older than MaxReferenceAgeDays.
	ErrStaleRate = errors.New("fx: reference rate is older than the maximum reference age")
	// ErrReferenceDateMismatch means a cross conversion was given rates from
	// different reference dates.
	ErrReferenceDateMismatch = errors.New("fx: cross conversion rates have different reference dates")
	// ErrUnsupportedCurrency means the currency has no reference rate.
	ErrUnsupportedCurrency = errors.New("fx: unsupported currency")
)

// Currencies are the non-EUR currencies whose ECB reference rates are stored.
var Currencies = []string{USD, GBP}

// Rate is one reference rate.
type Rate struct {
	Currency      string
	ReferenceDate time.Time
	UnitsPerEUR   decimal.Decimal
	Source        string
}

// Supported reports whether currency is EUR or has stored reference rates.
func Supported(currency string) bool {
	switch currency {
	case EUR, USD, GBP:
		return true
	default:
		return false
	}
}

// Identity is the EUR rate of 1 for a reference date. It lets EUR take part
// in Cross and in the same FX record as converted amounts.
func Identity(referenceDate time.Time) Rate {
	return Rate{Currency: EUR, ReferenceDate: Date(referenceDate), UnitsPerEUR: decimal.NewFromInt(1), Source: SourceIdentity}
}

// Date truncates t to its UTC calendar date.
func Date(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// AgeDays is the number of calendar days from a rate's reference date to on.
func AgeDays(referenceDate, on time.Time) int {
	return int(Date(on).Sub(Date(referenceDate)).Hours() / 24)
}

// ToEUR converts minor units of rate.Currency to euro cents.
func ToEUR(amountMinor int64, rate Rate) (int64, error) {
	units, err := rate.units()
	if err != nil {
		return 0, err
	}
	value := new(big.Rat).SetInt64(amountMinor)
	value.Quo(value, units)
	return roundHalfAwayFromZero(value), nil
}

// FromEUR converts euro cents to minor units of rate.Currency.
func FromEUR(eurMinor int64, rate Rate) (int64, error) {
	units, err := rate.units()
	if err != nil {
		return 0, err
	}
	value := new(big.Rat).SetInt64(eurMinor)
	value.Mul(value, units)
	return roundHalfAwayFromZero(value), nil
}

// Cross converts minor units of from.Currency to minor units of to.Currency
// through EUR in one step, so rounding happens once. Both rates must share a
// reference date.
func Cross(amountMinor int64, from, to Rate) (int64, error) {
	if !Date(from.ReferenceDate).Equal(Date(to.ReferenceDate)) {
		return 0, fmt.Errorf("%w: %s on %s, %s on %s", ErrReferenceDateMismatch,
			from.Currency, from.ReferenceDate.Format(time.DateOnly), to.Currency, to.ReferenceDate.Format(time.DateOnly))
	}
	fromUnits, err := from.units()
	if err != nil {
		return 0, err
	}
	toUnits, err := to.units()
	if err != nil {
		return 0, err
	}
	value := new(big.Rat).SetInt64(amountMinor)
	value.Mul(value, toUnits)
	value.Quo(value, fromUnits)
	return roundHalfAwayFromZero(value), nil
}

func (r Rate) units() (*big.Rat, error) {
	if !Supported(r.Currency) {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedCurrency, r.Currency)
	}
	if r.Currency == EUR && !r.UnitsPerEUR.Equal(decimal.NewFromInt(1)) {
		return nil, fmt.Errorf("fx: EUR rate must be 1, got %s", r.UnitsPerEUR)
	}
	if !r.UnitsPerEUR.IsPositive() {
		return nil, fmt.Errorf("fx: %s rate must be positive, got %s", r.Currency, r.UnitsPerEUR)
	}
	return r.UnitsPerEUR.Rat(), nil
}

// roundHalfAwayFromZero rounds an exact rational to the nearest integer, with
// halves rounded away from zero.
func roundHalfAwayFromZero(value *big.Rat) int64 {
	num := new(big.Int).Abs(value.Num())
	den := value.Denom()
	quotient, remainder := new(big.Int).QuoRem(num, den, new(big.Int))
	if new(big.Int).Mul(remainder, big.NewInt(2)).Cmp(den) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if value.Sign() < 0 {
		quotient.Neg(quotient)
	}
	return quotient.Int64()
}
