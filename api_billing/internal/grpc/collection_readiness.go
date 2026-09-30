package grpc

import (
	"database/sql"

	"github.com/shopspring/decimal"
)

// collectionProviderOperator is the collection provider of a tenant whose
// operator grant stands in for Stripe or Mollie collection.
const collectionProviderOperator = "operator"

// postpaidCollectionFacts is what decides whether a postpaid tenant's charges
// can be collected.
type postpaidCollectionFacts struct {
	PaymentMethod         sql.NullString
	StripeSubscriptionID  sql.NullString
	MollieSubscriptionID  sql.NullString
	StripeCustomerID      sql.NullString
	HasValidMollieMandate bool
	// GrantCollection, GrantWaiveUsage and EffectiveBasePrice come from the
	// operator grant in force, if any; EffectiveBasePrice is the tier's base
	// fee when no grant overrides it.
	GrantCollection    sql.NullString
	GrantWaiveUsage    bool
	EffectiveBasePrice string
}

// postpaidCollectionReadiness reports whether a postpaid tenant's charges can
// be collected, and by whom. A Stripe or Mollie setup that can charge the
// tenant off-session comes first: a subscription, or the saved card or valid
// mandate that advance-billed tenants run on. Without one, an operator grant
// stands in when the operator collects invoices by hand, or when the tenant
// owes nothing (no base fee and usage waived).
func postpaidCollectionReadiness(f postpaidCollectionFacts) (bool, string) {
	switch f.PaymentMethod.String {
	case "stripe":
		if nonEmpty(f.StripeSubscriptionID) || nonEmpty(f.StripeCustomerID) {
			return true, "stripe"
		}
	case "mollie":
		if nonEmpty(f.MollieSubscriptionID) || f.HasValidMollieMandate {
			return true, "mollie"
		}
	}
	if f.GrantCollection.String == "invoice" {
		return true, collectionProviderOperator
	}
	if f.GrantCollection.Valid && f.GrantWaiveUsage {
		if base, err := decimal.NewFromString(f.EffectiveBasePrice); err == nil && base.IsZero() {
			return true, collectionProviderOperator
		}
	}
	return false, ""
}

func nonEmpty(s sql.NullString) bool { return s.Valid && s.String != "" }
