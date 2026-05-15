package signalman

import "testing"

func TestSignalmanTLSServerNameDefaultsWithCA(t *testing.T) {
	if got := signalmanTLSServerName("/etc/frameworks/pki/ca.crt", "", false); got != "signalman.internal" {
		t.Fatalf("server name = %q", got)
	}
}

func TestSignalmanTLSServerNameHonorsExplicitValue(t *testing.T) {
	if got := signalmanTLSServerName("/etc/frameworks/pki/ca.crt", "regional-eu-1.internal", false); got != "regional-eu-1.internal" {
		t.Fatalf("server name = %q", got)
	}
}

func TestSignalmanTLSServerNameLeavesDevInsecureUnset(t *testing.T) {
	if got := signalmanTLSServerName("", "", true); got != "" {
		t.Fatalf("server name = %q", got)
	}
}
