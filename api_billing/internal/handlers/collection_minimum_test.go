package handlers

import "testing"

func TestDecideInvoiceCollection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		opening          int64
		current          int64
		wantOutcome      string
		wantCollected    int64
		wantClosingCarry int64
	}{
		{name: "zero usage", wantOutcome: "none"},
		{name: "below floor", current: 1, wantOutcome: "deferred", wantClosingCarry: 1},
		{name: "accumulates below floor", opening: 300, current: 199, wantOutcome: "deferred", wantClosingCarry: 499},
		{name: "hits floor", opening: 300, current: 200, wantOutcome: "collected", wantCollected: 500},
		{name: "above floor", opening: 499, current: 250, wantOutcome: "collected", wantCollected: 749},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := decideInvoiceCollection(tt.opening, tt.current, 500)
			if got.Outcome != tt.wantOutcome || got.CollectedCents != tt.wantCollected || got.ClosingBalanceCents != tt.wantClosingCarry {
				t.Fatalf("decision = %+v", got)
			}
		})
	}
}

func TestResolveInvoiceCollectionProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		method  string
		stripe  bool
		mollie  bool
		want    string
		wantErr bool
	}{
		{name: "selected stripe", method: "stripe", stripe: true, mollie: true, want: "stripe"},
		{name: "selected mollie", method: "mollie", stripe: true, mollie: true, want: "mollie"},
		{name: "legacy stripe", method: "card", stripe: true, want: "stripe"},
		{name: "legacy mollie", method: "card", mollie: true, want: "mollie"},
		{name: "ambiguous legacy", method: "card", stripe: true, mollie: true, wantErr: true},
		{name: "selected id missing", method: "stripe", mollie: true, wantErr: true},
		{name: "no provider", method: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := resolveInvoiceCollectionProvider(tt.method, tt.stripe, tt.mollie)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("resolveInvoiceCollectionProvider() = (%q, %v), want (%q, err=%v)", got, err, tt.want, tt.wantErr)
			}
		})
	}
}
