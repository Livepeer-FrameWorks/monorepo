package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// applyCABundle installs the gRPC CA trust anchor atomically and idempotently:
// an empty bundle is a no-op, a new/changed bundle is written and reports a
// change, and re-applying an identical bundle reports no change (no churn).
func TestApplyCABundle(t *testing.T) {
	caPath := filepath.Join(t.TempDir(), "pki", "ca.crt")
	t.Setenv("GRPC_TLS_CA_PATH", caPath)
	m := &Manager{logger: logging.NewLogger()}

	t.Run("empty bundle is a no-op", func(t *testing.T) {
		if m.applyCABundle(nil) {
			t.Fatal("empty bundle must report no change")
		}
		if _, err := os.Stat(caPath); !os.IsNotExist(err) {
			t.Fatal("no file should be written for an empty bundle")
		}
	})

	bundle := []byte("-----BEGIN CERTIFICATE-----\nFAKE\n-----END CERTIFICATE-----\n")

	t.Run("new bundle is installed", func(t *testing.T) {
		if !m.applyCABundle(bundle) {
			t.Fatal("first apply must report a change")
		}
		got, err := os.ReadFile(caPath)
		if err != nil {
			t.Fatalf("read installed CA: %v", err)
		}
		if string(got) != string(bundle) {
			t.Fatalf("installed CA = %q, want %q", got, bundle)
		}
	})

	t.Run("identical bundle is idempotent", func(t *testing.T) {
		if m.applyCABundle(bundle) {
			t.Fatal("re-applying the same bundle must report no change")
		}
	})

	t.Run("changed bundle is reinstalled", func(t *testing.T) {
		next := []byte("-----BEGIN CERTIFICATE-----\nROTATED\n-----END CERTIFICATE-----\n")
		if !m.applyCABundle(next) {
			t.Fatal("a different bundle must report a change")
		}
		got, _ := os.ReadFile(caPath)
		if string(got) != string(next) {
			t.Fatalf("CA after rotation = %q, want %q", got, next)
		}
	})
}
