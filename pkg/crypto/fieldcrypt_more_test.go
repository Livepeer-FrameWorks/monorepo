package crypto

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestDecrypt_CiphertextLengthBoundary(t *testing.T) {
	fe, err := DeriveFieldEncryptor([]byte("test-jwt-secret-that-is-long-xxx"), "boundary")
	if err != nil {
		t.Fatalf("DeriveFieldEncryptor: %v", err)
	}
	nonceSize := fe.gcm.NonceSize()

	cases := []struct {
		name    string
		dataLen int
		wantSub string
	}{
		{"one byte below nonce size is too short", nonceSize - 1, "too short"},
		{"exactly nonce size passes length gate then fails GCM", nonceSize, "decryption failed"},
		{"above nonce size fails GCM", nonceSize + 1, "decryption failed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data := make([]byte, c.dataLen)
			stored := "enc:v1:" + base64.StdEncoding.EncodeToString(data)
			_, err := fe.Decrypt(stored)
			if err == nil {
				t.Fatalf("expected error for data length %d", c.dataLen)
			}
			if !strings.Contains(err.Error(), c.wantSub) {
				t.Fatalf("error %q does not contain %q", err.Error(), c.wantSub)
			}
		})
	}
}
