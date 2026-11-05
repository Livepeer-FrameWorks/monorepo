package main

import (
	"strconv"
	"testing"
	"time"
)

func TestPreparedCharacterLiteralRetainsCurrencyWidth(t *testing.T) {
	got := literal(paramBinding{Type: "character", Value: "EUR"})
	if got != "'EUR'::pg_catalog.bpchar" {
		t.Fatalf("prepared bpchar parameter must retain all currency characters, not cast to SQL CHAR(1): %s", got)
	}
}

func TestPurserPaymentBindingsUseOwnedSourcesAndIntegerMinorAmounts(t *testing.T) {
	for _, tc := range []struct {
		name         string
		names, types []string
	}{
		{"InsertFundedAdvanceInvoice", []string{"tenant_id", "gross_eur_cents", "net_eur_cents", "tax_point_at", "payment_reference", "gross_original_minor", "currency", "units_per_eur", "reference_date", "source_reference_type", "source_reference_id"}, []string{"text", "bigint", "bigint", "timestamp with time zone", "text", "bigint", "text", "text", "date", "text", "text"}},
		{"RecordNativeMollieInvoicePayment", []string{"tenant_id", "invoice_id", "gross_minor", "payment_id", "paid_at"}, []string{"text", "text", "bigint", "character varying", "timestamp with time zone"}},
		{"InsertReorgedCryptoInvoiceCreditNote", []string{"tenant_id", "invoice_id", "event_id", "amount_cents", "currency", "payment_id", "tx_hash", "supplier_snapshot"}, []string{"text", "text", "character varying", "bigint", "character", "text", "text", "text"}},
		{"UpsertPendingProviderBillingPayment", []string{"payment_id", "invoice_id", "amount", "currency", "tx_id", "original_amount_cents", "eur_amount_cents", "fx_units_per_eur", "fx_source", "fx_reference_date"}, []string{"text", "text", "text", "text", "character varying", "bigint", "bigint", "text", "text", "date"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &binder{s: &seeder{tables: map[string]*table{}}, queryID: "purser:" + tc.name, now: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}
			got := b.bind("SELECT 1", tc.names, tc.types)
			for i, p := range got {
				if p.Null || p.Value == "" {
					t.Fatalf("%s lacks an executable owned binding: %+v", tc.names[i], p)
				}
				if p.Type == "bigint" {
					if _, err := strconv.ParseInt(p.Value, 10, 64); err != nil {
						t.Fatalf("%s binds decimal major units to integer minor units: %q", tc.names[i], p.Value)
					}
					want := "1210"
					if tc.names[i] == "net_eur_cents" {
						want = "1000"
					}
					if p.Value != want {
						t.Fatalf("%s lost the EUR12.10 gross/EUR10 net purchase: %q", tc.names[i], p.Value)
					}
				}
				if tc.names[i] == "amount" && p.Value != "12.10" {
					t.Fatal("major amount does not match the integer minor amount", p)
				}
				if tc.names[i] == "currency" && p.Value != "EUR" {
					t.Fatal("credit/payment currency differs from original EUR supply", p)
				}
			}
		})
	}
}
