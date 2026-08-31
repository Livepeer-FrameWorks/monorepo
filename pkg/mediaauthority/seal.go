package mediaauthority

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"strings"

	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"golang.org/x/crypto/hkdf"
)

const (
	sealedSecretDomain = "frameworks-media-authority-sealed-secret-v1\x00"
	maxRecipientBytes  = 256 << 10
	maxRecipients      = 256
	maxSealedPlaintext = 1 << 20
)

type SealRecipient struct {
	KeyID     string
	PublicKey *ecdh.PublicKey
}

type SealRecipientSet map[string]SealRecipient

type sealRecipientJSON struct {
	KeyID     string `json:"key_id"`
	PublicKey string `json:"public_key"`
}

// ParseSealRecipients parses the Commodore-only map of control-cell IDs to
// X25519 recipient keys. Duplicate cell IDs, unknown fields, malformed keys,
// and unbounded input are rejected.
func ParseSealRecipients(encoded string) (SealRecipientSet, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, errors.New("media authority seal recipient set is empty")
	}
	if len(encoded) > maxRecipientBytes {
		return nil, errors.New("media authority seal recipient set exceeds size limit")
	}
	decoder := json.NewDecoder(strings.NewReader(encoded))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, errors.New("media authority seal recipient set must be a JSON object")
	}
	raw := make(map[string]sealRecipientJSON)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("decode seal recipient cell: %w", err)
		}
		cellID, ok := token.(string)
		if !ok || strings.TrimSpace(cellID) == "" || cellID != strings.TrimSpace(cellID) {
			return nil, errors.New("invalid seal recipient cell ID")
		}
		if _, duplicate := raw[cellID]; duplicate {
			return nil, fmt.Errorf("duplicate seal recipient cell ID %q", cellID)
		}
		var value sealRecipientJSON
		valueDecoderBytes, err := readRawJSONValue(decoder)
		if err != nil {
			return nil, fmt.Errorf("decode seal recipient %q: %w", cellID, err)
		}
		valueDecoder := json.NewDecoder(bytes.NewReader(valueDecoderBytes))
		valueDecoder.DisallowUnknownFields()
		if err := valueDecoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("decode seal recipient %q: %w", cellID, err)
		}
		raw[cellID] = value
		if len(raw) > maxRecipients {
			return nil, fmt.Errorf("media authority seal recipient set exceeds %d cells", maxRecipients)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return nil, fmt.Errorf("decode seal recipient object end: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, errors.New("media authority seal recipient set is empty")
	}
	curve := ecdh.X25519()
	out := make(SealRecipientSet, len(raw))
	for cellID, value := range raw {
		keyID := strings.TrimSpace(value.KeyID)
		if keyID == "" || keyID != value.KeyID || len(keyID) > maxSignerKeyIDBytes {
			return nil, fmt.Errorf("invalid seal recipient key ID for cell %q", cellID)
		}
		rawKey, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value.PublicKey))
		if err != nil {
			return nil, fmt.Errorf("decode seal recipient public key for cell %q: %w", cellID, err)
		}
		publicKey, err := curve.NewPublicKey(rawKey)
		if err != nil {
			return nil, fmt.Errorf("parse X25519 recipient public key for cell %q: %w", cellID, err)
		}
		if expected := SealRecipientKeyID(cellID, publicKey); keyID != expected {
			return nil, fmt.Errorf("seal recipient key ID for cell %q does not bind that cell and public key", cellID)
		}
		out[cellID] = SealRecipient{KeyID: keyID, PublicKey: publicKey}
	}
	return out, nil
}

// SealRecipientKeyID binds a recipient identifier to its control cell and
// X25519 public key. Runtime consumers recompute it so a valid key cannot be
// paired accidentally with another cell's authority audience.
func SealRecipientKeyID(cellID string, publicKey *ecdh.PublicKey) string {
	cellID = strings.TrimSpace(cellID)
	if cellID == "" || publicKey == nil || publicKey.Curve() != ecdh.X25519() {
		return ""
	}
	digest := sha256.Sum256(append([]byte(cellID+"\x00"), publicKey.Bytes()...))
	return "media-seal-" + hex.EncodeToString(digest[:8])
}

func readRawJSONValue(decoder *json.Decoder) (json.RawMessage, error) {
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func ParseSealPrivateKey(encoded string) (*ecdh.PrivateKey, error) {
	pemBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return nil, fmt.Errorf("decode media authority seal private key: %w", err)
	}
	block, rest := pem.Decode(pemBytes)
	if block == nil || block.Type != "PRIVATE KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("media authority seal private key must contain one PKCS#8 PRIVATE KEY PEM block")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse media authority seal private key: %w", err)
	}
	privateKey, ok := parsed.(*ecdh.PrivateKey)
	if !ok || privateKey.Curve() != ecdh.X25519() {
		return nil, errors.New("media authority seal private key is not X25519")
	}
	return privateKey, nil
}

func SealSecret(cellID, authorityID string, recipient SealRecipient, plaintext []byte) (*mediaauthoritypb.SealedCellSecret, error) {
	cellID, authorityID = strings.TrimSpace(cellID), strings.TrimSpace(authorityID)
	if cellID == "" || authorityID == "" || recipient.PublicKey == nil || strings.TrimSpace(recipient.KeyID) == "" {
		return nil, errors.New("sealed secret requires cell, authority, key ID, and recipient public key")
	}
	if len(plaintext) == 0 || len(plaintext) > maxSealedPlaintext {
		return nil, fmt.Errorf("sealed secret plaintext size must be 1..%d bytes", maxSealedPlaintext)
	}
	curve := ecdh.X25519()
	ephemeral, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ephemeral X25519 key: %w", err)
	}
	shared, err := ephemeral.ECDH(recipient.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("derive sealed secret: %w", err)
	}
	aad := sealedSecretAAD(cellID, recipient.KeyID, authorityID)
	key, err := sealedSecretKey(shared, ephemeral.PublicKey().Bytes(), recipient.PublicKey.Bytes(), aad)
	if err != nil {
		return nil, err
	}
	gcm, err := newSealGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate sealed secret nonce: %w", err)
	}
	return &mediaauthoritypb.SealedCellSecret{
		AudienceCellId: cellID, RecipientKeyId: recipient.KeyID,
		EphemeralPublicKey: ephemeral.PublicKey().Bytes(), Nonce: nonce,
		Ciphertext: gcm.Seal(nil, nonce, plaintext, aad),
	}, nil
}

func OpenSecret(box *mediaauthoritypb.SealedCellSecret, cellID, authorityID, keyID string, privateKey *ecdh.PrivateKey) ([]byte, error) {
	if box == nil || privateKey == nil || box.GetAudienceCellId() != strings.TrimSpace(cellID) ||
		box.GetRecipientKeyId() != strings.TrimSpace(keyID) || strings.TrimSpace(authorityID) == "" {
		return nil, errors.New("sealed secret scope or key mismatch")
	}
	curve := ecdh.X25519()
	ephemeral, err := curve.NewPublicKey(box.GetEphemeralPublicKey())
	if err != nil {
		return nil, fmt.Errorf("parse sealed secret ephemeral key: %w", err)
	}
	shared, err := privateKey.ECDH(ephemeral)
	if err != nil {
		return nil, fmt.Errorf("derive sealed secret: %w", err)
	}
	aad := sealedSecretAAD(cellID, keyID, authorityID)
	key, err := sealedSecretKey(shared, ephemeral.Bytes(), privateKey.PublicKey().Bytes(), aad)
	if err != nil {
		return nil, err
	}
	gcm, err := newSealGCM(key)
	if err != nil {
		return nil, err
	}
	if len(box.GetNonce()) != gcm.NonceSize() {
		return nil, errors.New("sealed secret nonce has invalid size")
	}
	plaintext, err := gcm.Open(nil, box.GetNonce(), box.GetCiphertext(), aad)
	if err != nil {
		return nil, errors.New("sealed secret authentication failed")
	}
	if len(plaintext) == 0 || len(plaintext) > maxSealedPlaintext {
		return nil, errors.New("sealed secret plaintext has invalid size")
	}
	return plaintext, nil
}

func sealedSecretAAD(cellID, keyID, authorityID string) []byte {
	var out bytes.Buffer
	out.WriteString(sealedSecretDomain)
	for _, value := range []string{cellID, keyID, authorityID} {
		var encodedLength [4]byte
		binary.BigEndian.PutUint32(encodedLength[:], uint32(len(value)))
		out.Write(encodedLength[:])
		out.WriteString(value)
	}
	return out.Bytes()
}

func sealedSecretKey(shared, ephemeralPublic, recipientPublic, aad []byte) ([]byte, error) {
	saltInput := append(append([]byte(nil), ephemeralPublic...), recipientPublic...)
	salt := sha256.Sum256(saltInput)
	reader := hkdf.New(sha256.New, shared, salt[:], aad)
	key := make([]byte, 32)
	if _, err := io.ReadFull(reader, key); err != nil {
		return nil, fmt.Errorf("derive sealed secret AEAD key: %w", err)
	}
	return key, nil
}

func newSealGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create sealed secret cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create sealed secret AEAD: %w", err)
	}
	return gcm, nil
}
