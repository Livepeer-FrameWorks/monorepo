package handlers

import "testing"

func TestMapStripeSubscriptionStatus(t *testing.T) {
	// Intent: pin the Stripe-status -> purser-status state machine. The
	// non-obvious rules are: an active/trialing sub flagged cancel-at-period-end
	// is "pending_cancellation" (not active), terminal Stripe states collapse to
	// "cancelled", incomplete/paused are "pending", and any unrecognized status
	// passes through unchanged rather than being coerced.
	tests := []struct {
		name              string
		status            string
		cancelAtPeriodEnd bool
		want              string
	}{
		{"active stays active", "active", false, "active"},
		{"active cancelling is pending_cancellation", "active", true, "pending_cancellation"},
		{"trialing stays active", "trialing", false, "active"},
		{"trialing cancelling is pending_cancellation", "trialing", true, "pending_cancellation"},
		{"past_due passes through", "past_due", false, "past_due"},
		{"canceled collapses to cancelled", "canceled", false, "cancelled"},
		{"unpaid collapses to cancelled", "unpaid", false, "cancelled"},
		{"incomplete_expired collapses to cancelled", "incomplete_expired", false, "cancelled"},
		{"incomplete is pending", "incomplete", false, "pending"},
		{"paused is pending", "paused", false, "pending"},
		{"unknown passes through", "some_future_status", false, "some_future_status"},
		// cancelAtPeriodEnd only matters for active/trialing.
		{"cancel flag ignored for terminal status", "canceled", true, "cancelled"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MapStripeSubscriptionStatus(tt.status, tt.cancelAtPeriodEnd); got != tt.want {
				t.Fatalf("MapStripeSubscriptionStatus(%q, %v) = %q, want %q", tt.status, tt.cancelAtPeriodEnd, got, tt.want)
			}
		})
	}
}
