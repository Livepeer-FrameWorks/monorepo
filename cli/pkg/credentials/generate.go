package credentials

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"sort"
	"strings"

	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	"golang.org/x/crypto/hkdf"
)

// secretSpec defines a secret key and its generation parameters.
type secretSpec struct {
	Key     string
	ByteLen int // random bytes → hex-encoded (output is 2× this)
}

// generatable lists secrets the CLI can auto-generate when not provided.
var generatable = []secretSpec{
	{"DATABASE_RUNTIME_PASSWORD", 32},
	{"SERVICE_TOKEN", 32},
	{"CLUSTER_ACCESS_MATERIALIZATION_SECRET", 32},
	{"FOGHORN_BALANCER_CAPABILITY_SECRET", 32},
	{"FOGHORN_STATE_ENCRYPTION_KEY", 32},
	{"MEDIA_AUTHORITY_SEAL_ROOT_SECRET", 32},
	{"JWT_SECRET", 32},
	{"PASSWORD_RESET_SECRET", 32},
	{"FIELD_ENCRYPTION_KEY", 32},
	{"USAGE_HASH_SECRET", 32},
	{"TELEMETRY_TOKEN_SECRET", 32},
}

type MediaAuthoritySealPrivate struct {
	KeyID               string
	PrivateKeyPEMBase64 string
}

type mediaAuthoritySealRecipientJSON struct {
	KeyID     string `json:"key_id"`
	PublicKey string `json:"public_key"`
}

// DeriveMediaAuthoritySealMaterial deterministically derives a distinct
// X25519 recipient for every control cell. The root belongs to deployment
// tooling only: renderers pass Commodore the public map and each Foghorn only
// its own PKCS#8 private key.
func DeriveMediaAuthoritySealMaterial(rootHex string, cellIDs []string) (string, map[string]MediaAuthoritySealPrivate, error) {
	root, decodeErr := hex.DecodeString(strings.TrimSpace(rootHex))
	if decodeErr != nil || len(root) != 32 {
		return "", nil, fmt.Errorf("MEDIA_AUTHORITY_SEAL_ROOT_SECRET must be 64 hex characters")
	}
	unique := make(map[string]struct{}, len(cellIDs))
	for _, raw := range cellIDs {
		cellID := strings.TrimSpace(raw)
		if cellID == "" {
			return "", nil, fmt.Errorf("media authority seal control-cell ID is empty")
		}
		unique[cellID] = struct{}{}
	}
	cells := make([]string, 0, len(unique))
	for cellID := range unique {
		cells = append(cells, cellID)
	}
	sort.Strings(cells)
	if len(cells) == 0 {
		return "", nil, fmt.Errorf("media authority seal requires at least one control cell")
	}
	recipients := make(map[string]mediaAuthoritySealRecipientJSON, len(cells))
	privateKeys := make(map[string]MediaAuthoritySealPrivate, len(cells))
	curve := ecdh.X25519()
	for _, cellID := range cells {
		reader := hkdf.New(sha256.New, root, []byte("frameworks-media-authority-cell-seal-v1"), []byte(cellID))
		rawPrivate := make([]byte, 32)
		if _, readErr := io.ReadFull(reader, rawPrivate); readErr != nil {
			return "", nil, fmt.Errorf("derive media authority seal key for %q: %w", cellID, readErr)
		}
		privateKey, privateKeyErr := curve.NewPrivateKey(rawPrivate)
		if privateKeyErr != nil {
			return "", nil, fmt.Errorf("create media authority seal key for %q: %w", cellID, privateKeyErr)
		}
		publicBytes := privateKey.PublicKey().Bytes()
		keyID := sharedauthority.SealRecipientKeyID(cellID, privateKey.PublicKey())
		der, marshalErr := x509.MarshalPKCS8PrivateKey(privateKey)
		if marshalErr != nil {
			return "", nil, fmt.Errorf("encode media authority seal key for %q: %w", cellID, marshalErr)
		}
		recipients[cellID] = mediaAuthoritySealRecipientJSON{KeyID: keyID, PublicKey: base64.StdEncoding.EncodeToString(publicBytes)}
		privateKeys[cellID] = MediaAuthoritySealPrivate{
			KeyID: keyID, PrivateKeyPEMBase64: base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		}
	}
	encoded, err := json.Marshal(recipients)
	if err != nil {
		return "", nil, fmt.Errorf("encode media authority seal recipients: %w", err)
	}
	return string(encoded), privateKeys, nil
}

// DeriveFoghornStateEncryptionKey scopes the shared deployment root to one
// control cell while keeping replicas in that cell able to read the same
// durable obligations after failover.
func DeriveFoghornStateEncryptionKey(rootHex, cellID string) (string, error) {
	root, err := hex.DecodeString(strings.TrimSpace(rootHex))
	if err != nil || len(root) != 32 {
		return "", fmt.Errorf("FOGHORN_STATE_ENCRYPTION_KEY must be 64 hex characters")
	}
	cellID = strings.TrimSpace(cellID)
	if cellID == "" {
		return "", fmt.Errorf("foghorn state encryption requires a control cell ID")
	}
	reader := hkdf.New(sha256.New, root, []byte("frameworks-foghorn-state-encryption-v1"), []byte(cellID))
	derived := make([]byte, 32)
	if _, err := io.ReadFull(reader, derived); err != nil {
		return "", fmt.Errorf("derive Foghorn state encryption key: %w", err)
	}
	return hex.EncodeToString(derived), nil
}

var mediaAuthorityKeys = []string{
	"MEDIA_AUTHORITY_SIGNING_KEY_ID",
	"MEDIA_AUTHORITY_SIGNING_PRIVATE_KEY_PEM_B64",
	"MEDIA_AUTHORITY_TRUST_SET",
}

// isMissing returns true if the value is empty or a known placeholder.
func isMissing(v string) bool {
	switch v {
	case "", "change-me", "change-me-reset-key":
		return true
	}
	return false
}

// GenerateIfMissing inspects env for generatable secret keys.
// Any missing or placeholder values are replaced with cryptographically
// random hex strings. Returns the subset of keys that were generated
// (caller can persist these separately).
func GenerateIfMissing(env map[string]string) (map[string]string, error) {
	generated := make(map[string]string)
	missingAuthority := 0
	for _, key := range mediaAuthorityKeys {
		if isMissing(env[key]) {
			missingAuthority++
		}
	}
	if missingAuthority != 0 && missingAuthority != len(mediaAuthorityKeys) {
		return nil, fmt.Errorf("media authority signing configuration is partial; set all of %s or leave all as placeholders", strings.Join(mediaAuthorityKeys, ", "))
	}

	for _, spec := range generatable {
		if !isMissing(env[spec.Key]) {
			continue
		}

		val, err := randomHex(spec.ByteLen)
		if err != nil {
			return nil, fmt.Errorf("generate %s: %w", spec.Key, err)
		}

		env[spec.Key] = val
		generated[spec.Key] = val
	}

	if missingAuthority == len(mediaAuthorityKeys) {
		values, err := GenerateMediaAuthoritySigningTuple()
		if err != nil {
			return nil, err
		}
		for key, value := range values {
			env[key] = value
			generated[key] = value
		}
	}

	return generated, nil
}

// GenerateMediaAuthoritySigningTuple creates one internally consistent
// Ed25519 signer/trust-set tuple without generating unrelated platform
// credentials. Deployment tooling can persist these three values into an
// operator-owned secret store before lifecycle commands validate it.
func GenerateMediaAuthoritySigningTuple() (map[string]string, error) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate media authority Ed25519 key: %w", err)
	}
	keySuffix, err := randomHex(8)
	if err != nil {
		return nil, fmt.Errorf("generate media authority key ID: %w", err)
	}
	keyID := "media-authority-" + keySuffix
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("encode media authority private key: %w", err)
	}
	trustJSON, err := json.Marshal(map[string]string{keyID: base64.StdEncoding.EncodeToString(publicKey)})
	if err != nil {
		return nil, fmt.Errorf("encode media authority trust set: %w", err)
	}
	return map[string]string{
		mediaAuthorityKeys[0]: keyID,
		mediaAuthorityKeys[1]: base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		mediaAuthorityKeys[2]: string(trustJSON),
	}, nil
}

// GenerateMediaAuthorityDeploymentMaterial creates every new shared secret a
// production deployment needs before media-authority rendering can begin.
func GenerateMediaAuthorityDeploymentMaterial() (map[string]string, error) {
	values, err := GenerateMediaAuthoritySigningTuple()
	if err != nil {
		return nil, err
	}
	for _, key := range []string{"MEDIA_AUTHORITY_SEAL_ROOT_SECRET", "FOGHORN_STATE_ENCRYPTION_KEY"} {
		value, err := randomHex(32)
		if err != nil {
			return nil, fmt.Errorf("generate %s: %w", key, err)
		}
		values[key] = value
	}
	return values, nil
}

// GenerateSharedDeploymentMaterial creates a complete shared-secret set that passes
// ValidateShared. It is intended for first production bootstrap; upgrade tooling should use the
// narrower media-authority generator so existing platform secrets are never rotated implicitly.
func GenerateSharedDeploymentMaterial() (map[string]string, error) {
	values := make(map[string]string, len(generatable)+len(mediaAuthorityKeys))
	if _, err := GenerateIfMissing(values); err != nil {
		return nil, err
	}
	if err := ValidateShared(values); err != nil {
		return nil, fmt.Errorf("validate generated shared deployment material: %w", err)
	}
	return values, nil
}

// Keys returns the list of secret keys that GenerateIfMissing handles.
func Keys() []string {
	out := make([]string, 0, len(generatable)+len(mediaAuthorityKeys))
	for _, s := range generatable {
		out = append(out, s.Key)
	}
	out = append(out, mediaAuthorityKeys...)
	return out
}

// ValidateShared checks that all shared platform secrets are present in env.
// Returns an error listing any missing keys. For use in non-dev provisioning
// where shared secrets must come from manifest env_files, not auto-generation.
func ValidateShared(env map[string]string) error {
	var missing []string
	for _, spec := range generatable {
		if isMissing(env[spec.Key]) {
			missing = append(missing, spec.Key)
		}
	}
	for _, key := range mediaAuthorityKeys {
		if isMissing(env[key]) {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing shared platform secrets: %s — set them in your manifest env_files",
			strings.Join(missing, ", "))
	}
	if err := ValidateTelemetryTokenSecret(env["TELEMETRY_TOKEN_SECRET"]); err != nil {
		return err
	}
	return validateMediaAuthorityKeys(env)
}

func validateMediaAuthorityKeys(env map[string]string) error {
	keyID := strings.TrimSpace(env[mediaAuthorityKeys[0]])
	privatePEM, decodeErr := base64.StdEncoding.DecodeString(strings.TrimSpace(env[mediaAuthorityKeys[1]]))
	if decodeErr != nil {
		return fmt.Errorf("MEDIA_AUTHORITY_SIGNING_PRIVATE_KEY_PEM_B64 is not base64: %w", decodeErr)
	}
	block, rest := pem.Decode(privatePEM)
	if block == nil || block.Type != "PRIVATE KEY" || len(strings.TrimSpace(string(rest))) != 0 {
		return fmt.Errorf("MEDIA_AUTHORITY_SIGNING_PRIVATE_KEY_PEM_B64 must contain one PKCS#8 PRIVATE KEY PEM")
	}
	parsed, parseErr := x509.ParsePKCS8PrivateKey(block.Bytes)
	if parseErr != nil {
		return fmt.Errorf("parse media authority private key: %w", parseErr)
	}
	privateKey, ok := parsed.(ed25519.PrivateKey)
	if !ok || len(privateKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("media authority private key must be Ed25519")
	}
	var trust map[string]string
	if unmarshalErr := json.Unmarshal([]byte(env[mediaAuthorityKeys[2]]), &trust); unmarshalErr != nil {
		return fmt.Errorf("MEDIA_AUTHORITY_TRUST_SET must be a JSON object: %w", unmarshalErr)
	}
	encodedPublic, ok := trust[keyID]
	if !ok {
		return fmt.Errorf("MEDIA_AUTHORITY_TRUST_SET does not contain signing key ID %q", keyID)
	}
	publicKey, publicKeyErr := base64.StdEncoding.DecodeString(encodedPublic)
	if publicKeyErr != nil || len(publicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("MEDIA_AUTHORITY_TRUST_SET key %q is not a base64 Ed25519 public key", keyID)
	}
	derivedPublicKey, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok || !derivedPublicKey.Equal(ed25519.PublicKey(publicKey)) {
		return fmt.Errorf("media authority signing key does not match trust-set key %q", keyID)
	}
	return nil
}

// ValidateTelemetryTokenSecret enforces the shared Bridge signing-key wire
// format used across replicas: exactly 32 random bytes encoded as lowercase or
// uppercase hexadecimal.
func ValidateTelemetryTokenSecret(value string) error {
	if isMissing(value) {
		return fmt.Errorf("TELEMETRY_TOKEN_SECRET is required")
	}
	if len(value) != 64 {
		return fmt.Errorf("TELEMETRY_TOKEN_SECRET must be 64 hex characters (32 bytes)")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return fmt.Errorf("TELEMETRY_TOKEN_SECRET must be valid hex: %w", err)
	}
	return nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
