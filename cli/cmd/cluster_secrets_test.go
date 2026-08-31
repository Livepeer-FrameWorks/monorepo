package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"frameworks/cli/pkg/credentials"
)

func TestClusterSecretsGenerateMediaAuthorityWritesExclusivePrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "authority.env")
	cmd := newClusterSecretsGenerateMediaAuthorityCmd()
	cmd.SetArgs([]string{"--out", path})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"MEDIA_AUTHORITY_SIGNING_KEY_ID=",
		"MEDIA_AUTHORITY_SIGNING_PRIVATE_KEY_PEM_B64=",
		"MEDIA_AUTHORITY_TRUST_SET=",
		"MEDIA_AUTHORITY_SEAL_ROOT_SECRET=",
		"FOGHORN_STATE_ENCRYPTION_KEY=",
	} {
		if !strings.Contains(string(payload), key) {
			t.Fatalf("generated fragment is missing %s", key)
		}
	}
	if err := cmd.Execute(); err == nil {
		t.Fatal("generator overwrote an existing secret fragment")
	}
}

func TestClusterSecretsGenerateSharedPassesProductionValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.env")
	cmd := newClusterSecretsGenerateSharedCmd()
	cmd.SetArgs([]string{"--out", path})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	values := make(map[string]string)
	for _, line := range strings.Split(string(payload), "\n") {
		if strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		key, value, _ := strings.Cut(line, "=")
		values[key] = value
	}
	if err := credentials.ValidateShared(values); err != nil {
		t.Fatalf("generated shared fragment fails production validation: %v", err)
	}
}
