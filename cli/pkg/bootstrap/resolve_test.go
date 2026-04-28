package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultResolverEnv(t *testing.T) {
	t.Setenv("BOOTSTRAP_TEST_PASSWORD", "hunter2")
	r := &DefaultResolver{}
	got, err := r.Resolve(SecretRef{Env: "BOOTSTRAP_TEST_PASSWORD"})
	if err != nil {
		t.Fatalf("Resolve env: %v", err)
	}
	if got != "hunter2" {
		t.Fatalf("got %q, want hunter2", got)
	}

	if _, err := r.Resolve(SecretRef{Env: "BOOTSTRAP_TEST_MISSING"}); err == nil {
		t.Fatal("expected error for unset env var")
	}
}

func TestDefaultResolverFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pw.txt")
	if err := os.WriteFile(path, []byte("file-secret\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	r := &DefaultResolver{}
	got, err := r.Resolve(SecretRef{File: path})
	if err != nil {
		t.Fatalf("Resolve file: %v", err)
	}
	if got != "file-secret" {
		t.Fatalf("got %q, want file-secret (trimmed)", got)
	}
}

func TestDefaultResolverFlag(t *testing.T) {
	r := &DefaultResolver{Flags: map[string]string{"bootstrap-admin-password": "from-flag"}}
	got, err := r.Resolve(SecretRef{Flag: "bootstrap-admin-password"})
	if err != nil {
		t.Fatalf("Resolve flag: %v", err)
	}
	if got != "from-flag" {
		t.Fatalf("got %q, want from-flag", got)
	}

	if _, err := r.Resolve(SecretRef{Flag: "missing"}); err == nil {
		t.Fatal("expected error for unprovided flag")
	}
}

func TestDefaultResolverRejectsMultipleShapes(t *testing.T) {
	r := &DefaultResolver{}
	_, err := r.Resolve(SecretRef{Env: "X", File: "/y"})
	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("expected exactly-one-of error; got %v", err)
	}
}

func TestDefaultResolverRejectsZero(t *testing.T) {
	r := &DefaultResolver{}
	if _, err := r.Resolve(SecretRef{}); err == nil {
		t.Fatal("expected error for empty SecretRef")
	}
}

// TestLookupKeyEnvHandlesQuotes exercises edge cases the previous string-splitting
// parser handled correctly. Comments and quoted values still work.
func TestLookupKeyEnvHandlesQuotes(t *testing.T) {
	data := []byte("# comment\nKEY=\"quoted value\"\nOTHER=plain\n")
	got, ok := lookupKey(data, "env", "KEY")
	if !ok || got != "quoted value" {
		t.Fatalf("env quoted lookup: %q ok=%v", got, ok)
	}
}

// TestLookupKeyYAMLProperParser verifies the yaml.v3 path handles real YAML — quoted
// values, comments mid-document, integer/bool scalars — that the old string-splitting
// parser would have mangled.
func TestLookupKeyYAMLProperParser(t *testing.T) {
	data := []byte(`
# preamble
PASSWORD: "value:with:colons"
TOKEN: hunter2
COUNT: 42
ACTIVE: true
`)
	cases := []struct {
		key, want string
	}{
		{"PASSWORD", "value:with:colons"},
		{"TOKEN", "hunter2"},
		{"COUNT", "42"},
		{"ACTIVE", "true"},
	}
	for _, tc := range cases {
		got, ok := lookupKey(data, "yaml", tc.key)
		if !ok || got != tc.want {
			t.Errorf("yaml %q: got %q ok=%v, want %q", tc.key, got, ok, tc.want)
		}
	}

	if _, ok := lookupKey(data, "yaml", "missing"); ok {
		t.Error("missing key should return ok=false")
	}
}

// TestLookupKeyYAMLRejectsNested confirms the parser refuses nested structures
// rather than guessing — secrets are flat scalars only.
func TestLookupKeyYAMLRejectsNested(t *testing.T) {
	data := []byte(`
GROUP:
  KEY: value
`)
	if _, ok := lookupKey(data, "yaml", "GROUP"); ok {
		t.Error("nested object should not be returned as a string secret")
	}
}

// TestRenderResolvesAccountPassword pins the layer-5 contract: a Rendered carrying any
// account user must have a non-empty Password if the source carried a PasswordRef.
// Without a resolver, render fails; with one, the password lands in the Rendered.
func TestRenderResolvesAccountPassword(t *testing.T) {
	d, err := Derive(minimalManifest(), DeriveOptions{})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}

	overlay := &Overlay{
		Accounts: []AccountDerived{
			{
				Kind:   AccountSystemOperator,
				Tenant: TenantRef{Ref: "quartermaster.system_tenant"},
				Users: []AccountUserDerived{
					{
						AccountUserCommon: AccountUserCommon{Email: "ops@example.com", Role: "owner"},
						PasswordRef:       SecretRef{Flag: "bootstrap-admin-password"},
					},
				},
				Billing: AccountBilling{Mode: "none"},
			},
		},
	}

	// Without resolver: render must fail rather than emit empty Password.
	if _, rerr := Render(d, overlay, nil); rerr == nil {
		t.Fatal("expected Render to fail when accounts carry password_ref but no resolver supplied")
	}

	resolver := ResolverFunc(func(ref SecretRef) (string, error) {
		if ref.Flag == "bootstrap-admin-password" {
			return "rendered-secret", nil
		}
		return "", nil
	})
	r, err := Render(d, overlay, resolver)
	if err != nil {
		t.Fatalf("Render with resolver: %v", err)
	}
	if got := len(r.Accounts); got != 1 {
		t.Fatalf("expected 1 account; got %d", got)
	}
	if got := r.Accounts[0].Users[0].Password; got != "rendered-secret" {
		t.Fatalf("password = %q, want rendered-secret", got)
	}
}
