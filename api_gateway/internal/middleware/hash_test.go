package middleware

import "testing"

func resetHasherState() {
	hashSecretMu.Lock()
	hashSecret = nil
	hashSecretMu.Unlock()
}

func TestHashIdentifierEmptyString(t *testing.T) {
	resetHasherState()
	t.Cleanup(resetHasherState)

	InitHasher("test-secret")
	if got := hashIdentifier(""); got != 0 {
		t.Fatalf("hashIdentifier(\"\") = %d, want 0", got)
	}
	if got := HashIdentifier(""); got != 0 {
		t.Fatalf("HashIdentifier(\"\") = %d, want 0", got)
	}
}

func TestHashIdentifierDeterministic(t *testing.T) {
	resetHasherState()
	t.Cleanup(resetHasherState)

	InitHasher("test-secret")
	first := HashIdentifier("viewer-123")
	second := HashIdentifier("viewer-123")
	if first != second {
		t.Fatalf("expected deterministic hash, got %d and %d", first, second)
	}
}

func TestHashIdentifierDifferentInputs(t *testing.T) {
	resetHasherState()
	t.Cleanup(resetHasherState)

	InitHasher("test-secret")
	one := HashIdentifier("viewer-123")
	two := HashIdentifier("viewer-124")
	if one == two {
		t.Fatalf("expected different hashes for different inputs, got %d", one)
	}
}

func TestHashIdentifierEphemeralSecretNonZero(t *testing.T) {
	resetHasherState()
	t.Cleanup(resetHasherState)

	InitHasher("")
	if got := HashIdentifier("viewer-123"); got == 0 {
		t.Fatalf("expected non-zero hash with ephemeral secret")
	}
}

func TestHashIdentifierHMACModeNonZero(t *testing.T) {
	resetHasherState()
	t.Cleanup(resetHasherState)

	InitHasher("secret-key")
	if got := HashIdentifier("viewer-123"); got == 0 {
		t.Fatalf("expected non-zero hash in HMAC mode")
	}
}

func TestHashIdentifierEphemeralDiffersFromExplicit(t *testing.T) {
	resetHasherState()
	t.Cleanup(resetHasherState)

	InitHasher("")
	ephemeralHash := HashIdentifier("viewer-123")

	InitHasher("secret-key")
	explicitHash := HashIdentifier("viewer-123")

	if ephemeralHash == 0 || explicitHash == 0 {
		t.Fatalf("expected non-zero hashes, got ephemeral=%d explicit=%d", ephemeralHash, explicitHash)
	}
	if ephemeralHash == explicitHash {
		t.Fatalf("expected different hashes between ephemeral and explicit secrets")
	}
}
