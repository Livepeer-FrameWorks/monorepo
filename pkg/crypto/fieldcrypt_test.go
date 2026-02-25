package crypto

import (
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
