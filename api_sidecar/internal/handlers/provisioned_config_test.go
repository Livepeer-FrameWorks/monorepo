package handlers

import (
	"os"
	"path/filepath"
	"testing"

	"frameworks/api_sidecar/internal/appconfig/appconfigtest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestReadProvisionedConfig(t *testing.T) {
	dir := t.TempDir()
	if got := readProvisionedConfig(dir); got != nil {
		t.Fatalf("missing marker = %v, want nil", got)
	}
	if err := os.WriteFile(filepath.Join(dir, ProvisionedConfigFile), []byte("# comment\nOTHER=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readProvisionedConfig(dir); got != nil {
		t.Fatalf("marker without fields = %v, want nil", got)
	}
	if err := os.WriteFile(filepath.Join(dir, ProvisionedConfigFile), []byte("EDGE_CONFIG_CLI_VERSION=v0.3.11\nEDGE_CONFIG_DIGEST= sha256:abc \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := readProvisionedConfig(dir)
	if got.GetCliVersion() != "v0.3.11" || got.GetDigest() != "sha256:abc" {
		t.Fatalf("marker = %v, want cli v0.3.11 digest sha256:abc", got)
	}
	if got := readProvisionedConfig(""); got != nil {
		t.Fatalf("empty state dir = %v, want nil", got)
	}
}

// The lifecycle report Helmsman sends Foghorn carries the marker from the
// configured state dir, re-read on every report.
func TestNodeLifecycleUpdateReportsProvisionedConfig(t *testing.T) {
	dir := t.TempDir()
	appconfigtest.Setenv(t, "HELMSMAN_STATE_DIR", dir)
	pm := newStreamWSTestMonitor(t)

	report := func() (string, string, bool) {
		nlu := pm.convertNodeAPIToMistTrigger("node-a", map[string]any{"cpu": float64(1)}, logging.NewLogger()).GetNodeLifecycleUpdate()
		pc := nlu.GetProvisionedConfig()
		return pc.GetCliVersion(), pc.GetDigest(), pc != nil
	}
	if _, _, present := report(); present {
		t.Fatal("node without a marker must not report a provisioned config")
	}
	if err := os.WriteFile(filepath.Join(dir, ProvisionedConfigFile), []byte("EDGE_CONFIG_CLI_VERSION=v0.3.12\nEDGE_CONFIG_DIGEST=sha256:def\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if version, digest, present := report(); !present || version != "v0.3.12" || digest != "sha256:def" {
		t.Fatalf("reported provisioned config = %q/%q (present=%v), want v0.3.12/sha256:def", version, digest, present)
	}
}
