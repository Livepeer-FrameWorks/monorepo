package grpc

import (
	"strings"
	"testing"
)

func TestGenerateStreamKey_Format(t *testing.T) {
	key, err := generateStreamKey()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, "sk_") {
		t.Fatalf("expected sk_ prefix, got %q", key)
	}
	// 16 random bytes = 32 hex chars + "sk_" = 35
	if len(key) != 35 {
		t.Fatalf("expected length 35, got %d", len(key))
	}
}

func TestGenerateStreamKey_Unique(t *testing.T) {
	seen := make(map[string]struct{}, 100)
	for range 100 {
		key, err := generateStreamKey()
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := seen[key]; ok {
			t.Fatalf("duplicate key: %s", key)
		}
		seen[key] = struct{}{}
	}
}

func TestGenerateRandomString_Length(t *testing.T) {
	for _, length := range []int{0, 1, 16, 32, 64} {
		s, err := generateRandomString(length)
		if err != nil {
			t.Fatal(err)
		}
		if len(s) != length {
			t.Fatalf("length %d: got %d chars", length, len(s))
		}
	}
}

func TestGenerateRandomString_Charset(t *testing.T) {
	s, err := generateRandomString(1000)
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range s {
		if !strings.ContainsRune(alphaNumCharset, c) {
			t.Fatalf("char %d (%c) not in charset", i, c)
		}
	}
}

func TestGenerateRandomString_NegativeLength(t *testing.T) {
	s, err := generateRandomString(-1)
	if err != nil {
		t.Fatal(err)
	}
	if s != "" {
		t.Fatalf("expected empty string for negative length, got %q", s)
	}
}

func TestGenerateDVRHash_Format(t *testing.T) {
	hash, err := generateDVRHash()
	if err != nil {
		t.Fatal(err)
	}
	// 14 char timestamp + 16 hex chars = 30
	if len(hash) != 30 {
		t.Fatalf("expected length 30, got %d (%q)", len(hash), hash)
	}
	// First 14 chars should be digits (timestamp)
	for i := range 14 {
		if hash[i] < '0' || hash[i] > '9' {
			t.Fatalf("expected digit at position %d, got %c", i, hash[i])
		}
	}
}

func TestGenerateClipHash_Format(t *testing.T) {
	hash, err := generateClipHash()
	if err != nil {
		t.Fatal(err)
	}
	if len(hash) != 30 {
		t.Fatalf("expected length 30, got %d", len(hash))
	}
}

func TestGenerateVodHash_Format(t *testing.T) {
	hash, err := generateVodHash()
	if err != nil {
		t.Fatal(err)
	}
	if len(hash) != 30 {
		t.Fatalf("expected length 30, got %d", len(hash))
	}
}

func TestGenerateArtifactInternalName_Length(t *testing.T) {
	name, err := generateArtifactInternalName()
	if err != nil {
		t.Fatal(err)
	}
	if len(name) != artifactInternalNameLength {
		t.Fatalf("expected %d, got %d", artifactInternalNameLength, len(name))
	}
}

func TestGenerateArtifactPlaybackID_Length(t *testing.T) {
	id, err := generateArtifactPlaybackID()
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != artifactPlaybackIDLength {
		t.Fatalf("expected %d, got %d", artifactPlaybackIDLength, len(id))
	}
}

func TestGenerateSecureToken_Length(t *testing.T) {
	for _, n := range []int{8, 16, 32} {
		tok, err := generateSecureToken(n)
		if err != nil {
			t.Fatal(err)
		}
		// n bytes = 2*n hex chars
		if len(tok) != 2*n {
			t.Fatalf("n=%d: expected %d hex chars, got %d", n, 2*n, len(tok))
		}
	}
}

func TestHashToken_Deterministic(t *testing.T) {
	h1 := hashToken("test-token")
	h2 := hashToken("test-token")
	if h1 != h2 {
		t.Fatal("same input should produce same hash")
	}
	// SHA-256 = 64 hex chars
	if len(h1) != 64 {
		t.Fatalf("expected 64 hex chars, got %d", len(h1))
	}
}

func TestHashToken_DifferentInputs(t *testing.T) {
	h1 := hashToken("token-a")
	h2 := hashToken("token-b")
	if h1 == h2 {
		t.Fatal("different inputs should produce different hashes")
	}
}

func TestHashTokenWithSecret_HMAC(t *testing.T) {
	s := &CommodoreServer{passwordResetSecret: []byte("my-secret")}
	h := s.hashTokenWithSecret("test-token")
	if len(h) != 64 {
		t.Fatalf("expected 64 hex chars, got %d", len(h))
	}
	// HMAC result should differ from plain SHA-256
	plain := hashToken("test-token")
	if h == plain {
		t.Fatal("HMAC should differ from plain SHA-256")
	}
}

func TestHashTokenWithSecret_FallsBackToPlain(t *testing.T) {
	s := &CommodoreServer{passwordResetSecret: nil}
	h := s.hashTokenWithSecret("test-token")
	plain := hashToken("test-token")
	if h != plain {
		t.Fatal("should fall back to plain SHA-256 when no secret")
	}
}

func TestGetDefaultPermissions(t *testing.T) {
	tests := []struct {
		role string
		want int
	}{
		{"owner", 3},
		{"admin", 3},
		{"member", 2},
		{"viewer", 1},
		{"", 1},
	}
	for _, tc := range tests {
		perms := getDefaultPermissions(tc.role)
		if len(perms) != tc.want {
			t.Errorf("role %q: expected %d permissions, got %d (%v)", tc.role, tc.want, len(perms), perms)
		}
	}
}

func TestGetDefaultPermissions_Content(t *testing.T) {
	ownerPerms := getDefaultPermissions("owner")
	if ownerPerms[0] != "read" || ownerPerms[1] != "write" || ownerPerms[2] != "admin" {
		t.Fatalf("unexpected owner permissions: %v", ownerPerms)
	}
	memberPerms := getDefaultPermissions("member")
	if memberPerms[0] != "read" || memberPerms[1] != "write" {
		t.Fatalf("unexpected member permissions: %v", memberPerms)
	}
	viewerPerms := getDefaultPermissions("viewer")
	if viewerPerms[0] != "read" {
		t.Fatalf("unexpected viewer permissions: %v", viewerPerms)
	}
}
