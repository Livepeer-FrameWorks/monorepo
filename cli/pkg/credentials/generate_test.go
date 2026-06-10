package credentials

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestIsMissing(t *testing.T) {
	missing := []string{"", "change-me", "change-me-reset-key"}
	for _, v := range missing {
		if !isMissing(v) {
			t.Errorf("isMissing(%q) = false, want true", v)
		}
	}
	present := []string{"a-real-secret", "change-me-not", " "}
	for _, v := range present {
		if isMissing(v) {
			t.Errorf("isMissing(%q) = true, want false", v)
		}
	}
}

func TestKeys(t *testing.T) {
	keys := Keys()
	if len(keys) != len(generatable) {
		t.Fatalf("Keys() len = %d, want %d", len(keys), len(generatable))
	}
	want := map[string]bool{
		"SERVICE_TOKEN": true, "JWT_SECRET": true, "PASSWORD_RESET_SECRET": true,
		"FIELD_ENCRYPTION_KEY": true, "USAGE_HASH_SECRET": true,
	}
	for _, k := range keys {
		if !want[k] {
			t.Errorf("unexpected key %q", k)
		}
	}
}

// GenerateIfMissing must fill every missing/placeholder key, leave already-set
// real values untouched, and report exactly the keys it generated.
func TestGenerateIfMissingFillsOnlyMissing(t *testing.T) {
	env := map[string]string{
		"SERVICE_TOKEN": "already-set", // real value -> preserved
		"JWT_SECRET":    "change-me",   // placeholder -> regenerated
		// the remaining generatable keys are absent -> generated
	}

	generated, err := GenerateIfMissing(env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if env["SERVICE_TOKEN"] != "already-set" {
		t.Errorf("real value was overwritten: %q", env["SERVICE_TOKEN"])
	}
	if _, ok := generated["SERVICE_TOKEN"]; ok {
		t.Errorf("SERVICE_TOKEN should not be reported as generated")
	}

	// Every key except the preserved one should now be present and reported.
	for _, spec := range generatable {
		if spec.Key == "SERVICE_TOKEN" {
			continue
		}
		val, ok := generated[spec.Key]
		if !ok {
			t.Errorf("%s was not generated", spec.Key)
			continue
		}
		if env[spec.Key] != val {
			t.Errorf("%s: env value %q != generated %q", spec.Key, env[spec.Key], val)
		}
		// 32 random bytes -> 64 hex chars.
		if len(val) != spec.ByteLen*2 {
			t.Errorf("%s: hex len = %d, want %d", spec.Key, len(val), spec.ByteLen*2)
		}
		if _, decErr := hex.DecodeString(val); decErr != nil {
			t.Errorf("%s: not valid hex: %v", spec.Key, decErr)
		}
	}
}

// Two generated secrets must not collide (sanity check on randomness).
func TestGenerateIfMissingProducesDistinctValues(t *testing.T) {
	env := map[string]string{}
	if _, err := GenerateIfMissing(env); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if env["JWT_SECRET"] == env["SERVICE_TOKEN"] {
		t.Fatalf("two generated secrets are identical: %q", env["JWT_SECRET"])
	}
}

func TestValidateShared(t *testing.T) {
	// All present -> no error.
	full := map[string]string{}
	for _, spec := range generatable {
		full[spec.Key] = "real-value"
	}
	if err := ValidateShared(full); err != nil {
		t.Fatalf("ValidateShared(full) = %v, want nil", err)
	}

	// Missing + placeholder keys are reported by name.
	partial := map[string]string{
		"SERVICE_TOKEN": "real-value",
		"JWT_SECRET":    "change-me", // placeholder counts as missing
		// others absent
	}
	err := ValidateShared(partial)
	if err == nil {
		t.Fatalf("ValidateShared(partial) = nil, want error")
	}
	if !strings.Contains(err.Error(), "JWT_SECRET") || !strings.Contains(err.Error(), "FIELD_ENCRYPTION_KEY") {
		t.Errorf("error should name the missing keys, got: %v", err)
	}
	if strings.Contains(err.Error(), "SERVICE_TOKEN") {
		t.Errorf("error should not name the present key SERVICE_TOKEN, got: %v", err)
	}
}

func TestRandomHex(t *testing.T) {
	h, err := randomHex(16)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(h) != 32 {
		t.Fatalf("randomHex(16) len = %d, want 32", len(h))
	}
	if _, err := hex.DecodeString(h); err != nil {
		t.Fatalf("not valid hex: %v", err)
	}
}
