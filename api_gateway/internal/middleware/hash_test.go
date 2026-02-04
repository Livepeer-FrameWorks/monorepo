package middleware

import "testing"

func resetHasherState() {
	hashSecretMu.Lock()
	hashSecret = nil
	useHMAC = false
	hashSecretMu.Unlock()
}

func TestHashIdentifierEmptyString(t *testing.T) {
	resetHasherState()
	t.Cleanup(resetHasherState)

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

	first := HashIdentifier("viewer-123")
	second := HashIdentifier("viewer-123")
	if first != second {
		t.Fatalf("expected deterministic hash, got %d and %d", first, second)
	}
}

func TestHashIdentifierDifferentInputs(t *testing.T) {
	resetHasherState()
	t.Cleanup(resetHasherState)

	one := HashIdentifier("viewer-123")
	two := HashIdentifier("viewer-124")
	if one == two {
		t.Fatalf("expected different hashes for different inputs, got %d", one)
	}
}

func TestHashIdentifierFNV64ModeNonZero(t *testing.T) {
	resetHasherState()
	t.Cleanup(resetHasherState)

	if got := HashIdentifier("viewer-123"); got == 0 {
		t.Fatalf("expected non-zero hash in FNV64 mode")
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

func TestHashIdentifierFNV64DiffersFromHMAC(t *testing.T) {
	resetHasherState()
	t.Cleanup(resetHasherState)

	fnvHash := HashIdentifier("viewer-123")
	InitHasher("secret-key")
	hmacHash := HashIdentifier("viewer-123")

	if fnvHash == 0 || hmacHash == 0 {
		t.Fatalf("expected non-zero hashes, got fnv=%d hmac=%d", fnvHash, hmacHash)
	}
	if fnvHash == hmacHash {
		t.Fatalf("expected different hashes between FNV64 and HMAC modes")
	}
}
