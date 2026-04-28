package bootstrap

import (
	"os"
	"path/filepath"
	"testing"

	"frameworks/cli/pkg/inventory"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestLoadOverlayValid(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "bootstrap.yaml", `
quartermaster:
  tenants:
    - alias: northwind
      name: Northwind Traders
purser:
  cluster_pricing:
    - cluster_id: northwind-private
      pricing_model: flat
`)
	o, err := LoadOverlay(path)
	if err != nil {
		t.Fatalf("LoadOverlay: %v", err)
	}
	if got := len(o.Quartermaster.Tenants); got != 1 {
		t.Fatalf("expected 1 overlay tenant; got %d", got)
	}
	if got := len(o.Purser.ClusterPricing); got != 1 {
		t.Fatalf("expected 1 overlay cluster_pricing; got %d", got)
	}
}

func TestLoadOverlayEmptyPath(t *testing.T) {
	o, err := LoadOverlay("")
	if err != nil {
		t.Fatalf("expected nil error for empty path; got %v", err)
	}
	if o != nil {
		t.Fatal("expected nil overlay for empty path")
	}
}

func TestLoadOverlayRejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "bootstrap.yaml", `
quartermaster:
  not_a_real_field: oops
`)
	_, err := LoadOverlay(path)
	if err == nil {
		t.Fatal("expected error on unknown field")
	}
}

func TestRenderFromManifestUsesBootstrapOverlay(t *testing.T) {
	dir := t.TempDir()
	overlayPath := "bootstrap.yaml"
	writeFile(t, dir, overlayPath, `
quartermaster:
  tenants:
    - alias: northwind
      name: Northwind
`)

	m := minimalManifest()
	m.BootstrapOverlay = overlayPath
	r, err := RenderFromManifest(m, dir, DeriveOptions{}, nil)
	if err != nil {
		t.Fatalf("RenderFromManifest: %v", err)
	}
	if got := len(r.Quartermaster.Tenants); got != 1 || r.Quartermaster.Tenants[0].Alias != "northwind" {
		t.Fatalf("overlay tenant not applied: %+v", r.Quartermaster.Tenants)
	}
}

func TestRenderFromManifestNoOverlay(t *testing.T) {
	m := minimalManifest()
	m.BootstrapOverlay = ""
	r, err := RenderFromManifest(m, "", DeriveOptions{}, nil)
	if err != nil {
		t.Fatalf("RenderFromManifest: %v", err)
	}
	if r.Quartermaster.SystemTenant == nil {
		t.Fatal("expected derived system_tenant even without overlay")
	}
}

func TestRenderFromManifestBootstrapAdminAccount(t *testing.T) {
	m := minimalManifest()
	resolver := ResolverFunc(func(ref SecretRef) (string, error) {
		if ref.Flag == "bootstrap-admin-password" {
			return "ops-password", nil
		}
		return "", nil
	})
	opts := DeriveOptions{
		BootstrapAdmin: &BootstrapAdminSpec{
			Email:       "ops@example.com",
			Role:        "owner",
			PasswordRef: SecretRef{Flag: "bootstrap-admin-password"},
		},
	}
	r, err := RenderFromManifest(m, "", opts, resolver)
	if err != nil {
		t.Fatalf("RenderFromManifest: %v", err)
	}
	if got := len(r.Accounts); got != 1 {
		t.Fatalf("expected 1 account from bootstrap-admin; got %d", got)
	}
	if r.Accounts[0].Kind != AccountSystemOperator {
		t.Fatalf("expected system_operator kind; got %q", r.Accounts[0].Kind)
	}
	if r.Accounts[0].Users[0].Password != "ops-password" {
		t.Fatalf("password not resolved into rendered: %+v", r.Accounts[0].Users[0])
	}
}

// silenceUnused keeps the inventory import alive when other tests change.
var _ = inventory.Manifest{}
