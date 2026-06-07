package handlers

import "testing"

// These two predicates are the money-safety gate for Stripe Checkout: value
// (tier activation, cluster access) is granted ONLY for the exact statuses
// below. Asynchronous methods (SEPA, iDEAL, Bancontact) report "unpaid" at
// checkout.session.completed and settle later, so "unpaid" must never
// provision. Any unrecognized status fails closed. The tests freeze that
// contract so a well-meaning "also treat X as paid" edit fails loudly.

func TestStripeCheckoutPaid(t *testing.T) {
	tests := []struct {
		status string
		want   bool
	}{
		{"paid", true},
		{"unpaid", false},
		{"no_payment_required", false}, // one-time checkout: funds must actually collect
		{"", false},
		{"processing", false},
	}
	for _, tc := range tests {
		if got := stripeCheckoutPaid(tc.status); got != tc.want {
			t.Errorf("stripeCheckoutPaid(%q) = %v, want %v", tc.status, got, tc.want)
		}
	}
}

func TestStripeSubscriptionProvisionable(t *testing.T) {
	tests := []struct {
		status string
		want   bool
	}{
		{"paid", true},
		{"no_payment_required", true}, // trials / fully-discounted subscriptions
		{"unpaid", false},             // async first payment activates later
		{"", false},
		{"processing", false},
	}
	for _, tc := range tests {
		if got := stripeSubscriptionProvisionable(tc.status); got != tc.want {
			t.Errorf("stripeSubscriptionProvisionable(%q) = %v, want %v", tc.status, got, tc.want)
		}
	}
}
