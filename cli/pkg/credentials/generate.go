package credentials

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// secretSpec defines a secret key and its generation parameters.
type secretSpec struct {
	Key     string
	ByteLen int // random bytes → hex-encoded (output is 2× this)
}

// generatable lists secrets the CLI can auto-generate when not provided.
var generatable = []secretSpec{
	{"SERVICE_TOKEN", 32},
	{"JWT_SECRET", 32},
	{"PASSWORD_RESET_SECRET", 32},
	{"FIELD_ENCRYPTION_KEY", 32},
	{"USAGE_HASH_SECRET", 32},
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

	return generated, nil
}

// Keys returns the list of secret keys that GenerateIfMissing handles.
func Keys() []string {
	out := make([]string, len(generatable))
	for i, s := range generatable {
		out[i] = s.Key
	}
	return out
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
