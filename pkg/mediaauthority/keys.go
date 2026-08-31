package mediaauthority

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	maxTrustSetBytes = 64 << 10
	maxTrustedKeys   = 64
)

// ParseSigningPrivateKey decodes a base64-wrapped PKCS#8 Ed25519 private-key
// PEM. It deliberately accepts neither raw seeds nor service-token-derived
// fallbacks, keeping key custody and rotation explicit.
func ParseSigningPrivateKey(encoded string) (ed25519.PrivateKey, error) {
	derPEM, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return nil, fmt.Errorf("decode media authority private key: %w", err)
	}
	block, rest := pem.Decode(derPEM)
	if block == nil || block.Type != "PRIVATE KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("media authority private key must contain one PKCS#8 PRIVATE KEY PEM block")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse media authority private key: %w", err)
	}
	privateKey, ok := parsed.(ed25519.PrivateKey)
	if !ok || len(privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("media authority private key is not Ed25519")
	}
	return append(ed25519.PrivateKey(nil), privateKey...), nil
}

// ParseTrustSet decodes a JSON object mapping signer key IDs to standard-base64
// raw Ed25519 public keys. Multiple entries support current/next overlap.
func ParseTrustSet(encoded string) (TrustSet, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, errors.New("media authority trust set is empty")
	}
	if len(encoded) > maxTrustSetBytes {
		return nil, errors.New("media authority trust set exceeds size limit")
	}
	decoder := json.NewDecoder(strings.NewReader(encoded))
	start, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("decode media authority trust set: %w", err)
	}
	if start != json.Delim('{') {
		return nil, errors.New("decode media authority trust set: expected JSON object")
	}
	values := make(map[string]string)
	for decoder.More() {
		token, tokenErr := decoder.Token()
		if tokenErr != nil {
			return nil, fmt.Errorf("decode media authority trust set key: %w", tokenErr)
		}
		keyID, ok := token.(string)
		if !ok {
			return nil, errors.New("decode media authority trust set: non-string key")
		}
		if _, duplicate := values[keyID]; duplicate {
			return nil, fmt.Errorf("media authority trust set contains duplicate signer key ID %q", keyID)
		}
		var value string
		if decodeErr := decoder.Decode(&value); decodeErr != nil {
			return nil, fmt.Errorf("decode media authority public key %q: %w", keyID, decodeErr)
		}
		values[keyID] = value
		if len(values) > maxTrustedKeys {
			return nil, fmt.Errorf("media authority trust set must contain 1..%d keys", maxTrustedKeys)
		}
	}
	end, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("decode media authority trust set object end: %w", err)
	}
	if end != json.Delim('}') {
		return nil, errors.New("decode media authority trust set: expected object end")
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, err
	}
	if len(values) == 0 || len(values) > maxTrustedKeys {
		return nil, fmt.Errorf("media authority trust set must contain 1..%d keys", maxTrustedKeys)
	}
	trust := make(TrustSet, len(values))
	for keyID, encodedKey := range values {
		if strings.TrimSpace(keyID) == "" || keyID != strings.TrimSpace(keyID) || len(keyID) > maxSignerKeyIDBytes {
			return nil, fmt.Errorf("invalid media authority signer key ID %q", keyID)
		}
		publicKey, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encodedKey))
		if err != nil {
			return nil, fmt.Errorf("decode media authority public key %q: %w", keyID, err)
		}
		if len(publicKey) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("media authority public key %q is not Ed25519", keyID)
		}
		trust[keyID] = append(ed25519.PublicKey(nil), publicKey...)
	}
	return trust, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("media authority trust set contains trailing JSON")
		}
		return fmt.Errorf("decode trailing media authority trust set data: %w", err)
	}
	return nil
}
