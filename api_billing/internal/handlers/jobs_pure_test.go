package handlers

import (
	"context"
	"database/sql"
	"errors"
	"strings"
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
	mk := func(d time.Duration) models.UsageSummary {
		return models.UsageSummary{Sequence: 1, PeriodStart: start, PeriodEnd: start.Add(d)}
	}
	tests := []struct {
		name            string
		summary         models.UsageSummary
		wantGranularity string
		wantErr         bool
	}{
		{name: "five minutes", summary: mk(5 * time.Minute), wantGranularity: "minute_5"},
		{name: "one hour boundary", summary: mk(time.Hour), wantGranularity: "hourly"},
		{name: "one day boundary", summary: mk(24 * time.Hour), wantGranularity: "daily"},
		{name: "month boundary", summary: mk(28 * 24 * time.Hour), wantGranularity: "monthly"},
		{name: "missing period", summary: models.UsageSummary{}, wantErr: true},
		{name: "non-positive period", summary: mk(0), wantErr: true},
		{name: "inverted period", summary: models.UsageSummary{PeriodStart: start.Add(time.Hour), PeriodEnd: start}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, gran, err := parseUsageSummaryPeriod(tc.summary)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got granularity %q", gran)
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

func TestValidateUsageSummaryEnvelopeRequiresSequence(t *testing.T) {
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	summary := models.UsageSummary{
		ReportID:    strings.Repeat("a", 64),
		SourceID:    "region-eu-1",
		ReportKind:  "finalized",
		TenantID:    "11111111-1111-4111-8111-111111111111",
		ClusterID:   "cluster-eu-1",
		PeriodStart: start,
		PeriodEnd:   start.Add(5 * time.Minute),
		Complete:    true,
	}
	if err := validateUsageSummaryEnvelope(summary); err == nil || err.Error() != "missing_sequence" {
		t.Fatalf("expected missing_sequence, got %v", err)
	}
	summary.Sequence = uint64(summary.PeriodEnd.Unix())
	if err := validateUsageSummaryEnvelope(summary); err != nil {
		t.Fatalf("valid envelope rejected: %v", err)
	}
}

func TestValidateWindowCompletionRejectsUsage(t *testing.T) {
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	summary := models.UsageSummary{
		ReportID: strings.Repeat("b", 64), SourceID: "eu-1", ReportKind: "window_complete", Sequence: 1,
		TenantID: "11111111-1111-4111-8111-111111111111", ClusterID: "_source",
		PeriodStart: start, PeriodEnd: start.Add(5 * time.Minute), Complete: true,
		Meters: []models.MeterQuantity{{Meter: "api_requests", Unit: "request", Quantity: 1}},
	}
	if err := validateUsageSummaryEnvelope(summary); err == nil || err.Error() != "window_complete_contains_usage" {
		t.Fatalf("expected window_complete_contains_usage, got %v", err)
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

// nextProviderPaymentAttempt is the retry state machine: a fresh logical
// payment gets attempt 1; an ambiguous provider-call failure reuses that same
// attempt (and its provider idempotency key); all other states stop retries.
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
			name: "prior provider_call_failed reuses logical attempt",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("billing_payment_attempts").WithArgs(provider, invoiceID).
					WillReturnRows(sqlmock.NewRows(cols).AddRow(1, "provider_call_failed"))
			},
			want: 1,
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
			name: "legacy higher attempt is still reused",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("billing_payment_attempts").WithArgs(provider, invoiceID).
					WillReturnRows(sqlmock.NewRows(cols).AddRow(maxProviderPaymentAttempts, "provider_call_failed"))
			},
			want: maxProviderPaymentAttempts,
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
