package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"frameworks/api_billing/internal/fx"
	billingtax "frameworks/api_billing/internal/tax"
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
// The payment-currency figures are VAT on top of the net in that currency; the
// EUR figures, the ones the tax document states VAT in, are the net valued at
// the locked FX rate with VAT on top of it, rounded once.
type AdvanceMoney = billingtax.AdvanceMoney

func quoteAdvanceMoney(netOriginal int64, rateBPS int, rate fx.Rate) (AdvanceMoney, fx.Record, error) {
	if netOriginal <= 0 || rateBPS < 0 || rateBPS > 10000 {
		return AdvanceMoney{NetOriginal: netOriginal, Currency: rate.Currency, RateBPS: rateBPS}, fx.Record{}, fmt.Errorf("invalid advance price or VAT rate")
	}
	vatOriginal, grossOriginal := exclusiveTax(netOriginal, rateBPS)
	return advanceMoney(netOriginal, vatOriginal, grossOriginal, rateBPS, rate)
}

// advanceMoney values a receipt of grossOriginal, netOriginal of it net, in EUR at rate: the net
// at the rate, VAT on top of that EUR net (exclusiveTax), and the receipt recorded at their sum.
// Valuing the gross and the VAT apart would round each and could state a VAT that is not the
// rate on the net. The recorded EUR gross differs from the gross valued alone by at most a cent.
func advanceMoney(netOriginal, vatOriginal, grossOriginal int64, rateBPS int, rate fx.Rate) (AdvanceMoney, fx.Record, error) {
	a := AdvanceMoney{NetOriginal: netOriginal, VATOriginal: vatOriginal, GrossOriginal: grossOriginal, Currency: rate.Currency, RateBPS: rateBPS}
	net, err := fx.RecordToEUR(netOriginal, rate)
	if err != nil {
		return a, fx.Record{}, err
	}
	a.NetEUR = net.EURMinor
	if a.NetEUR <= 0 {
		return a, fx.Record{}, fmt.Errorf("advance is below EUR minor-unit precision")
	}
	a.VATEUR, a.GrossEUR = exclusiveTax(a.NetEUR, rateBPS)
	cash, err := fx.RecordToEUR(grossOriginal, rate)
	if err != nil {
		return a, fx.Record{}, err
	}
	cash.EURMinor = a.GrossEUR
	return a, cash, nil
}
