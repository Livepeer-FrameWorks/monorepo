package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testCatalog is a small, self-contained catalog so these tests assert the
// resolution/precedence contract rather than the contents of catalog.yaml.
func testCatalog() Catalog {
	spec := func(name, role string) ServiceSpec {
		return ServiceSpec{Name: name, Role: role, Image: "img/" + name + ":latest"}
	}
	return Catalog{
		Profiles: map[string][]string{
			"central-all": {"bridge", "commodore", "grafana"},
			"control":     {"bridge", "commodore"},
			"media":       {"foghorn"},
		},
		Services: map[string]ServiceSpec{
			"bridge":    spec("bridge", "control"),
			"commodore": spec("commodore", "control"),
			"foghorn":   spec("foghorn", "media"),
			"grafana":   spec("grafana", "observability"),
		},
	}
}

func names(specs []ServiceSpec) []string {
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.Name)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestResolveSelection(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		profile    string
		includeCSV string
		excludeCSV string
		want       []string // sorted service names
		wantErr    bool
	}{
		{name: "profile only", profile: "control", want: []string{"bridge", "commodore"}},
		{name: "include adds to profile", profile: "media", includeCSV: "bridge", want: []string{"bridge", "foghorn"}},
		{name: "exclude removes profile member", profile: "control", excludeCSV: "bridge", want: []string{"commodore"}},
		{name: "include then exclude same", includeCSV: "bridge,commodore", excludeCSV: "commodore", want: []string{"bridge"}},
		{name: "empty falls back to central-all", want: []string{"bridge", "commodore", "grafana"}},
		{name: "output sorted and deduped", includeCSV: "grafana,bridge,bridge", want: []string{"bridge", "grafana"}},
		{name: "unknown profile errors", profile: "does-not-exist", wantErr: true},
		{name: "selected service missing from catalog errors", includeCSV: "ghost", wantErr: true},
	}
	cat := testCatalog()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ResolveSelection(cat, tc.profile, tc.includeCSV, tc.excludeCSV)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (result=%v)", names(got))
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !equalStrings(names(got), tc.want) {
				t.Fatalf("got %v, want %v", names(got), tc.want)
			}
		})
	}
}

func TestSplitCSV(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{name: "empty", in: "", want: []string{}},
		{name: "single", in: "a", want: []string{"a"}},
		{name: "trims and drops empties", in: " a , ,b ,", want: []string{"a", "b"}},
		{name: "preserves order", in: "c,a,b", want: []string{"c", "a", "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := splitCSV(tc.in)
			if !equalStrings(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestResolveServiceList(t *testing.T) {
	t.Parallel()

	t.Run("explicit short-circuits", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		// A plan file exists but explicit must win.
		if err := SavePlan(dir, []ServiceSpec{{Name: "zzz"}}, ""); err != nil {
			t.Fatalf("SavePlan: %v", err)
		}
		got, err := ResolveServiceList(dir, []string{"a", "b"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !equalStrings(got, []string{"a", "b"}) {
			t.Fatalf("got %v, want [a b]", got)
		}
	})

	t.Run("plan honored when no explicit", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := SavePlan(dir, []ServiceSpec{{Name: "commodore"}, {Name: "bridge"}}, "control"); err != nil {
			t.Fatalf("SavePlan: %v", err)
		}
		got, err := ResolveServiceList(dir, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// SavePlan sorts the stored names.
		if !equalStrings(got, []string{"bridge", "commodore"}) {
			t.Fatalf("got %v, want [bridge commodore]", got)
		}
	})

	t.Run("fragment scan ignores non svc files", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		for _, f := range []string{"svc-bridge.yml", "svc-commodore.yml", "compose.yml", "svc-.yml", "readme.md"} {
			if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
				t.Fatalf("write %s: %v", f, err)
			}
		}
		got, err := ResolveServiceList(dir, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !equalStrings(got, []string{"bridge", "commodore"}) {
			t.Fatalf("got %v, want [bridge commodore]", got)
		}
	})

	t.Run("empty dir yields empty", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		got, err := ResolveServiceList(dir, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("got %v, want empty", got)
		}
	})
}

func TestSavePlanLoadPlanRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	specs := []ServiceSpec{{Name: "foghorn"}, {Name: "bridge"}, {Name: "commodore"}}
	if err := SavePlan(dir, specs, "media"); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}
	p, err := LoadPlan(dir)
	if err != nil {
		t.Fatalf("LoadPlan: %v", err)
	}
	if p.Profile != "media" {
		t.Fatalf("profile = %q, want media", p.Profile)
	}
	if !equalStrings(p.Services, []string{"bridge", "commodore", "foghorn"}) {
		t.Fatalf("services = %v, want sorted [bridge commodore foghorn]", p.Services)
	}
}

func TestSummarizeSelection(t *testing.T) {
	t.Parallel()
	out := SummarizeSelection([]ServiceSpec{
		{Name: "bridge", Role: "control", Image: "img/bridge:latest"},
	})
	for _, want := range []string{"bridge", "control", "img/bridge:latest"} {
		if !strings.Contains(out, want) {
			t.Fatalf("summary %q missing %q", out, want)
		}
	}
}

func TestGenerateFragments(t *testing.T) {
	t.Parallel()

	t.Run("writes fragment and skips observability", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		specs := []ServiceSpec{
			{Name: "bridge", Role: "control", Image: "img/bridge:latest", Ports: []string{"18000:18000"}},
			{Name: "grafana", Role: "observability", Image: "grafana:latest"},
		}
		if err := GenerateFragments(dir, specs, false); err != nil {
			t.Fatalf("GenerateFragments: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(dir, "svc-bridge.yml"))
		if err != nil {
			t.Fatalf("expected svc-bridge.yml: %v", err)
		}
		content := string(got)
		for _, want := range []string{"img/bridge:latest", "frameworks-bridge", "18000:18000"} {
			if !strings.Contains(content, want) {
				t.Fatalf("fragment missing %q:\n%s", want, content)
			}
		}
		// observability service must not produce a fragment
		if _, err := os.Stat(filepath.Join(dir, "svc-grafana.yml")); !os.IsNotExist(err) {
			t.Fatalf("observability service should be skipped, got err=%v", err)
		}
	})

	t.Run("refuses overwrite without flag, succeeds with flag", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		specs := []ServiceSpec{{Name: "bridge", Role: "control", Image: "img/bridge:latest"}}
		if err := GenerateFragments(dir, specs, false); err != nil {
			t.Fatalf("first GenerateFragments: %v", err)
		}
		if err := GenerateFragments(dir, specs, false); err == nil {
			t.Fatalf("expected overwrite error on second run without --overwrite")
		}
		if err := GenerateFragments(dir, specs, true); err != nil {
			t.Fatalf("overwrite=true should succeed: %v", err)
		}
	})

	t.Run("deploy override drives container name", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		specs := []ServiceSpec{{Name: "periscope-ingest", Deploy: "periscope", Role: "analytics", Image: "img/p:latest"}}
		if err := GenerateFragments(dir, specs, false); err != nil {
			t.Fatalf("GenerateFragments: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(dir, "svc-periscope-ingest.yml"))
		if err != nil {
			t.Fatalf("read fragment: %v", err)
		}
		if !strings.Contains(string(got), "frameworks-periscope") {
			t.Fatalf("expected container_name from Deploy override:\n%s", got)
		}
	})
}
