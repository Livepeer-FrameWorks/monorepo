package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testRoadmap = `## Streaming

### StreamCrafter Browser Studio <Badge text="Now" variant="success" />

### Composition and SSAI <Badge text="Next" variant="tip" />

### Stream-Level Balances <Badge text="Later" variant="note" />

### Product Boundary: Customer-Operated Control Plane
`

func TestParseRoadmapSectionsReadsBadgedHeadingsOnly(t *testing.T) {
	got := parseRoadmapSections(testRoadmap)
	want := map[string]roadmapSection{
		"streamcrafter-browser-studio": {Title: "StreamCrafter Browser Studio", Horizon: "now"},
		"composition-and-ssai":         {Title: "Composition and SSAI", Horizon: "next"},
		"stream-level-balances":        {Title: "Stream-Level Balances", Horizon: "later"},
	}
	if len(got) != len(want) {
		t.Fatalf("sections = %v", got)
	}
	for anchor, w := range want {
		if got[anchor] != w {
			t.Errorf("section %q = %+v, want %+v", anchor, got[anchor], w)
		}
	}
}

func validStatusRegistry() *Registry {
	return &Registry{Features: []Feature{
		{
			Slug: "streamcrafter", Name: "StreamCrafter", Status: "shipped", Audience: "product", Kind: "product",
			Maturity: "beta", Note: "Beta.", Roadmap: "streamcrafter-browser-studio",
			Showcase: &Showcase{Order: 10, Summary: "Browser studio.", Figure: "MultistreamFanout",
				Link: &ShowcaseLink{Label: "Try it", To: "/stream-test?ref=x"}},
			Subitems: []Feature{{
				Slug: "composition", Name: "Composition and SSAI", Status: "roadmap", Audience: "product", Kind: "product",
				Horizon: "next", Roadmap: "composition-and-ssai",
				Showcase: &Showcase{Order: 20, Title: "StreamCrafter cloud production", Summary: "Server mixing."},
			}},
		},
		{
			Slug: "stream-balances", Name: "Stream-level balances", Status: "roadmap", Audience: "product", Kind: "product",
			Horizon: "later", Roadmap: "stream-level-balances",
		},
		{Slug: "backend-sdks", Name: "Backend SDKs", Status: "partial", Audience: "product", Kind: "product", Note: "Expanding."},
		{Slug: "byo", Name: "Federating", Status: "gap", Audience: "product", Kind: "product"},
	}}
}

func runStatusValidation(t *testing.T, reg *Registry) []string {
	t.Helper()
	root := t.TempDir()
	page := filepath.Join(root, "website_docs", "src", "content", "docs", "builders", "go-live-browser.mdx")
	if err := os.MkdirAll(filepath.Dir(page), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(page, []byte("---\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	v := &validator{repoRoot: root}
	v.validateStatusFields(reg, parseRoadmapSections(testRoadmap),
		map[string]bool{"/stream-test": true, "/": true}, map[string]bool{"MultistreamFanout": true})
	return v.errors
}

func TestValidateStatusFieldsAcceptsConsistentRegistry(t *testing.T) {
	reg := validStatusRegistry()
	if errs := runStatusValidation(t, reg); len(errs) != 0 {
		t.Fatalf("consistent registry rejected:\n%s", strings.Join(errs, "\n"))
	}
	if reg.Features[0].Horizon != "now" || reg.Features[2].Horizon != "now" {
		t.Fatalf("shipped/partial rows did not default to horizon now: %q %q", reg.Features[0].Horizon, reg.Features[2].Horizon)
	}
	if reg.Features[3].Horizon != "" {
		t.Fatalf("gap row got horizon %q", reg.Features[3].Horizon)
	}
}

func TestValidateStatusFieldsRejectsDrift(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(r *Registry)
		want   string
	}{
		{"roadmap horizon disagrees with badge", func(r *Registry) { r.Features[0].Subitems[0].Horizon = "later" },
			`composition: horizon later disagrees with the roadmap badge "next"`},
		{"product roadmap row without anchor", func(r *Registry) { r.Features[1].Roadmap = "" },
			"stream-balances: product roadmap rows need a roadmap section anchor"},
		{"badged section claimed by no row", func(r *Registry) { r.Features[1].Roadmap = "" },
			`roadmap section "Stream-Level Balances" (#stream-level-balances) is claimed by no registry row`},
		{"product roadmap row without horizon", func(r *Registry) { r.Features[1].Horizon = "" },
			"stream-balances: product roadmap rows need horizon next or later"},
		{"unknown anchor", func(r *Registry) { r.Features[1].Roadmap = "stream-balance" },
			`stream-balances: roadmap anchor "stream-balance" is not a badged section`},
		{"shipped row in the future", func(r *Registry) { r.Features[0].Horizon = "next" },
			"streamcrafter: status=shipped ships today, so horizon must be now"},
		{"beta on a roadmap row", func(r *Registry) { r.Features[1].Maturity = "beta" },
			"stream-balances: maturity beta applies only to shipped or partial rows"},
		{"unknown maturity", func(r *Registry) { r.Features[0].Maturity = "alpha" },
			`streamcrafter: invalid maturity "alpha"`},
		{"partial product row without note", func(r *Registry) { r.Features[2].Note = "" },
			"backend-sdks: product rows with status=partial need a public note"},
		{"gap row with horizon", func(r *Registry) { r.Features[3].Horizon = "later" },
			"byo: status=gap is outside the product and cannot have a horizon"},
		{"duplicate showcase order", func(r *Registry) { r.Features[0].Subitems[0].Showcase.Order = 10 },
			"composition: showcase.order 10 is already used by streamcrafter"},
		{"missing figure", func(r *Registry) { r.Features[0].Showcase.Figure = "AgentLoop" },
			`streamcrafter: showcase.figure "AgentLoop" has no AgentLoop.jsx`},
		{"unknown marketing route", func(r *Registry) { r.Features[0].Showcase.Link.To = "/studio" },
			`streamcrafter: showcase.link.to "/studio" has no route`},
		{"link with both targets", func(r *Registry) { r.Features[0].Showcase.Link.Docs = "builders/go-live-browser.mdx" },
			"streamcrafter: showcase.link needs exactly one of to (marketing route) or docs (docs page)"},
		{"missing docs page", func(r *Registry) {
			r.Features[0].Showcase.Link = &ShowcaseLink{Label: "Guide", Docs: "builders/nope.mdx"}
		}, `streamcrafter: showcase.link.docs "builders/nope.mdx" not found`},
		{"too many bullets", func(r *Registry) { r.Features[0].Showcase.Bullets = []string{"a", "b", "c", "d", "e"} },
			"streamcrafter: showcase has 5 bullets (max 4)"},
		{"showcase on a gap row", func(r *Registry) { r.Features[3].Showcase = &Showcase{Order: 99, Summary: "x"} },
			"byo: showcase rows need a horizon"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := validStatusRegistry()
			tc.mutate(reg)
			joined := strings.Join(runStatusValidation(t, reg), "\n")
			if !strings.Contains(joined, tc.want) {
				t.Fatalf("missing error %q in:\n%s", tc.want, joined)
			}
		})
	}
}

func TestRenderStatusJSONShape(t *testing.T) {
	reg := validStatusRegistry()
	reg.Features[0].Showcase.Link = &ShowcaseLink{Label: "Guide", Docs: "builders/go-live-browser.mdx"}
	if errs := runStatusValidation(t, reg); len(errs) != 0 {
		t.Fatalf("registry rejected: %v", errs)
	}
	buf, err := renderStatusJSON(*reg)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(buf, &doc); err != nil {
		t.Fatal(err)
	}
	features := doc["features"].(map[string]any)
	if _, ok := features["byo"]; ok {
		t.Fatal("gap row without a horizon must not appear in status.json")
	}
	sc := features["streamcrafter"].(map[string]any)
	if sc["tag"] != "Beta" || sc["tier"] != "Shipping today" || sc["availability"] != "Available" || sc["maturity"] != "beta" {
		t.Fatalf("streamcrafter status = %v", sc)
	}
	if rm := sc["roadmap"].(map[string]any); rm["path"] != "/roadmap#streamcrafter-browser-studio" {
		t.Fatalf("roadmap = %v", rm)
	}
	link := sc["showcase"].(map[string]any)["link"].(map[string]any)
	if link["site"] != "docs" || link["path"] != "/builders/go-live-browser" {
		t.Fatalf("docs link = %v", link)
	}
	sdks := features["backend-sdks"].(map[string]any)
	for _, key := range []string{"maturity", "roadmap", "showcase"} {
		v, present := sdks[key]
		if !present || v != nil {
			t.Errorf("backend-sdks.%s = %v (present %v), want null", key, v, present)
		}
	}
	if sdks["tag"] != "Shipping" || sdks["availability"] != "Expanding" {
		t.Fatalf("backend-sdks = %v", sdks)
	}
	comp := features["composition"].(map[string]any)
	if comp["tag"] != "Up next" || comp["tier"] != "Up next" || comp["showcase"].(map[string]any)["title"] != "StreamCrafter cloud production" {
		t.Fatalf("composition = %v", comp)
	}
	if features["stream-balances"].(map[string]any)["tag"] != "Further out" {
		t.Fatal("later row is not tagged Further out")
	}
	showcase := doc["showcase"].(map[string]any)
	if got := showcase["now"].([]any); len(got) != 1 || got[0] != "streamcrafter" {
		t.Fatalf("showcase.now = %v", got)
	}
	if got := showcase["later"].([]any); len(got) != 0 {
		t.Fatalf("showcase.later = %v, want empty (stream-balances has no showcase copy)", got)
	}
}

func TestStatusConsumerErrors(t *testing.T) {
	literal := `const items = [{ label: "SCTE-35", status: "expanding" }];
const group = { eyebrow: "In the pipeline" };
const note = "On the roadmap: planned, with no date yet.";`
	joined := strings.Join(statusConsumerErrors(literal), "\n")
	for _, want := range []string{"does not import data/status.json", `literal status "expanding"`, `literal tier label "In the pipeline"`, `literal tier label "On the roadmap"`} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	withImport := strings.Join(statusConsumerErrors(`import status from "@/data/status.json";
`+literal), "\n")
	if strings.Contains(withImport, "does not import") || !strings.Contains(withImport, `literal status "expanding"`) {
		t.Fatalf("an import must not exempt literals:\n%s", withImport)
	}
	clean := `import status from "../../data/status.json";
const tag = status.features["scte35-cues"].tag;
const heading = status.tiers[0].label;`
	if errs := statusConsumerErrors(clean); len(errs) != 0 {
		t.Fatalf("data-driven consumer rejected: %v", errs)
	}
}

func TestCheckMarketingStatusConsumers(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	clean := `import status from "@/data/status.json";
export const tag = status.features.streams.tag;`
	for _, rel := range statusConsumers {
		write(rel, clean)
	}
	write(siteConfigPath, `description: "What ships today, what is up next, and what is further out.",`)
	write("website_marketing/src/components/pages/LandingPage.jsx", `<p>Your account moves to Free after that.</p>`)

	check := func() string {
		v := &validator{repoRoot: root}
		v.checkMarketingStatusConsumers(root)
		return strings.Join(v.errors, "\n")
	}
	if got := check(); got != "" {
		t.Fatalf("data-driven marketing sources rejected:\n%s", got)
	}

	write(statusConsumers[0], `export const label = "Pipeline";`)
	write("website_marketing/src/components/pages/LandingPage.jsx", `<span>Shipping today</span>`)
	write(siteConfigPath, `description: "What is in the pipeline, and what is further out on the roadmap.",`)
	got := check()
	for _, want := range []string{
		statusConsumers[0] + ": does not import data/status.json",
		`LandingPage.jsx: literal tier label "Shipping today"`,
		siteConfigPath + `: retired tier name "in the pipeline"`,
		siteConfigPath + `: retired tier name "on the roadmap"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}

	if err := os.Remove(filepath.Join(root, filepath.FromSlash(statusConsumers[1]))); err != nil {
		t.Fatal(err)
	}
	if got := check(); !strings.Contains(got, statusConsumers[1]+": status consumer is missing") {
		t.Fatalf("a removed consumer was not reported:\n%s", got)
	}
}

func TestMatrixCellsCarryBetaRoadmapAndNote(t *testing.T) {
	f := Feature{Status: "shipped", Maturity: "beta", Roadmap: "streamcrafter-browser-studio", Note: "Expect | breaking changes."}
	if got := availabilityCell(f); got != "[Available (beta)](/roadmap#streamcrafter-browser-studio)" {
		t.Fatalf("availability = %q", got)
	}
	if got := describeCell(f, "Studio."); got != `Studio. *Expect \| breaking changes.*` {
		t.Fatalf("description = %q", got)
	}
	if got := availabilityCell(Feature{Status: "partial"}); got != "Expanding" {
		t.Fatalf("plain availability = %q", got)
	}
}

func TestCapabilityPagesPointAtTheShowcase(t *testing.T) {
	const pointer = "[showcase](%MARKETING_URL%/showcase)"
	reg := Registry{}
	for name, page := range map[string]string{
		"feature-matrix.mdx":        renderMatrixMDX(reg),
		"platform-capabilities.mdx": renderCapabilitiesMDX(reg),
	} {
		intro, _, _ := strings.Cut(page, "\n#")
		if !strings.Contains(intro, pointer) {
			t.Errorf("%s intro lacks the showcase pointer %q:\n%s", name, pointer, intro)
		}
	}
}
