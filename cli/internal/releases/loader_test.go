package releases

import (
	"testing"
)

func TestShippedCatalogHasDatabaseOwnershipButNoReleaseRequirements(t *testing.T) {
	if got := Catalog(); len(got) != 0 {
		t.Errorf("embedded catalog must ship empty; got %d release(s)", len(got))
	}
	if got := ServiceDatabase("purser"); got != "purser" {
		t.Errorf("purser database ownership = %q, want purser", got)
	}
	if db, ok := ServiceDatabaseLookup("navigator"); ok || db != "" {
		t.Errorf("navigator database ownership = (%q, %v), want empty/false", db, ok)
	}
	if got := Lookup("v0.5.0"); got != nil {
		t.Errorf("empty release list Lookup must return nil; got %+v", got)
	}
}

func TestLoadErrorIsNilForShippedCatalog(t *testing.T) {
	if err := LoadError(); err != nil {
		t.Fatalf("shipped catalog must parse cleanly: %v", err)
	}
}

func TestPath_EmptyCatalog(t *testing.T) {
	got, err := Path("v0.4.0", "v0.5.0")
	if err != nil {
		t.Fatalf("empty-catalog Path must not error; got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty-catalog Path must return empty; got %v", got)
	}
}

func TestPath_RejectsEmptyEndpoints(t *testing.T) {
	got, err := Path("", "v0.5.0")
	if err != nil || got != nil {
		t.Errorf("Path with empty from must return (nil, nil); got %v, %v", got, err)
	}
	got, err = Path("v0.4.0", "")
	if err != nil || got != nil {
		t.Errorf("Path with empty to must return (nil, nil); got %v, %v", got, err)
	}
}

func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v0.4.0", "v0.5.0", -1},
		{"v0.5.0", "v0.4.0", 1},
		{"v1.2.3", "v1.2.3", 0},
		{"v1.2.3-rc1", "v1.2.3", -1},
		{"v1.2.3", "v1.2.3-rc1", 1},
		{"v1.2.3-rc1", "v1.2.3-rc2", -1},
		{"v0.10.0", "v0.9.0", 1},
		{"v0.0.1", "v0.1.0", -1},
	}
	for _, c := range cases {
		if got := CompareSemver(c.a, c.b); got != c.want {
			t.Errorf("CompareSemver(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestPathWithFixtureCatalog(t *testing.T) {
	// Use a synthetic catalog to validate Path's filter and refusal logic.
	saved := catalogData
	t.Cleanup(func() { catalogData = saved })

	catalogData = catalogFile{
		Releases: []Release{
			{Version: "v0.3.0"},
			{Version: "v0.4.0", CompatibleFrom: "v0.3.0"},
			{Version: "v0.5.0", CompatibleFrom: "v0.4.0"},
			{Version: "v0.6.0", CompatibleFrom: "v0.5.0", RequiresIntermediate: []string{"v0.5.0"}},
		},
	}

	t.Run("from inside compatible window", func(t *testing.T) {
		got, err := Path("v0.4.0", "v0.5.0")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 || got[0].Version != "v0.5.0" {
			t.Errorf("got %+v, want only v0.5.0", got)
		}
	})

	t.Run("skipping a compatible_from refuses", func(t *testing.T) {
		// v0.5.0.compatible_from = v0.4.0, so v0.3.0 -> v0.5.0 is refused
		// — operator must transit v0.4.0 first.
		_, err := Path("v0.3.0", "v0.5.0")
		if err == nil {
			t.Fatal("want error for direct skip past compatible_from, got nil")
		}
	})

	t.Run("compatible_from refusal", func(t *testing.T) {
		_, err := Path("v0.3.0", "v0.6.0")
		if err == nil {
			t.Fatal("want error for direct skip past compatible_from, got nil")
		}
	})

	t.Run("unknown target in non-empty catalog refuses", func(t *testing.T) {
		_, err := Path("v0.4.0", "v0.9.0")
		if err == nil {
			t.Fatal("want error for target missing from non-empty catalog, got nil")
		}
	})
}
