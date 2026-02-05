package handlers

import (
	"context"
	"testing"
)

func TestGetBillingStatusNilFallback(t *testing.T) {
	originalTrigger := triggerProcessor
	originalQuartermaster := quartermasterClient
	defer func() {
		triggerProcessor = originalTrigger
		quartermasterClient = originalQuartermaster
	}()

	triggerProcessor = nil
	quartermasterClient = nil

	if status := getBillingStatus(context.Background(), "stream-1", "tenant-1"); status != nil {
		t.Fatalf("expected nil status when no clients available, got %#v", status)
	}
}
