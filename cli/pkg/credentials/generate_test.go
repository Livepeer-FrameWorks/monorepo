package credentials

import (
	"crypto/ecdh"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"runtime"
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
	if len(keys) != len(generatable)+len(mediaAuthorityKeys) {
		t.Fatalf("Keys() len = %d, want %d", len(keys), len(generatable)+len(mediaAuthorityKeys))
	}
	want := map[string]bool{
		"DATABASE_RUNTIME_PASSWORD": true,
		"SERVICE_TOKEN":             true, "JWT_SECRET": true, "PASSWORD_RESET_SECRET": true,
		"FIELD_ENCRYPTION_KEY": true, "USAGE_HASH_SECRET": true, "TELEMETRY_TOKEN_SECRET": true,
		"CLUSTER_ACCESS_MATERIALIZATION_SECRET":       true,
		"FOGHORN_BALANCER_CAPABILITY_SECRET":          true,
		"FOGHORN_STATE_ENCRYPTION_KEY":                true,
		"MEDIA_AUTHORITY_SEAL_ROOT_SECRET":            true,
		"MEDIA_AUTHORITY_SIGNING_KEY_ID":              true,
		"MEDIA_AUTHORITY_SIGNING_PRIVATE_KEY_PEM_B64": true,
		"MEDIA_AUTHORITY_TRUST_SET":                   true,
	}
	for _, k := range keys {
		if !want[k] {
			t.Errorf("unexpected key %q", k)
		}
	}
}

func TestDeriveMediaAuthoritySealMaterialIsCellScopedAndStable(t *testing.T) {
	root := strings.Repeat("ab", 32)
	recipientsJSON, privateKeys, deriveErr := DeriveMediaAuthoritySealMaterial(root, []string{"cell-b", "cell-a", "cell-a"})
	if deriveErr != nil {
		t.Fatal(deriveErr)
	}
	var recipients map[string]mediaAuthoritySealRecipientJSON
	if unmarshalErr := json.Unmarshal([]byte(recipientsJSON), &recipients); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	if len(recipients) != 2 || len(privateKeys) != 2 || privateKeys["cell-a"].KeyID == privateKeys["cell-b"].KeyID {
		t.Fatalf("unexpected derived material: recipients=%+v private=%+v", recipients, privateKeys)
	}
	for cellID, privateMaterial := range privateKeys {
		pemBytes, decodeErr := base64.StdEncoding.DecodeString(privateMaterial.PrivateKeyPEMBase64)
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		block, _ := pem.Decode(pemBytes)
		parsed, parseErr := x509.ParsePKCS8PrivateKey(block.Bytes)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		privateKey, ok := parsed.(*ecdh.PrivateKey)
		if !ok || privateMaterial.KeyID != recipients[cellID].KeyID || base64.StdEncoding.EncodeToString(privateKey.PublicKey().Bytes()) != recipients[cellID].PublicKey {
			t.Fatalf("cell %q private/public material mismatch", cellID)
		}
	}
	again, _, err := DeriveMediaAuthoritySealMaterial(root, []string{"cell-a", "cell-b"})
	if err != nil || again != recipientsJSON {
		t.Fatalf("derivation is not stable: %v, %q != %q", err, again, recipientsJSON)
	}
	if _, _, err := DeriveMediaAuthoritySealMaterial("bad", []string{"cell-a"}); err == nil {
		t.Fatal("invalid seal root was accepted")
	}
}

func TestSecretsTemplateCoversGeneratableSharedSecrets(t *testing.T) {
	t.Parallel()

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve credentials test path")
	}
	templatePath := filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "config", "env", "secrets.env.example")
	contents, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatalf("read shared secret template: %v", err)
	}

	declared := make(map[string]bool)
	for _, line := range strings.Split(string(contents), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, _, found := strings.Cut(line, "=")
		if found {
			declared[strings.TrimSpace(key)] = true
		}
	}
	for _, key := range Keys() {
		if !declared[key] {
			t.Errorf("%s is generated and required but missing from config/env/secrets.env.example", key)
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
	for _, key := range mediaAuthorityKeys {
		if _, ok := generated[key]; !ok {
			t.Errorf("%s was not generated", key)
		}
	}
	if err := validateMediaAuthorityKeys(env); err != nil {
		t.Fatalf("generated media authority key material is inconsistent: %v", err)
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

func TestDeriveFoghornStateEncryptionKeyScopesByCell(t *testing.T) {
	root := strings.Repeat("ab", 32)
	a, err := DeriveFoghornStateEncryptionKey(root, "cell-a")
	if err != nil {
		t.Fatal(err)
	}
	aReplica, err := DeriveFoghornStateEncryptionKey(root, "cell-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := DeriveFoghornStateEncryptionKey(root, "cell-b")
	if err != nil {
		t.Fatal(err)
	}
	if a != aReplica || a == b || len(a) != 64 {
		t.Fatalf("unexpected cell derivation: a=%q replica=%q b=%q", a, aReplica, b)
	}
}

func TestValidateShared(t *testing.T) {
	// All present -> no error.
	full := map[string]string{}
	for _, spec := range generatable {
		full[spec.Key] = "real-value"
	}
	full["DATABASE_PASSWORD"] = "owner-value"
	full["DATABASE_RUNTIME_PASSWORD"] = "runtime-value"
	full["TELEMETRY_TOKEN_SECRET"] = strings.Repeat("ab", 32)
	if _, err := GenerateIfMissing(full); err != nil {
		t.Fatalf("generate media authority test keys: %v", err)
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

func TestGenerateIfMissingRejectsPartialMediaAuthorityKeys(t *testing.T) {
	env := map[string]string{"MEDIA_AUTHORITY_SIGNING_KEY_ID": "current"}
	if _, err := GenerateIfMissing(env); err == nil {
		t.Fatal("partial media authority key configuration was accepted")
	}
}

func TestGenerateMediaAuthorityDeploymentMaterialIsCompleteAndValid(t *testing.T) {
	values, err := GenerateMediaAuthorityDeploymentMaterial()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateMediaAuthorityKeys(values); err != nil {
		t.Fatalf("generated signing tuple is invalid: %v", err)
	}
	for _, key := range []string{"MEDIA_AUTHORITY_SEAL_ROOT_SECRET", "FOGHORN_STATE_ENCRYPTION_KEY"} {
		if raw, err := hex.DecodeString(values[key]); err != nil || len(raw) != 32 {
			t.Fatalf("%s is not a 32-byte hex root", key)
		}
	}
}

func TestValidateTelemetryTokenSecret(t *testing.T) {
	if err := ValidateTelemetryTokenSecret(strings.Repeat("ab", 32)); err != nil {
		t.Fatalf("valid secret rejected: %v", err)
	}
	for _, value := range []string{"", "short", strings.Repeat("z", 64)} {
		if err := ValidateTelemetryTokenSecret(value); err == nil {
			t.Fatalf("invalid secret %q accepted", value)
		}
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
