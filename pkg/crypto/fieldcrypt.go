// Package crypto provides application-level field encryption using AES-256-GCM.
//
// Encrypted values carry a version prefix so callers can explicitly end a
// legacy plaintext/v1 migration instead of accepting unauthenticated input
// forever.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/hkdf"
)

const (
	prefixV1 = "enc:v1:"
	prefixV2 = "enc:v2:"
)

// FieldEncryptor encrypts and decrypts string fields at the application level.
// Safe for concurrent use.
type FieldEncryptor struct {
	gcm     cipher.AEAD
	purpose string
}

type FieldCiphertextFormat string

const (
	FieldCiphertextPlaintext FieldCiphertextFormat = "plaintext"
	FieldCiphertextV1        FieldCiphertextFormat = "v1"
	FieldCiphertextV2        FieldCiphertextFormat = "v2"
)

// DeriveFieldEncryptor derives an AES-256 key from an existing secret using HKDF
// and returns a FieldEncryptor. The purpose string isolates this derived key from
// other uses of the same master secret.
func DeriveFieldEncryptor(masterSecret []byte, purpose string) (*FieldEncryptor, error) {
	hkdfReader := hkdf.New(sha256.New, masterSecret, []byte("frameworks-field-encryption"), []byte(purpose))
	key := make([]byte, 32)
	if _, err := io.ReadFull(hkdfReader, key); err != nil {
		return nil, fmt.Errorf("crypto: HKDF derivation failed: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: %w", err)
	}
	return &FieldEncryptor{gcm: gcm, purpose: purpose}, nil
}

// Encrypt encrypts plaintext and returns a prefixed string suitable for DB storage.
func (fe *FieldEncryptor) Encrypt(plaintext string) (string, error) {
	return fe.encrypt(plaintext, nil, prefixV1)
}

// EncryptWithAAD binds ciphertext to its owning record. The same additional
// data must be supplied to DecryptWithAAD; it is authenticated but not stored.
func (fe *FieldEncryptor) EncryptWithAAD(plaintext string, additionalData []byte) (string, error) {
	return fe.encrypt(plaintext, fe.versionedAAD(prefixV2, additionalData), prefixV2)
}

func (fe *FieldEncryptor) encrypt(plaintext string, additionalData []byte, versionPrefix string) (string, error) {
	nonce := make([]byte, fe.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("crypto: failed to generate nonce: %w", err)
	}
	ciphertext := fe.gcm.Seal(nonce, nonce, []byte(plaintext), additionalData)
	return versionPrefix + base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt decrypts a value previously produced by Encrypt.
// If the value lacks the "enc:v1:" prefix, it is returned as-is (plaintext passthrough
// for backward compatibility during migration).
func (fe *FieldEncryptor) Decrypt(stored string) (string, error) {
	return fe.decrypt(stored, nil, false)
}

// DecryptWithAAD opens row-bound v2 ciphertext and retains compatibility with
// plaintext and v1 ciphertext written before row binding was introduced.
func (fe *FieldEncryptor) DecryptWithAAD(stored string, additionalData []byte) (string, error) {
	return fe.decrypt(stored, additionalData, true)
}

// DecryptWithAADStrict accepts only v2 ciphertext. Use this after a column's
// legacy rows have been migrated, or when a row is explicitly marked as v2.
func (fe *FieldEncryptor) DecryptWithAADStrict(stored string, additionalData []byte) (string, error) {
	if CiphertextFormat(stored) != FieldCiphertextV2 {
		return "", errors.New("crypto: v2 ciphertext required")
	}
	return fe.decrypt(stored, additionalData, false)
}

func (fe *FieldEncryptor) decrypt(stored string, additionalData []byte, allowLegacyV2 bool) (string, error) {
	versionPrefix := ""
	var aad []byte
	switch {
	case strings.HasPrefix(stored, prefixV2):
		versionPrefix = prefixV2
		aad = fe.versionedAAD(prefixV2, additionalData)
	case strings.HasPrefix(stored, prefixV1):
		versionPrefix = prefixV1
	default:
		return stored, nil
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, versionPrefix))
	if err != nil {
		return "", fmt.Errorf("crypto: invalid base64: %w", err)
	}
	nonceSize := fe.gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("crypto: ciphertext too short")
	}
	plaintext, err := fe.gcm.Open(nil, data[:nonceSize], data[nonceSize:], aad)
	if err != nil && versionPrefix == prefixV2 && allowLegacyV2 {
		// Compatibility is deliberately available only through DecryptWithAAD.
		// Callers with an explicit v2 row marker use the strict method, while a
		// migration reader may open ciphertext written before the version and
		// HKDF purpose were folded into the authenticated data.
		plaintext, err = fe.gcm.Open(nil, data[:nonceSize], data[nonceSize:], additionalData)
	}
	if err != nil {
		return "", fmt.Errorf("crypto: decryption failed: %w", err)
	}
	return string(plaintext), nil
}

func (fe *FieldEncryptor) versionedAAD(versionPrefix string, additionalData []byte) []byte {
	aad := make([]byte, 0, len(versionPrefix)+len(fe.purpose)+len(additionalData)+2)
	aad = append(aad, versionPrefix...)
	aad = append(aad, 0)
	aad = append(aad, fe.purpose...)
	aad = append(aad, 0)
	aad = append(aad, additionalData...)
	return aad
}

// CiphertextFormat reports the authenticated storage format without opening
// the value. Unknown or absent prefixes are legacy plaintext.
func CiphertextFormat(stored string) FieldCiphertextFormat {
	switch {
	case strings.HasPrefix(stored, prefixV2):
		return FieldCiphertextV2
	case strings.HasPrefix(stored, prefixV1):
		return FieldCiphertextV1
	default:
		return FieldCiphertextPlaintext
	}
}

// IsEncrypted returns true if the stored value has the encryption prefix.
func IsEncrypted(stored string) bool {
	return strings.HasPrefix(stored, prefixV1) || strings.HasPrefix(stored, prefixV2)
}
