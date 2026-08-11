package gitops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseManifest_RejectsTypoedCompatibilityFields asserts a misspelled compatibility field is a hard error,
// not a silently dropped safety gate. A permissive decoder would ignore the unknown key and leave the real field zero
// (rollback enabled, no min-CLI floor, no required transition), removing the gate without any signal.
func TestParseManifest_RejectsTypoedCompatibilityFields(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{"misspelled rollback_disabled", "platform_version: v1.0.0\nrollback_disabld:\n  - chandler\n"},
		{"misspelled min_cli_version", "platform_version: v1.0.0\nmin_cli_verison: v1.0.0\n"},
		{"misspelled required_transitions", "platform_version: v1.0.0\nrequired_transition:\n  - storage-descriptor-adoption\n"},
		{"unknown top-level field", "platform_version: v1.0.0\nrollback_polcy: none\n"},
		{"malformed min_cli_version value", "platform_version: v1.0.0\nmin_cli_version: 1.0\n"},
		{"empty required transition id", "platform_version: v1.0.0\nrequired_transitions:\n  - \"\"\n"},
		{"empty rollback deploy name", "platform_version: v1.0.0\nrollback_disabled:\n  - \"\"\n"},
		// A valid-LOOKING rollback typo must be rejected: it would parse and then never match IsRollbackDisabled,
		// silently re-enabling rollback across a readiness-contract cut.
		{"unknown rollback target typo", "platform_version: v1.0.0\nrollback_disabled:\n  - chandlr\n"},
		// Surrounding whitespace is non-canonical: the trimmed lookup would pass but IsRollbackDisabled matches the
		// stored slice EXACTLY, so " chandler " would never match "chandler".
		{"rollback name with whitespace", "platform_version: v1.0.0\nrollback_disabled:\n  - \" chandler \"\n"},
		// Single-document contract: a trailing document (which could hide compatibility metadata) and empty input are
		// both rejected.
		{"trailing yaml document", "platform_version: v1.0.0\n---\nplatform_version: v2.0.0\n"},
		{"empty input", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := parseManifest([]byte(c.yaml)); err == nil {
				t.Fatalf("%s must be rejected, but parsed clean", c.name)
			}
		})
	}
}

// TestParseManifest_AcceptsValidManifest confirms a well-formed manifest with every compatibility field passes.
func TestParseManifest_AcceptsValidManifest(t *testing.T) {
	yamlText := "platform_version: v0.2.97\n" +
		"min_cli_version: v0.2.97-rc1\n" +
		"required_transitions:\n  - storage-descriptor-adoption\n" +
		"rollback_disabled:\n  - chandler\n" +
		"services: []\nnative_binaries: []\ninterfaces: []\ninfrastructure: []\n"
	m, err := parseManifest([]byte(yamlText))
	if err != nil {
		t.Fatalf("valid manifest must parse: %v", err)
	}
	if m.MinCLIVersion != "v0.2.97-rc1" || len(m.RequiredTransitions) != 1 || len(m.RollbackDisabled) != 1 {
		t.Fatalf("compatibility metadata lost: %+v", m)
	}
	// The low-level structural parser does not itself judge platform_version identity; that binding is enforced at the
	// FETCH layer by bindManifestIdentity, which requires a concrete platform_version and (for a pinned tag or a channel
	// pointer's declared version) an exact release match.
	if err := bindManifestIdentity(&Manifest{PlatformVersion: "cached"}, "latest"); err == nil {
		t.Fatal("a non-concrete platform_version must be rejected by identity binding")
	}
	if err := bindManifestIdentity(&Manifest{PlatformVersion: "v1.0.0"}, "v2.0.0"); err == nil {
		t.Fatal("a manifest whose platform_version does not match the requested release must be rejected")
	}
	if err := bindManifestIdentity(&Manifest{PlatformVersion: "v1.0.0"}, "v1.0.0"); err != nil {
		t.Fatalf("a matching concrete identity must bind: %v", err)
	}
}

// TestParseManifest_PreservesExternalDependencyUnknownFields confirms strict decoding does NOT break the intentional
// forward-compat escape hatch: an external dependency's unmodeled fields are absorbed by its inline map, not rejected.
func TestParseManifest_PreservesExternalDependencyUnknownFields(t *testing.T) {
	yamlText := "platform_version: v1.0.0\n" +
		"external_dependencies:\n" +
		"  - name: mistserver\n" +
		"    image: mist:latest\n" +
		"    some_future_field: preserved\n"
	m, err := parseManifest([]byte(yamlText))
	if err != nil {
		t.Fatalf("external dependency with unknown fields must still parse (inline preservation): %v", err)
	}
	if len(m.ExternalDependencies) != 1 {
		t.Fatalf("external dependency lost: %+v", m)
	}
	if got, ok := m.ExternalDependencies[0].Raw["some_future_field"]; !ok || !strings.Contains(strings.ToLower(strings.TrimSpace(toString(got))), "preserved") {
		t.Fatalf("unknown external-dependency field must be preserved in Raw, got %+v", m.ExternalDependencies[0].Raw)
	}
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// TestFetchLocal_PresentChannelPointerFailsClosed asserts that a channel pointer which EXISTS but is unusable aborts,
// rather than silently falling back to a directory scan that could deploy a release the channel never named. A release
// the scan WOULD pick is present precisely to prove the fallback does not run.
func TestFetchLocal_PresentChannelPointerFailsClosed(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		pointer string
	}{
		{"malformed pointer", "platform_version: v3.0.0\nmanifest: [broken\n"},
		{"pointer names no manifest", "platform_version: v3.0.0\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			mustMkdir(t, filepath.Join(dir, "channels"))
			mustMkdir(t, filepath.Join(dir, "releases"))
			mustWrite(t, filepath.Join(dir, "channels", "stable.yaml"), c.pointer)
			mustWrite(t, filepath.Join(dir, "releases", "v9.9.9.yaml"), "platform_version: v9.9.9\nservices: []\n")
			f, err := NewFetcher(FetchOptions{Repository: dir, CacheDir: t.TempDir()})
			if err != nil {
				t.Fatalf("NewFetcher: %v", err)
			}
			if _, err := f.fetchFromLocal("stable", "latest"); err == nil {
				t.Fatalf("%s must fail closed, not fall back to a directory scan", c.name)
			}
		})
	}
}

// TestFetchLocal_PointerAbsentFailsClosed asserts an absent channel pointer is a hard error, not a directory scan: a
// scan cannot honor the requested channel (a missing stable pointer could otherwise deploy an RC, and vice versa).
func TestFetchLocal_PointerAbsentFailsClosed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, "releases")) // no channels/ pointer
	mustWrite(t, filepath.Join(dir, "releases", "v0.2.9.yaml"), "platform_version: v0.2.9\nservices: []\n")
	mustWrite(t, filepath.Join(dir, "releases", "v0.2.10.yaml"), "platform_version: v0.2.10\nservices: []\n")
	f, err := NewFetcher(FetchOptions{Repository: dir, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewFetcher: %v", err)
	}
	if _, err := f.fetchFromLocal("stable", "latest"); err == nil {
		t.Fatal("an absent channel pointer must fail closed, not scan the releases directory")
	}
}

// TestFetchLocal_IdentityMismatchRejected asserts a fetched manifest is bound to its requested identity: a pinned tag
// whose file declares a different platform_version, and a channel pointer whose declared version disagrees with the
// referenced manifest, are both rejected — so one release's migrations cannot run against another's artifacts/policy.
func TestFetchLocal_IdentityMismatchRejected(t *testing.T) {
	t.Parallel()

	t.Run("pinned filename vs content mismatch", func(t *testing.T) {
		dir := t.TempDir()
		mustMkdir(t, filepath.Join(dir, "releases"))
		// File named v0.2.10 but content declares v0.2.9.
		mustWrite(t, filepath.Join(dir, "releases", "v0.2.10.yaml"), "platform_version: v0.2.9\nservices: []\n")
		f, _ := NewFetcher(FetchOptions{Repository: dir, CacheDir: t.TempDir()})
		if _, err := f.fetchFromLocal("stable", "v0.2.10"); err == nil {
			t.Fatal("a pinned manifest whose content version differs from its filename must be rejected")
		}
	})

	t.Run("channel pointer vs manifest mismatch", func(t *testing.T) {
		dir := t.TempDir()
		mustMkdir(t, filepath.Join(dir, "channels"))
		mustMkdir(t, filepath.Join(dir, "releases"))
		// Pointer claims v3.0.0 but the referenced manifest declares v2.0.0.
		mustWrite(t, filepath.Join(dir, "channels", "stable.yaml"), "platform_version: v3.0.0\nmanifest: releases/v3.0.0.yaml\n")
		mustWrite(t, filepath.Join(dir, "releases", "v3.0.0.yaml"), "platform_version: v2.0.0\nservices: []\n")
		f, _ := NewFetcher(FetchOptions{Repository: dir, CacheDir: t.TempDir()})
		if _, err := f.fetchFromLocal("stable", "latest"); err == nil {
			t.Fatal("a channel pointer whose declared version disagrees with the referenced manifest must be rejected")
		}
	})

	t.Run("non-concrete platform_version rejected", func(t *testing.T) {
		dir := t.TempDir()
		mustMkdir(t, filepath.Join(dir, "releases"))
		mustWrite(t, filepath.Join(dir, "releases", "v1.0.0.yaml"), "platform_version: cached\nservices: []\n")
		f, _ := NewFetcher(FetchOptions{Repository: dir, CacheDir: t.TempDir()})
		if _, err := f.fetchFromLocal("stable", "v1.0.0"); err == nil {
			t.Fatal("a non-concrete platform_version must be rejected at fetch")
		}
	})
}

// TestParseChannelPointer_Strict pins the strict single-document pointer parse: a misspelled or absent
// platform_version, a missing manifest path, and trailing documents are all rejected — so a channel pointer can never
// silently lose its version and let bindManifestIdentity accept any concrete manifest.
func TestParseChannelPointer_Strict(t *testing.T) {
	t.Parallel()
	bad := map[string]string{
		"misspelled platform_version": "platform_verison: v1.0.0\nmanifest: releases/v1.0.0.yaml\n",
		"missing platform_version":    "manifest: releases/v1.0.0.yaml\n",
		"non-concrete version":        "platform_version: cached\nmanifest: releases/v1.0.0.yaml\n",
		"missing manifest path":       "platform_version: v1.0.0\n",
		"unknown field":               "platform_version: v1.0.0\nmanifest: releases/v1.0.0.yaml\nchannl: stable\n",
		"trailing document":           "platform_version: v1.0.0\nmanifest: releases/v1.0.0.yaml\n---\nplatform_version: v2.0.0\n",
		"empty":                       "",
	}
	for name, y := range bad {
		if _, err := parseChannelPointer([]byte(y)); err == nil {
			t.Errorf("%s must be rejected", name)
		}
	}
	if p, err := parseChannelPointer([]byte("platform_version: v3.0.0\nmanifest: releases/v3.0.0.yaml\nupdated_at: 2026-01-01T00:00:00Z\n")); err != nil {
		t.Fatalf("valid pointer must parse: %v", err)
	} else if p.PlatformVersion != "v3.0.0" || p.Manifest != "releases/v3.0.0.yaml" {
		t.Fatalf("pointer fields lost: %+v", p)
	}
}

// TestBindManifestIdentity_ExactNotPrecedence pins that identity binding is byte-exact, not SemVer precedence:
// precedence ignores build metadata, so +buildB must NOT bind to a requested +buildA.
func TestBindManifestIdentity_ExactNotPrecedence(t *testing.T) {
	t.Parallel()
	if err := bindManifestIdentity(&Manifest{PlatformVersion: "v1.2.3+buildB"}, "v1.2.3+buildA"); err == nil {
		t.Fatal("build metadata is part of release identity; +buildB must not bind to requested +buildA")
	}
	if err := bindManifestIdentity(&Manifest{PlatformVersion: "v1.2.3+buildA"}, "v1.2.3+buildA"); err != nil {
		t.Fatalf("identical build metadata must bind: %v", err)
	}
}

// TestFetchLocal_TypoedChannelPointerRejected asserts the strict pointer parse fails closed through the real fetch
// path, rather than producing an empty version that would bind to any concrete manifest.
func TestFetchLocal_TypoedChannelPointerRejected(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, "channels"))
	mustMkdir(t, filepath.Join(dir, "releases"))
	mustWrite(t, filepath.Join(dir, "channels", "stable.yaml"), "platform_verison: v9.9.9\nmanifest: releases/v9.9.9.yaml\n")
	mustWrite(t, filepath.Join(dir, "releases", "v9.9.9.yaml"), "platform_version: v9.9.9\nservices: []\n")
	f, _ := NewFetcher(FetchOptions{Repository: dir, CacheDir: t.TempDir()})
	if _, err := f.fetchFromLocal("stable", "latest"); err == nil {
		t.Fatal("a channel pointer with a misspelled platform_version must fail closed")
	}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
