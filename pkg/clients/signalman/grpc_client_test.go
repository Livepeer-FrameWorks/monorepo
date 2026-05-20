package signalman

import "testing"

func TestDefaultServerName(t *testing.T) {
	if DefaultServerName != "signalman.internal" {
		t.Fatalf("DefaultServerName = %q", DefaultServerName)
	}
}
