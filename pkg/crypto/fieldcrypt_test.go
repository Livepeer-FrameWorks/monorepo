package crypto

import (
	"strings"
	"testing"
)

func TestDeriveFieldEncryptor(t *testing.T) {
	fe, err := DeriveFieldEncryptor([]byte("test-jwt-secret-that-is-long-xxx"), "push-target-uri")
	if err != nil {
		t.Fatalf("DeriveFieldEncryptor: %v", err)
	}
	if fe == nil {
		t.Fatal("expected non-nil encryptor")
	}
}

func TestRoundTrip(t *testing.T) {
	fe, err := DeriveFieldEncryptor([]byte("test-jwt-secret-that-is-long-xxx"), "push-target-uri")
	if err != nil {
		t.Fatalf("DeriveFieldEncryptor: %v", err)
	}

	original := "rtmp://live.twitch.tv/app/live_abc123xyz"
	encrypted, err := fe.Encrypt(original)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	if encrypted == original {
		t.Fatal("encrypted should differ from plaintext")
	}
	if !IsEncrypted(encrypted) {
		t.Fatalf("expected enc:v1: prefix, got %q", encrypted[:20])
	}

	decrypted, err := fe.Decrypt(encrypted)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if decrypted != original {
		t.Fatalf("round-trip failed: got %q, want %q", decrypted, original)
	}
}

func TestPlaintextPassthrough(t *testing.T) {
	fe, err := DeriveFieldEncryptor([]byte("test-jwt-secret-that-is-long-xxx"), "push-target-uri")
	if err != nil {
		t.Fatalf("DeriveFieldEncryptor: %v", err)
	}

	plaintext := "rtmp://live.twitch.tv/app/live_abc123xyz"
	result, err := fe.Decrypt(plaintext)
	if err != nil {
		t.Fatalf("Decrypt plaintext: %v", err)
	}
	if result != plaintext {
		t.Fatalf("plaintext passthrough failed: got %q", result)
	}
}

func TestDifferentPurposesProduceDifferentKeys(t *testing.T) {
	secret := []byte("test-jwt-secret-that-is-long-xxx")
	fe1, _ := DeriveFieldEncryptor(secret, "purpose-a")
	fe2, _ := DeriveFieldEncryptor(secret, "purpose-b")

	original := "rtmp://test"
	enc1, _ := fe1.Encrypt(original)
	_, err := fe2.Decrypt(enc1)
	if err == nil {
		t.Fatal("expected decryption to fail with different purpose")
	}
}

func TestEncryptProducesUniqueOutput(t *testing.T) {
	fe, _ := DeriveFieldEncryptor([]byte("test-jwt-secret-that-is-long-xxx"), "test")

	enc1, _ := fe.Encrypt("same-input")
	enc2, _ := fe.Encrypt("same-input")
	if enc1 == enc2 {
		t.Fatal("two encryptions of same plaintext should produce different ciphertext (random nonce)")
	}
}

func TestFieldKeyringRotationAndLegacyRead(t *testing.T) {
	oldSecret := []byte("old-field-secret-at-least-16")
	newSecret := []byte("new-field-secret-at-least-16")
	legacy, err := DeriveFieldEncryptor(oldSecret, "push-target-uri")
	if err != nil {
		t.Fatal(err)
	}
	legacyCiphertext, err := legacy.Encrypt("rtmp://legacy.example/app/key")
	if err != nil {
		t.Fatal(err)
	}

	ring, err := NewFieldKeyring("current", newSecret, map[string][]byte{"previous": oldSecret}, [][]byte{oldSecret}, "push-target-uri")
	if err != nil {
		t.Fatal(err)
	}
	if got, decryptErr := ring.Decrypt(legacyCiphertext); decryptErr != nil || got != "rtmp://legacy.example/app/key" {
		t.Fatalf("legacy decrypt = %q, %v", got, decryptErr)
	}
	stored, err := ring.Encrypt("rtmp://new.example/app/key")
	if err != nil {
		t.Fatal(err)
	}
	if CiphertextFormat(stored) != FieldCiphertextV3 || !strings.HasPrefix(stored, "enc:v3:current:") {
		t.Fatalf("unexpected keyring envelope %q", stored)
	}
	rotated, err := NewFieldKeyring("next", []byte("next-field-secret-at-least-16"), map[string][]byte{"current": newSecret}, nil, "push-target-uri")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := rotated.Decrypt(stored); err != nil || got != "rtmp://new.example/app/key" {
		t.Fatalf("previous-key decrypt = %q, %v", got, err)
	}
}

func TestFieldKeyringRejectsUnknownAndTamperedKeyIDs(t *testing.T) {
	ring, err := NewFieldKeyring("current", []byte("new-field-secret-at-least-16"), nil, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := ring.Encrypt("secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ring.Decrypt(strings.Replace(stored, "current", "unknown", 1)); err == nil {
		t.Fatal("unknown key ID was accepted")
	}
	if _, err := ring.Decrypt(strings.Replace(stored, "current", "bad/key", 1)); err == nil {
		t.Fatal("invalid key ID was accepted")
	}
}

func TestParseLegacyFieldSecretsAllowsHistoricalShortJWT(t *testing.T) {
	secrets, err := ParseLegacyFieldSecrets(`["short","historical-jwt-secret"]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(secrets) != 2 || string(secrets[0]) != "short" || string(secrets[1]) != "historical-jwt-secret" {
		t.Fatalf("unexpected legacy secrets: %#v", secrets)
	}
	if _, err := ParseLegacyFieldSecrets(`["ok",""]`); err == nil {
		t.Fatal("expected empty legacy secret to be rejected")
	}
}

func TestDecryptWithAADStrictRejectsLegacyFormats(t *testing.T) {
	fe, _ := DeriveFieldEncryptor([]byte("test-jwt-secret-that-is-long-xxx"), "test")
	v1, err := fe.Encrypt("legacy")
	if err != nil {
		t.Fatal(err)
	}
	for _, stored := range []string{"plaintext", v1} {
		if _, strictErr := fe.DecryptWithAADStrict(stored, []byte("row")); strictErr == nil {
			t.Fatalf("strict open accepted %s", CiphertextFormat(stored))
		}
	}
	v2, err := fe.EncryptWithAAD("bound", []byte("row"))
	if err != nil {
		t.Fatal(err)
	}
	opened, err := fe.DecryptWithAADStrict(v2, []byte("row"))
	if err != nil || opened != "bound" {
		t.Fatalf("strict v2 open = %q, %v", opened, err)
	}
}

func TestV2CiphertextAuthenticatesPurposeAndVersion(t *testing.T) {
	secret := []byte("test-jwt-secret-that-is-long-xxx")
	first, _ := DeriveFieldEncryptor(secret, "purpose-a")
	second, _ := DeriveFieldEncryptor(secret, "purpose-b")
	stored, err := first.EncryptWithAAD("secret", []byte("row"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.DecryptWithAADStrict(stored, []byte("row")); err == nil {
		t.Fatal("v2 ciphertext opened under another HKDF purpose")
	}
}

func TestCompatibleAADOpenMigratesPreBindingV2ButStrictRejectsIt(t *testing.T) {
	fe, _ := DeriveFieldEncryptor([]byte("test-jwt-secret-that-is-long-xxx"), "test")
	aad := []byte("row")
	legacyV2, err := fe.encrypt("legacy-v2", aad, prefixV2)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := fe.DecryptWithAAD(legacyV2, aad)
	if err != nil || opened != "legacy-v2" {
		t.Fatalf("compatible legacy-v2 open = %q, %v", opened, err)
	}
	if _, err := fe.DecryptWithAADStrict(legacyV2, aad); err == nil {
		t.Fatal("strict v2 state accepted pre-binding ciphertext")
	}
}
