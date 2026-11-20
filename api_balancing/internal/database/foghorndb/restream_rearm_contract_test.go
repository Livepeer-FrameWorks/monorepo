package foghorndb

import (
	"strings"
	"testing"
)

func TestRuntimeRestreamRearmHasStableRunResetAndBoundedBackoff(t *testing.T) {
	for _, fragment := range []string{
		"effect.updated_at <= NOW() - INTERVAL '5 minutes'",
		"effect.attempts < 12",
		"power(2, LEAST",
		"INTERVAL '5 minutes'",
	} {
		if !strings.Contains(rearmAdmissionPushTargetsAfterRuntimeEnd, fragment) {
			t.Fatalf("runtime rearm is missing %q", fragment)
		}
	}
	if strings.Contains(markAdmissionActivationDone, "attempts = 0") {
		t.Fatal("short successful activations erase the per-generation restart budget")
	}
}

func TestOfflineSettlementKeepsRetryBudgetAndBoundsAcknowledgementWait(t *testing.T) {
	if strings.Contains(settleOfflineEffect, "\n    attempts =") {
		t.Fatal("incomplete offline settlement erases the per-generation retry budget")
	}
	for _, fragment := range []string{
		"THEN e.ack_wait_attempts + 1 ELSE e.ack_wait_attempts END",
		"e.attempts >= 12 OR (p.awaiting_ack AND e.ack_wait_attempts + 1 >= 12)",
	} {
		if !strings.Contains(settleOfflineEffect, fragment) {
			t.Fatalf("offline settlement is missing %q", fragment)
		}
	}
}

func TestOfflineDeadLettersAreVisibleAndRetained(t *testing.T) {
	if !strings.Contains(failExhaustedOfflineEffects, "RETURNING id, tenant_id") {
		t.Fatal("exhausted offline effects are not returned for observability")
	}
	if !strings.Contains(purgeTerminalOfflineEffects, "state = 'failed' AND updated_at < NOW() - INTERVAL '30 days'") {
		t.Fatal("offline dead letters do not have the documented 30-day retention")
	}
}
