package stripe

import (
	"fmt"
	"strings"
	"testing"
)

func TestTaxRecoveryMetadataCapacityPreservesHoldAndExistingWork(t *testing.T) {
	c, b, p := terminalReviewFixture(t)
	delete(b.currentSub.Metadata, legacyTaxRecoveryPending)
	b.currentSub.Metadata["frameworks_tax_review"] = "true"
	b.currentSub.Metadata[taxRecoveryWorkPrefix+strings.Repeat("a", 32)] = "pending"
	for len(b.currentSub.Metadata) < 50 {
		b.currentSub.Metadata[fmt.Sprintf("operator_%d", len(b.currentSub.Metadata))] = "preserved"
	}
	// Removing the active flag frees one key for the new immutable token.
	if err := c.SyncSubscriptionTax(t.Context(), "tenant", "customer", "sub", p); err != nil {
		t.Fatal(err)
	}
	if len(taxRecoveryWorkKeys(b.currentSub.Metadata)) != 2 || len(b.currentSub.Metadata) != 50 {
		t.Fatalf("resume pruned old work or violated metadata capacity: %v", b.currentSub.Metadata)
	}
	// Discovery cannot silently prune a full metadata map to retain new work.
	changes := map[string]string{}
	if _, err := newTaxRecoveryWork(b.currentSub.Metadata, changes); err == nil || len(changes) != 0 {
		t.Fatal("full metadata silently discarded recovery ownership")
	}
	// Legacy migration needing an extra profile key must fail before any write.
	c, b, p = terminalReviewFixture(t)
	delete(b.currentSub.Metadata, "frameworks_tax_profile")
	for len(b.currentSub.Metadata) < 50 {
		b.currentSub.Metadata[fmt.Sprintf("operator_%d", len(b.currentSub.Metadata))] = "preserved"
	}
	before := b.calls
	if err := c.SyncSubscriptionTax(t.Context(), "tenant", "customer", "sub", p); err == nil || b.updatedSub != nil || b.calls-before != 1 {
		t.Fatal("capacity failure mutated provider before recovery ownership could be retained")
	}
}

func TestLegacyRecoveryMarkerMigratesOnceAndRetainsDistinctWork(t *testing.T) {
	c, b, p := terminalReviewFixture(t)
	first, err := c.ReconcileSubscriptionTax(t.Context(), "tenant", "customer", "sub", p)
	if err != nil || len(first.WorkKeys) != 1 || b.currentSub.Metadata[legacyTaxRecoveryPending] != "" {
		t.Fatalf("legacy marker did not migrate: %+v %v", first, err)
	}
	again, err := c.ReconcileSubscriptionTax(t.Context(), "tenant", "customer", "sub", p)
	if err != nil || len(again.WorkKeys) != 1 || again.WorkKeys[0] != first.WorkKeys[0] {
		t.Fatal("stable worker minted new work on every pass")
	}
	if err = c.AckTaxRecoveryReport(t.Context(), again); err != nil {
		t.Fatal(err)
	}
	before := b.invoiceListCalls
	clean, err := c.ReconcileSubscriptionTax(t.Context(), "tenant", "customer", "sub", p)
	if err != nil || clean.RecoveryPending || b.invoiceListCalls != before {
		t.Fatal("completed migration kept scanning invoice history")
	}
}
