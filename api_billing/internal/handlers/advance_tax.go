package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"frameworks/api_billing/internal/fx"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

func QuoteFiatPrepaidAdvance(ctx context.Context, db *sql.DB, tenant string, net fx.Record) (AdvanceMoney, fx.Record, json.RawMessage, error) {
	var a invoiceTaxAssessment
	err := database.WithRetryablePostgresTx(ctx, db, nil, func(tx *sql.Tx) error {
		var err error
		a, err = assessInvoiceTaxTx(ctx, tx, tenant, net.EURMinor, time.Now().UTC())
		return err
	})
	if err != nil {
		return AdvanceMoney{}, fx.Record{}, nil, err
	}
	money, cash, err := quoteAdvanceMoney(net.OriginalMinor, a.Decision.RateBPS, net.Rate())
	if err != nil {
		return money, cash, nil, err
	}
	a.Net, a.VAT, a.Gross = money.NetEUR, money.VATEUR, money.GrossEUR
	a.Advance = &money
	a.DocumentKind = "full"
	retained, err := json.Marshal(a)
	return money, cash, retained, err
}

// AdvanceMoney separates the gross cash receipt from the net service credit.
// VAT is rounded in the payment currency and valued at the locked FX rate.
type AdvanceMoney struct {
	NetOriginal   int64  `json:"net_original_minor"`
	VATOriginal   int64  `json:"vat_original_minor"`
	GrossOriginal int64  `json:"gross_original_minor"`
	NetEUR        int64  `json:"net_eur_cents"`
	VATEUR        int64  `json:"vat_eur_cents"`
	GrossEUR      int64  `json:"gross_eur_cents"`
	Currency      string `json:"currency"`
	RateBPS       int    `json:"rate_bps"`
}

func quoteAdvanceMoney(netOriginal int64, rateBPS int, rate fx.Rate) (AdvanceMoney, fx.Record, error) {
	a := AdvanceMoney{NetOriginal: netOriginal, Currency: rate.Currency, RateBPS: rateBPS}
	if netOriginal <= 0 || rateBPS < 0 || rateBPS > 10000 {
		return a, fx.Record{}, fmt.Errorf("invalid advance price or VAT rate")
	}
	a.VATOriginal, a.GrossOriginal = exclusiveTax(netOriginal, rateBPS)
	cash, err := fx.RecordToEUR(a.GrossOriginal, rate)
	if err != nil {
		return a, fx.Record{}, err
	}
	vat, err := fx.RecordToEUR(a.VATOriginal, rate)
	if err != nil {
		return a, fx.Record{}, err
	}
	a.GrossEUR = cash.EURMinor
	a.VATEUR = vat.EURMinor
	a.NetEUR = a.GrossEUR - a.VATEUR
	if a.NetEUR <= 0 {
		return a, fx.Record{}, fmt.Errorf("advance is below EUR minor-unit precision")
	}
	return a, cash, nil
}
