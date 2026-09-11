package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
)

func TestCapacityConsentSecretGenerationAndIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "consent.env")
	cmd := newClusterSecretsGenerateCapacityConsentCmd()
	cmd.SetArgs([]string{"--out", path})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("secret file is not private")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(payload), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[key] = value
		}
	}
	if len(values) != 2 || !strings.HasPrefix(values["CAPACITY_CONSENT_REVIEW_KEY_ID"], "capacity-consent-") {
		t.Fatal("unexpected generated signing tuple")
	}
	if _, err := mediaauthority.ParseSigningPrivateKey(values["CAPACITY_CONSENT_REVIEW_PRIVATE_KEY_PEM_B64"]); err != nil {
		t.Fatal("generated signer is invalid")
	}
	if err := cmd.Execute(); err == nil {
		t.Fatal("existing secret fragment overwritten")
	}
	for _, service := range []string{"quartermaster", "commodore", "foghorn", "purser", "bridge", "helmsman", "unknown"} {
		env := map[string]string{}
		for key, value := range values {
			env[key] = value
		}
		restrictClusterAccessSecrets(service, env)
		for key := range values {
			if (env[key] != "") != (service == "quartermaster") {
				t.Fatalf("review signing material distributed incorrectly to %s", service)
			}
		}
	}
}
