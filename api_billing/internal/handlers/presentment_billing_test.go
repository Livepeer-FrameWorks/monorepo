package handlers

import (
	"github.com/shopspring/decimal"
	"testing"
)

func TestFrozenReplacementCreditCapsAtSourceCurrencyTaxAmount(t *testing.T) {
	previous := previousBaseFee{taxReady: true, credit: baseFeeCredit{cents: 3025}, creditOriginalMinor: 3781, sourceGrossEUR: 6050, sourceGrossOriginal: 7563}
	credit, minor := frozenReplacementCredit(previous, 2420)
	if credit.cents > 2420 || minor <= 0 || minor >= previous.creditOriginalMinor || credit.forfeitedCents != previous.credit.cents-credit.cents {
		t.Fatalf("source credit cap invalid: %+v original%d", credit, minor)
	}
	want := decimal.NewFromInt(previous.sourceGrossEUR).Mul(decimal.NewFromInt(minor)).Div(decimal.NewFromInt(previous.sourceGrossOriginal)).Round(0).IntPart()
	if credit.cents != want {
		t.Fatalf("credit financial allocation%d differs from source tax credit%d", credit.cents, want)
	}
}
