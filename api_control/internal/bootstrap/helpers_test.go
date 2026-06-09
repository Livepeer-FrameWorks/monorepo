package bootstrap

import (
	"reflect"
	"strings"
	"testing"
)

func validAccount() Account {
	return Account{
		Kind:   AccountSystemOperator,
		Tenant: TenantRef{Ref: "quartermaster.system_tenant"},
		Users:  []AccountUser{{Email: "op@example.com", Role: "owner", Password: "pw"}},
	}
}

func validPullStream() PullStream {
	return PullStream{
		PlaybackID:  "pb1",
		OwnerTenant: TenantRef{Ref: "quartermaster.tenants[acme]"},
		Title:       "Pull One",
		SourceURI:   "https://cdn.example.com/live.m3u8",
	}
}

func validMistNativeStream() MistNativeStream {
	return MistNativeStream{
		PlaybackID:        "mp1",
		OwnerTenant:       TenantRef{Ref: "quartermaster.system_tenant"},
		Title:             "Mist One",
		Source:            "ts-exec:ffmpeg -i x",
		SourceKind:        "exec",
		AllowedClusterIDs: []string{"cluster-1"},
	}
}

// Check is the offline --check pass: it must accept a fully valid desired state
// and surface the FIRST validation failure from any section. These tests pin
// that it actually descends into accounts, pull streams, and mist streams.
func TestCheck(t *testing.T) {
	t.Run("accepts_valid_desired_state", func(t *testing.T) {
		ds := DesiredState{
			Accounts: []Account{validAccount()},
			Commodore: CommodoreSection{
				PullStreams:       []PullStream{validPullStream()},
				MistNativeStreams: []MistNativeStream{validMistNativeStream()},
			},
		}
		if err := Check(ds); err != nil {
			t.Fatalf("Check(valid) = %v, want nil", err)
		}
	})

	t.Run("empty_is_valid", func(t *testing.T) {
		if err := Check(DesiredState{}); err != nil {
			t.Fatalf("Check(empty) = %v, want nil", err)
		}
	})

	t.Run("rejects_bad_account_kind", func(t *testing.T) {
		acc := validAccount()
		acc.Kind = "wizard"
		if err := Check(DesiredState{Accounts: []Account{acc}}); err == nil {
			t.Fatal("Check accepted an invalid account kind")
		}
	})

	t.Run("rejects_bad_user_role", func(t *testing.T) {
		acc := validAccount()
		acc.Users[0].Role = "superuser"
		err := Check(DesiredState{Accounts: []Account{acc}})
		if err == nil || !strings.Contains(err.Error(), acc.Tenant.Ref) {
			t.Fatalf("Check error = %v, want one mentioning the tenant ref", err)
		}
	})

	t.Run("rejects_malformed_tenant_ref", func(t *testing.T) {
		acc := validAccount()
		acc.Tenant.Ref = "not-a-ref"
		if err := Check(DesiredState{Accounts: []Account{acc}}); err == nil {
			t.Fatal("Check accepted a malformed tenant ref")
		}
	})

	t.Run("rejects_blocked_pull_source", func(t *testing.T) {
		ps := validPullStream()
		ps.SourceURI = "" // empty fails shape validation
		ds := DesiredState{Commodore: CommodoreSection{PullStreams: []PullStream{ps}}}
		if err := Check(ds); err == nil {
			t.Fatal("Check accepted a pull stream with no source_uri")
		}
	})

	t.Run("rejects_customer_owned_mist_native_stream", func(t *testing.T) {
		ms := validMistNativeStream()
		ms.OwnerTenant.Ref = "quartermaster.tenants[acme]" // not the system tenant
		ds := DesiredState{Commodore: CommodoreSection{MistNativeStreams: []MistNativeStream{ms}}}
		if err := Check(ds); err == nil {
			t.Fatal("Check accepted a customer-owned mist_native stream")
		}
	})
}

func TestDefaultPermissions(t *testing.T) {
	cases := map[string][]string{
		"owner":   {"read", "write", "admin"},
		"admin":   {"read", "write", "admin"},
		"member":  {"read", "write"},
		"":        {"read"},
		"unknown": {"read"},
	}
	for role, want := range cases {
		if got := defaultPermissions(role); !reflect.DeepEqual(got, want) {
			t.Errorf("defaultPermissions(%q) = %v, want %v", role, got, want)
		}
	}
}

func TestStringSliceEq(t *testing.T) {
	if !stringSliceEq(nil, nil) {
		t.Error("nil == nil should be true")
	}
	if !stringSliceEq([]string{"a", "b"}, []string{"a", "b"}) {
		t.Error("equal slices should be true")
	}
	if stringSliceEq([]string{"a"}, []string{"a", "b"}) {
		t.Error("length mismatch should be false")
	}
	if stringSliceEq([]string{"a", "b"}, []string{"a", "c"}) {
		t.Error("element mismatch should be false")
	}
}

func TestAliasFromRefEdgeCases(t *testing.T) {
	ok := map[string]string{
		"quartermaster.system_tenant":  "frameworks",
		"quartermaster.tenants[acme]":  "acme",
		"quartermaster.tenants[a-b_c]": "a-b_c",
		"quartermaster.tenants[]":      "", // empty brackets parse to an empty alias, no error
	}
	for ref, want := range ok {
		got, err := AliasFromRef(ref)
		if err != nil {
			t.Errorf("AliasFromRef(%q) unexpected error: %v", ref, err)
			continue
		}
		if got != want {
			t.Errorf("AliasFromRef(%q) = %q, want %q", ref, got, want)
		}
	}

	for _, bad := range []string{"", "frameworks", "quartermaster.tenants[acme", "quartermaster.tenant[acme]"} {
		if _, err := AliasFromRef(bad); err == nil {
			t.Errorf("AliasFromRef(%q) = nil error, want malformed-ref error", bad)
		}
	}
}

func TestNormalizeAllowedClusterIDs(t *testing.T) {
	if got := normalizeAllowedClusterIDs(nil); got != nil {
		t.Errorf("nil input = %v, want nil", got)
	}
	got := normalizeAllowedClusterIDs([]string{" b ", "a", "a", "", "  ", "b"})
	want := []string{"a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("normalizeAllowedClusterIDs = %v, want %v (deduped, trimmed, sorted)", got, want)
	}
}

func TestIsSystemTenantRef(t *testing.T) {
	yes := []string{
		"quartermaster.system_tenant",
		"quartermaster.tenants.frameworks",
		"  quartermaster.system_tenant  ", // trimmed
	}
	for _, ref := range yes {
		if !isSystemTenantRef(ref) {
			t.Errorf("isSystemTenantRef(%q) = false, want true", ref)
		}
	}
	no := []string{"", "quartermaster.tenants[acme]", "quartermaster.tenants.acme", "system_tenant"}
	for _, ref := range no {
		if isSystemTenantRef(ref) {
			t.Errorf("isSystemTenantRef(%q) = true, want false", ref)
		}
	}
}

// jsonStringsEqual backs the reconciler's idempotent compare: it must treat
// whitespace-different and null/empty-equivalent JSON as equal, and reject
// genuinely different or unparseable values.
func TestJSONStringsEqual(t *testing.T) {
	equal := [][2]string{
		{`{"a":1}`, `{"a":1}`},
		{`{"a":1}`, ` { "a" : 1 } `},
		{"", "null"},
		{"", ""},
		{"[1,2]", " [1, 2] "},
	}
	for _, p := range equal {
		if !jsonStringsEqual(p[0], p[1]) {
			t.Errorf("jsonStringsEqual(%q, %q) = false, want true", p[0], p[1])
		}
	}
	notEqual := [][2]string{
		{`{"a":1}`, `{"a":2}`},
		{`{"a":1}`, `{"b":1}`},
		{"{invalid", "{}"},
	}
	for _, p := range notEqual {
		if jsonStringsEqual(p[0], p[1]) {
			t.Errorf("jsonStringsEqual(%q, %q) = true, want false", p[0], p[1])
		}
	}
}

func TestOrEmptyJSONNull(t *testing.T) {
	if got := orEmptyJSONNull(""); got != "null" {
		t.Errorf("orEmptyJSONNull(empty) = %q, want null", got)
	}
	if got := orEmptyJSONNull(`{"a":1}`); got != `{"a":1}` {
		t.Errorf("orEmptyJSONNull(non-empty) = %q, want passthrough", got)
	}
}

func TestEncodeLocalAssetPaths(t *testing.T) {
	if got, err := encodeLocalAssetPaths(nil); err != nil || got != "[]" {
		t.Errorf("empty = (%q, %v), want ([], nil)", got, err)
	}
	got, err := encodeLocalAssetPaths([]MistNativeStreamAsset{{Path: "/srv/a.ts", Sha256: "abc"}})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(got, "/srv/a.ts") || !strings.HasPrefix(got, "[") {
		t.Errorf("encoded = %q, want a JSON array containing the path", got)
	}
}
