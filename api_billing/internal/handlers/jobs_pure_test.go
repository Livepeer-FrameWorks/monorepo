package handlers

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/shopspring/decimal"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"
)

// stripeOverageMinorUnitExponent / overageAmountParts encode a hard column
// invariant: Purser's invoice/payment amount columns hold at most 2 minor
// units, so a 3-minor-unit currency MUST be rejected rather than silently
// truncated. The rounding must also follow the currency's own exponent.
func TestOverageAmountParts(t *testing.T) {
	tests := []struct {
		name      string
		amount    string
		currency  string
		wantStr   string
		wantCents int64
		wantErr   bool
	}{
		{name: "EUR two minor units", amount: "2.5", currency: "EUR", wantStr: "2.50", wantCents: 250},
		{name: "EUR rounds half away from zero", amount: "1.005", currency: "EUR", wantStr: "1.01", wantCents: 101},
		{name: "USD lowercase currency", amount: "10", currency: "usd", wantStr: "10.00", wantCents: 1000},
		{name: "JPY zero minor units truncates fraction", amount: "1234.7", currency: "JPY", wantStr: "1235", wantCents: 1235},
		{name: "BHD three minor units rejected", amount: "1.234", currency: "BHD", wantErr: true},
		{name: "KWD three minor units rejected", amount: "5.5", currency: "KWD", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			amt := decimal.RequireFromString(tc.amount)
			_, gotStr, gotCents, err := overageAmountParts(amt, tc.currency)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %s %s, got none (str=%q cents=%d)", tc.amount, tc.currency, gotStr, gotCents)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotStr != tc.wantStr {
				t.Errorf("amountStr = %q, want %q", gotStr, tc.wantStr)
			}
			if gotCents != tc.wantCents {
				t.Errorf("amountCents = %d, want %d", gotCents, tc.wantCents)
			}
		})
	}
}

func TestParseUsageSummaryPeriod(t *testing.T) {
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	mk := func(d time.Duration) string {
		return start.Format(time.RFC3339) + "/" + start.Add(d).Format(time.RFC3339)
	}
	tests := []struct {
		name            string
		period          string
		wantGranularity string
		wantErr         bool
	}{
		{name: "five minutes", period: mk(5 * time.Minute), wantGranularity: "minute_5"},
		{name: "one hour boundary", period: mk(time.Hour), wantGranularity: "hourly"},
		{name: "one day boundary", period: mk(24 * time.Hour), wantGranularity: "daily"},
		{name: "month boundary", period: mk(28 * 24 * time.Hour), wantGranularity: "monthly"},
		{name: "missing separator", period: "2026-04-01T00:00:00Z", wantErr: true},
		{name: "unparseable start", period: "notatime/2026-04-01T00:05:00Z", wantErr: true},
		{name: "non-positive period", period: mk(0), wantErr: true},
		{name: "inverted period", period: start.Add(time.Hour).Format(time.RFC3339) + "/" + start.Format(time.RFC3339), wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, gran, err := parseUsageSummaryPeriod(models.UsageSummary{Period: tc.period})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got granularity %q", tc.period, gran)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gran != tc.wantGranularity {
				t.Errorf("granularity = %q, want %q", gran, tc.wantGranularity)
			}
		})
	}
}

// loadSubscriptionPeriod has a 3-way precedence: mollie_next_payment_date wins
// (derives a [end-1mo, end] window truncated to UTC midnight), else a stored
// billing_period only when end strictly follows start, else a calendar-month
// fallback. The inverted-stored-period case is the subtle invariant: a corrupt
// stored window must NOT be returned verbatim.
func TestLoadSubscriptionPeriod(t *testing.T) {
	tenantID := "00000000-0000-0000-0000-000000000001"
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	calStart := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	calEnd := calStart.AddDate(0, 1, 0)

	cols := []string{"billing_period_start", "billing_period_end", "mollie_next_payment_date"}

	t.Run("mollie next payment date wins and truncates to UTC midnight", func(t *testing.T) {
		mockDB, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer mockDB.Close()
		mollieNext := time.Date(2026, 5, 15, 10, 30, 0, 0, time.UTC)
		mock.ExpectQuery("SELECT billing_period_start").
			WithArgs(tenantID).
			WillReturnRows(sqlmock.NewRows(cols).AddRow(nil, nil, mollieNext))

		gotStart, gotEnd, err := loadSubscriptionPeriod(context.Background(), mockDB, tenantID, now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		wantEnd := time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC)
		wantStart := wantEnd.AddDate(0, -1, 0)
		if !gotStart.Equal(wantStart) || !gotEnd.Equal(wantEnd) {
			t.Fatalf("got [%s, %s], want [%s, %s]", gotStart, gotEnd, wantStart, wantEnd)
		}
	})

	t.Run("valid stored period returned verbatim", func(t *testing.T) {
		mockDB, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer mockDB.Close()
		storedStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
		storedEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
		mock.ExpectQuery("SELECT billing_period_start").
			WithArgs(tenantID).
			WillReturnRows(sqlmock.NewRows(cols).AddRow(storedStart, storedEnd, nil))

		gotStart, gotEnd, err := loadSubscriptionPeriod(context.Background(), mockDB, tenantID, now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !gotStart.Equal(storedStart) || !gotEnd.Equal(storedEnd) {
			t.Fatalf("got [%s, %s], want stored [%s, %s]", gotStart, gotEnd, storedStart, storedEnd)
		}
	})

	t.Run("inverted stored period falls through to calendar month", func(t *testing.T) {
		mockDB, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer mockDB.Close()
		// end before start: corrupt window must not be returned.
		storedStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
		storedEnd := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
		mock.ExpectQuery("SELECT billing_period_start").
			WithArgs(tenantID).
			WillReturnRows(sqlmock.NewRows(cols).AddRow(storedStart, storedEnd, nil))

		gotStart, gotEnd, err := loadSubscriptionPeriod(context.Background(), mockDB, tenantID, now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !gotStart.Equal(calStart) || !gotEnd.Equal(calEnd) {
			t.Fatalf("got [%s, %s], want calendar [%s, %s]", gotStart, gotEnd, calStart, calEnd)
		}
	})

	t.Run("no active subscription falls back to calendar month without error", func(t *testing.T) {
		mockDB, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer mockDB.Close()
		mock.ExpectQuery("SELECT billing_period_start").
			WithArgs(tenantID).
			WillReturnError(sql.ErrNoRows)

		gotStart, gotEnd, err := loadSubscriptionPeriod(context.Background(), mockDB, tenantID, now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !gotStart.Equal(calStart) || !gotEnd.Equal(calEnd) {
			t.Fatalf("got [%s, %s], want calendar [%s, %s]", gotStart, gotEnd, calStart, calEnd)
		}
	})

	t.Run("non-ErrNoRows query error is propagated", func(t *testing.T) {
		mockDB, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer mockDB.Close()
		mock.ExpectQuery("SELECT billing_period_start").
			WithArgs(tenantID).
			WillReturnError(errors.New("connection reset"))

		if _, _, err := loadSubscriptionPeriod(context.Background(), mockDB, tenantID, now); err == nil {
			t.Fatal("expected error to propagate, got nil")
		}
	})
}

// nextProviderPaymentAttempt is the retry state machine: a fresh invoice gets
// attempt 1; a previously failed provider call increments up to the cap; any
// non-failure terminal status (or hitting the cap) stops further attempts.
func TestNextProviderPaymentAttempt(t *testing.T) {
	const provider = "stripe"
	const invoiceID = "inv-1"
	cols := []string{"attempt_number", "status"}

	cases := []struct {
		name    string
		setup   func(mock sqlmock.Sqlmock)
		want    int
		wantErr bool
	}{
		{
			name: "no prior attempt starts at one",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("billing_payment_attempts").WithArgs(provider, invoiceID).WillReturnError(sql.ErrNoRows)
			},
			want: 1,
		},
		{
			name: "prior provider_call_failed increments",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("billing_payment_attempts").WithArgs(provider, invoiceID).
					WillReturnRows(sqlmock.NewRows(cols).AddRow(1, "provider_call_failed"))
			},
			want: 2,
		},
		{
			name: "non-failure status stops retries",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("billing_payment_attempts").WithArgs(provider, invoiceID).
					WillReturnRows(sqlmock.NewRows(cols).AddRow(1, "confirmed"))
			},
			want: 0,
		},
		{
			name: "at attempt cap stops retries",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("billing_payment_attempts").WithArgs(provider, invoiceID).
					WillReturnRows(sqlmock.NewRows(cols).AddRow(maxProviderPaymentAttempts, "provider_call_failed"))
			},
			want: 0,
		},
		{
			name: "query error propagates",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("billing_payment_attempts").WithArgs(provider, invoiceID).
					WillReturnError(errors.New("boom"))
			},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mockDB, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer mockDB.Close()
			tc.setup(mock)
			jm := &JobManager{db: mockDB, logger: logging.NewLogger(), billing: &Service{}}
			got, err := jm.nextProviderPaymentAttempt(context.Background(), provider, invoiceID)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got attempt %d", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("nextProviderPaymentAttempt = %d, want %d", got, tc.want)
			}
		})
	}
}
