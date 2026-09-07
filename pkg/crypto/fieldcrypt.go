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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/hkdf"
)

const (
	prefixV1 = "enc:v1:"
	prefixV2 = "enc:v2:"
	prefixV3 = "enc:v3:"
)

// FieldCipher is the storage-facing subset shared by single-key encryptors and
// rotating keyrings.
type FieldCipher interface {
	Encrypt(plaintext string) (string, error)
	Decrypt(stored string) (string, error)
}

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
	FieldCiphertextV3        FieldCiphertextFormat = "v3"
)

// FieldKeyring writes with one named active key and reads ciphertext produced
// by the active, previous, or explicitly supplied legacy encryptors. v3 embeds
// only the non-secret key ID, allowing rotation without trial-decrypting every
// configured key.
type FieldKeyring struct {
	activeID string
	active   *FieldEncryptor
	keys     map[string]*FieldEncryptor
	legacy   []*FieldEncryptor
	purpose  string
}

// ActiveEnvelopePrefix identifies ciphertext already written by this
// keyring's active key. It contains no secret material and is intended for
// bounded rotation queries.
func (ring *FieldKeyring) ActiveEnvelopePrefix() string {
	return prefixV3 + ring.activeID + ":"
}

// ParseFieldKeySet parses the JSON object used by
// FIELD_ENCRYPTION_PREVIOUS_KEYS. Values are master-secret strings; the caller
// supplies the purpose when constructing a keyring.
func ParseFieldKeySet(raw string) (map[string][]byte, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var encoded map[string]string
	if err := json.Unmarshal([]byte(raw), &encoded); err != nil {
		return nil, fmt.Errorf("crypto: parse previous field keys: %w", err)
	}
	keys := make(map[string][]byte, len(encoded))
	for id, secret := range encoded {
		if !validFieldKeyID(id) || len(secret) < 16 {
			return nil, fmt.Errorf("crypto: invalid previous field key %q", id)
		}
		keys[id] = []byte(secret)
	}
	return keys, nil
}

// ParseLegacyFieldSecrets parses read-only v1/v2 master secrets. Unlike active
// and previous v3 field keys, these values may be shorter than 16 bytes because
// historical JWT secrets had no field-encryption length requirement. They are
// never eligible for new writes.
func ParseLegacyFieldSecrets(raw string) ([][]byte, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var encoded []string
	if err := json.Unmarshal([]byte(raw), &encoded); err != nil {
		return nil, fmt.Errorf("crypto: parse legacy field secrets: %w", err)
	}
	secrets := make([][]byte, 0, len(encoded))
	for index, secret := range encoded {
		if secret == "" {
			return nil, fmt.Errorf("crypto: empty legacy field secret at index %d", index)
		}
		secrets = append(secrets, []byte(secret))
	}
	return secrets, nil
}

// NewFieldKeyring constructs a rotating field cipher. Key IDs are restricted
// to a storage-safe alphabet because they are embedded in the ciphertext
// envelope. legacySecrets are read-only v1/v2 keys and are never used to write.
func NewFieldKeyring(activeID string, activeSecret []byte, previous map[string][]byte, legacySecrets [][]byte, purpose string) (*FieldKeyring, error) {
	if !validFieldKeyID(activeID) {
		return nil, errors.New("crypto: invalid active field key ID")
	}
	if len(activeSecret) < 16 {
		return nil, errors.New("crypto: active field key must be at least 16 bytes")
	}
	active, err := DeriveFieldEncryptor(activeSecret, purpose)
	if err != nil {
		return nil, err
	}
	ring := &FieldKeyring{
		activeID: activeID,
		active:   active,
		keys:     map[string]*FieldEncryptor{activeID: active},
		purpose:  purpose,
	}
	for id, secret := range previous {
		if !validFieldKeyID(id) || id == activeID {
			return nil, fmt.Errorf("crypto: invalid or duplicate previous field key ID %q", id)
		}
		if len(secret) < 16 {
			return nil, fmt.Errorf("crypto: previous field key %q must be at least 16 bytes", id)
		}
		derived, deriveErr := DeriveFieldEncryptor(secret, purpose)
		if deriveErr != nil {
			return nil, deriveErr
		}
		ring.keys[id] = derived
	}
	for _, secret := range legacySecrets {
		if len(secret) == 0 {
			continue
		}
		derived, deriveErr := DeriveFieldEncryptor(secret, purpose)
		if deriveErr != nil {
			return nil, deriveErr
		}
		ring.legacy = append(ring.legacy, derived)
	}
	return ring, nil
}

func validFieldKeyID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}

// Encrypt writes an authenticated v3 envelope with the active key ID.
func (ring *FieldKeyring) Encrypt(plaintext string) (string, error) {
	prefix := ring.ActiveEnvelopePrefix()
	aad := []byte(prefix + "\x00" + ring.purpose)
	return ring.active.encrypt(plaintext, aad, prefix)
}

// Decrypt opens v3 by key ID and retains read-only compatibility with v1/v2
// ciphertext during migration. Legacy plaintext remains readable so existing
// bootstrap rows can be re-encrypted by the data migration.
func (ring *FieldKeyring) Decrypt(stored string) (string, error) {
	if strings.HasPrefix(stored, prefixV3) {
		remainder := strings.TrimPrefix(stored, prefixV3)
		parts := strings.SplitN(remainder, ":", 2)
		if len(parts) != 2 || !validFieldKeyID(parts[0]) {
			return "", errors.New("crypto: malformed v3 field ciphertext")
		}
		encryptor, ok := ring.keys[parts[0]]
		if !ok {
			return "", fmt.Errorf("crypto: unknown field key ID %q", parts[0])
		}
		prefix := prefixV3 + parts[0] + ":"
		return encryptor.decryptEnvelope(parts[1], []byte(prefix+"\x00"+ring.purpose))
	}
	if !IsEncrypted(stored) {
		return stored, nil
	}
	var lastErr error
	for _, legacy := range ring.legacy {
		plain, err := legacy.Decrypt(stored)
		if err == nil {
			return plain, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("no legacy field keys configured")
	}
	return "", fmt.Errorf("crypto: legacy field decryption failed: %w", lastErr)
}

func (fe *FieldEncryptor) decryptEnvelope(encoded string, additionalData []byte) (string, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("crypto: invalid base64: %w", err)
	}
	nonceSize := fe.gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("crypto: ciphertext too short")
	}
	plaintext, err := fe.gcm.Open(nil, data[:nonceSize], data[nonceSize:], additionalData)
	if err != nil {
		return "", fmt.Errorf("crypto: decryption failed: %w", err)
	}
	return string(plaintext), nil
}

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
	case strings.HasPrefix(stored, prefixV3):
		return FieldCiphertextV3
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
	return strings.HasPrefix(stored, prefixV1) || strings.HasPrefix(stored, prefixV2) || strings.HasPrefix(stored, prefixV3)
}
