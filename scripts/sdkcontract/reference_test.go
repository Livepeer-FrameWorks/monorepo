package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

func loadTestSchema(t *testing.T, sdl string) *ast.Schema {
	t.Helper()
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "test.graphql", Input: sdl})
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func referencePath(slug string) string { return referenceDir + "/" + slug + ".mdx" }

// pageDefining returns the page slug whose headings include name.
func pageDefining(t *testing.T, files map[string]string, heading string) string {
	t.Helper()
	var found []string
	for path, page := range files {
		if regexp.MustCompile(`(?m)^#{2,3} ` + regexp.QuoteMeta(heading) + `$`).MatchString(page) {
			found = append(found, strings.TrimSuffix(filepath.Base(path), ".mdx"))
		}
	}
	if len(found) != 1 {
		t.Fatalf("%s is defined on %d pages %v, want exactly one", heading, len(found), found)
	}
	return found[0]
}

func TestReferenceDomainsClaimEveryPublicRoot(t *testing.T) {
	schema, err := loadPublicSchema("../..", headRef)
	if err != nil {
		t.Fatal(err)
	}
	roots := publicRootFields(schema)
	assigned, problems := assignDomains(referenceDomains, roots)
	if len(problems) > 0 {
		t.Fatalf("domain problems:\n%s", strings.Join(problems, "\n"))
	}
	if len(assigned) != len(roots) || len(roots) == 0 {
		t.Fatalf("assigned %d of %d root fields", len(assigned), len(roots))
	}
}

func TestReferenceFailsOnUnclaimedRoot(t *testing.T) {
	schema := loadTestSchema(t, `
type Query { alpha: Int, zulu: Int }
`)
	domains := []referenceDomain{{Slug: "a", Title: "A", Summary: "A.", Keywords: []string{"alpha"}}}
	_, err := renderReferenceDomains(schema, nil, domains)
	if err == nil || !strings.Contains(err.Error(), "query.zulu: no reference domain claims this root field") {
		t.Fatalf("unclaimed root not reported: %v", err)
	}

	domains[0].Keywords = append(domains[0].Keywords, "zulu", "unused")
	_, err = renderReferenceDomains(schema, nil, domains)
	if err == nil || !strings.Contains(err.Error(), `keyword "unused" claims no root field`) {
		t.Fatalf("dead keyword not reported: %v", err)
	}
}

func TestReferenceKeywordPrecedence(t *testing.T) {
	roots := []rootField{
		{Kind: "query", Field: &ast.FieldDefinition{Name: "skipperConversation"}},
		{Kind: "query", Field: &ast.FieldDefinition{Name: "tenantUsage"}},
		{Kind: "query", Field: &ast.FieldDefinition{Name: "tenant"}},
		{Kind: "query", Field: &ast.FieldDefinition{Name: "conversation"}},
		{Kind: "subscription", Field: &ast.FieldDefinition{Name: "tenantEvents"}},
	}
	domains := []referenceDomain{
		{Slug: "account", Keywords: []string{"tenant"}},
		{Slug: "billing", Keywords: []string{"tenantusage"}},
		{Slug: "support", Keywords: []string{"conversation"}},
		{Slug: "skipper", Keywords: []string{"skipper"}},
		{Slug: "live", Subscriptions: true},
	}
	assigned, problems := assignDomains(domains, roots)
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	want := map[string]string{
		"query.skipperConversation": "skipper", // earliest keyword wins
		"query.tenantUsage":         "billing", // longer keyword wins a tie
		"query.tenant":              "account",
		"query.conversation":        "support",
		"subscription.tenantEvents": "live", // subscriptions ignore keywords
	}
	for key, slug := range want {
		if got := domains[assigned[key]].Slug; got != slug {
			t.Errorf("%s: domain %s, want %s", key, got, slug)
		}
	}
}

const placementSDL = `
directive @experimental(reason: String!, until: String!) on FIELD_DEFINITION
scalar Time
interface Node { id: ID! }
type Query {
  alpha(at: Time): Alpha
  beta: Beta
  node(id: ID!): Node
}
type Subscription { betaEvents: Beta }
type Alpha implements Node {
  id: ID!
  shared: Shared
  alphaOnly: AlphaOnly @experimental(reason: "Shape may change.", until: "v9.0.0")
}
type Beta { shared: Shared, betaOnly: BetaOnly }
type Shared { at: Time }
type AlphaOnly { n: Int }
type BetaOnly { n: Int }
`

var placementDomains = []referenceDomain{
	{Slug: "alpha", Title: "Alpha", Summary: "Alpha.", Keywords: []string{"alpha"}},
	{Slug: "beta", Title: "Beta", Summary: "Beta.", Keywords: []string{"beta"}},
	{Slug: "lookup", Title: "Lookup", Summary: "Lookup.", Keywords: []string{"node"}},
	{Slug: "live", Title: "Live", Summary: "Live.", Subscriptions: true},
}

func TestReferencePlacesDomainAndSharedTypes(t *testing.T) {
	schema := loadTestSchema(t, placementSDL)
	files, err := renderReferenceDomains(schema, nil, placementDomains)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"AlphaOnly": "alpha",
		"BetaOnly":  "shared", // Query.beta and Subscription.betaEvents are different domains
		"Beta":      "shared",
		"Shared":    "shared",
		"Time":      "shared",
		// Query.node returns the interface; that does not reach Alpha.
		"Alpha": "alpha",
		"Node":  "shared",
	}
	for name, slug := range want {
		if got := pageDefining(t, files, name); got != slug {
			t.Errorf("%s is on %s, want %s", name, got, slug)
		}
	}
	for _, heading := range []string{"Query.alpha", "Query.beta", "Query.node", "Subscription.betaEvents"} {
		pageDefining(t, files, heading)
	}
	if !strings.Contains(files[referencePath("shared")], "Implemented by [Alpha](/builders/api-schema/alpha/#alpha).") {
		t.Error("interface does not link its implementations")
	}
	if !strings.Contains(files[referencePath("alpha")], "**alphaOnly**: `AlphaOnly` ([AlphaOnly](/builders/api-schema/alpha/#alphaonly)) **Experimental until v9.0.0.**") {
		t.Error("nested experimental field has no badge")
	}
}

func TestReferenceRootShowsExperimentalAndSDKNames(t *testing.T) {
	schema := loadTestSchema(t, `
directive @experimental(reason: String!, until: String!) on FIELD_DEFINITION
type Query {
  "Fetch an alpha."
  alpha(id: ID!, limit: Int = 10): Alpha @experimental(reason: "Shape may change.", until: "v9.0.0")
  alphaTree: AlphaTree
}
type Subscription { alphaEvents: Alpha }
type Alpha { id: ID! }
type AlphaTree { child(id: ID!): Alpha }
`)
	bindings := map[string][]sdkBinding{
		"query.alpha":              {{Operation: "GetDVRAlpha", Kind: "query", Path: []string{"alpha"}}},
		"query.alphaTree":          {{Operation: "GetChild", Kind: "query", Path: []string{"alphaTree", "child"}}},
		"subscription.alphaEvents": {{Operation: "AlphaEvents", Kind: "subscription", Path: []string{"alphaEvents"}}},
	}
	domains := []referenceDomain{{Slug: "alpha", Title: "Alpha", Summary: "Alpha.", Keywords: []string{"alpha"}}, {Slug: "live", Title: "Live", Summary: "Live.", Subscriptions: true}}
	files, err := renderReferenceDomains(schema, bindings, domains)
	if err != nil {
		t.Fatal(err)
	}
	page := files[referencePath("alpha")]
	for _, want := range []string{
		"### Query.alpha\n\n**Experimental until v9.0.0:** Shape may change.\n\nFetch an alpha.\n\n```graphql\nalpha(\n  id: ID!\n  limit: Int = 10\n): Alpha\n```\n",
		"- `limit`: `Int` (default `10`)",
		"| GetDVRAlpha | `GetDVRAlphaDocument` | `GetDVRAlpha` | `get_dvr_alpha` |",
		"| GetChild | `alphaTree.child` | `GetChildDocument` | `GetChild` | `get_child` |",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("alpha page lacks %q", want)
		}
	}
	if !strings.Contains(files[referencePath("live")], "| AlphaEvents | `AlphaEventsDocument` | `SubscribeAlphaEvents` | `alpha_events` (async) |") {
		t.Error("subscription SDK names missing")
	}
}

func TestReferenceDeprecatedRootNamesNoOperation(t *testing.T) {
	schema := loadTestSchema(t, `
type Query {
  alpha: Int
  oldAlpha: Int @deprecated(reason: "Use alpha.")
}
`)
	domains := []referenceDomain{{Slug: "alpha", Title: "Alpha", Summary: "Alpha.", Keywords: []string{"alpha"}}}
	files, err := renderReferenceDomains(schema, nil, domains)
	if err != nil {
		t.Fatal(err)
	}
	page := files[referencePath("alpha")]
	if !strings.Contains(page, "**SDK:** none; deprecated fields get no generated operation.") {
		t.Error("deprecated root does not explain the missing operation")
	}
	if !strings.Contains(page, "**SDK:** no generated operation;") {
		t.Error("undeprecated root without an operation is not reported")
	}
}

// TestReferenceEveryUndeprecatedRootHasAnOperation reads the repository's
// operations: the reference shows an SDK method for every public root field
// the default-operation generator targets.
func TestReferenceEveryUndeprecatedRootHasAnOperation(t *testing.T) {
	schema, err := loadPublicSchema("../..", headRef)
	if err != nil {
		t.Fatal(err)
	}
	ops, err := loadOperations("../..")
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := sdkBindings(schema, ops)
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range publicRootFields(schema) {
		if !isDeprecated(root.Field.Directives) && len(bindings[root.key()]) == 0 {
			t.Errorf("%s has no SDK operation", root.key())
		}
	}
}

func TestPythonMethodNameMatchesAriadne(t *testing.T) {
	for in, want := range map[string]string{
		"GetStream":         "get_stream",
		"ListDVRChapters":   "list_dvr_chapters",
		"DeleteDVR":         "delete_dvr",
		"SubmitX402Payment": "submit_x_402_payment",
		"GetAPIUsage":       "get_api_usage",
		"ABc":               "a_bc",
	} {
		if got := pythonMethodName(in); got != want {
			t.Errorf("pythonMethodName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestReferenceSDKNamesExistInSDKs checks the naming rules against the
// generated SDKs for the hand-written operations.
func TestReferenceSDKNamesExistInSDKs(t *testing.T) {
	ops, err := loadOperations("../..")
	if err != nil {
		t.Fatal(err)
	}
	read := func(path string) string {
		data, err := os.ReadFile(filepath.Join("../..", path))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	ts := read("npm_api/src/generated/graphql.ts")
	goSDK := read("sdk_go/generated.go")
	py := read("sdk_python/src/livepeer_frameworks/_generated/graphql/async_client.py")
	checked := 0
	for _, op := range ops {
		if strings.HasPrefix(op.File, publicDir+"/generated/") {
			continue
		}
		checked++
		if !strings.Contains(ts, "export const "+op.Name+"Document ") {
			t.Errorf("TypeScript SDK has no %sDocument", op.Name)
		}
		if name := goFunctionName(op.Name, op.Kind); !strings.Contains(goSDK, "\nfunc "+name+"(") {
			t.Errorf("Go SDK has no func %s", name)
		}
		if !strings.Contains(py, "async def "+pythonMethodName(op.Name)+"(") {
			t.Errorf("Python SDK has no method %s", pythonMethodName(op.Name))
		}
	}
	if checked == 0 {
		t.Fatal("no hand-written operations")
	}
}

var (
	headingPattern = regexp.MustCompile(`(?m)^#{2,6}\s+(.+?)\s*$`)
	linkPattern    = regexp.MustCompile(`\]\((/builders/api-schema/[^)\s]*)\)`)
	slugDrop       = regexp.MustCompile(`[^\p{L}\p{N}\s-]`)
	fencePattern   = regexp.MustCompile("(?s)```.*?```")
)

// headingAnchors mirrors website_docs/scripts/check-internal-links.mjs.
func headingAnchors(page string) (map[string]bool, []string) {
	anchors := map[string]bool{}
	var duplicates []string
	for _, match := range headingPattern.FindAllStringSubmatch(fencePattern.ReplaceAllString(page, ""), -1) {
		slug := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(slugDrop.ReplaceAllString(match[1], ""))), " ", "-")
		if anchors[slug] {
			duplicates = append(duplicates, slug)
		}
		anchors[slug] = true
	}
	return anchors, duplicates
}

func TestReferenceLinksResolveAndEveryTypeIsRenderedOnce(t *testing.T) {
	schema, err := loadPublicSchema("../..", headRef)
	if err != nil {
		t.Fatal(err)
	}
	files, err := renderReference(schema, map[string][]sdkBinding{"query.stream": {{Operation: "GetStream", Kind: "query", Path: []string{"stream"}}}})
	if err != nil {
		t.Fatal(err)
	}
	again, err := renderReference(schema, map[string][]sdkBinding{"query.stream": {{Operation: "GetStream", Kind: "query", Path: []string{"stream"}}}})
	if err != nil {
		t.Fatal(err)
	}
	anchors := map[string]map[string]bool{}
	for path, page := range files {
		if again[path] != page {
			t.Fatalf("%s is not deterministic", path)
		}
		slug := strings.TrimSuffix(filepath.Base(path), ".mdx")
		route := referenceRoute + slug + "/"
		if slug == "index" {
			route = referenceRoute
		}
		var duplicates []string
		anchors[route], duplicates = headingAnchors(page)
		if len(duplicates) > 0 {
			t.Errorf("%s repeats anchors %v", path, duplicates)
		}
		if lines := strings.Count(page, "\n"); lines > 2*maxReferencePageLines {
			t.Errorf("%s has %d lines", path, lines)
		}
	}
	for path, page := range files {
		for _, match := range linkPattern.FindAllStringSubmatch(page, -1) {
			route, anchor, _ := strings.Cut(match[1], "#")
			if anchors[route] == nil {
				t.Errorf("%s links to missing page %s", path, match[1])
			} else if anchor != "" && !anchors[route][anchor] {
				t.Errorf("%s links to missing anchor %s", path, match[1])
			}
		}
	}
	for name, def := range schema.Types {
		if def.BuiltIn || name == "Query" || name == "Mutation" || name == "Subscription" {
			continue
		}
		pageDefining(t, files, name)
	}
	if !strings.Contains(files[referencePath("streams")], "| GetStream | `GetStreamDocument` | `GetStream` | `get_stream` |") {
		t.Error("streams page does not show GetStream SDK names")
	}
	for _, private := range []string{"Query.platform", "Query.bootstrapTokensConnection", "Mutation.createBootstrapToken", "Mutation.revokeBootstrapToken", "Platform", "PlatformTenantIndex"} {
		for path, page := range files {
			if regexp.MustCompile(`(?m)^#{2,3} ` + regexp.QuoteMeta(private) + `$`).MatchString(page) {
				t.Errorf("private %s exposed in %s", private, path)
			}
		}
	}
}

func TestPruneReferenceRemovesStalePages(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(repo, referenceDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"index.mdx", "types-a-f.mdx", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{referencePath("index"): "x\n"}

	stale, err := pruneReference(repo, files, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 1 || stale[0] != referencePath("types-a-f") {
		t.Fatalf("check mode reported %v", stale)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "types-a-f.mdx")); statErr != nil {
		t.Fatal("check mode removed a page")
	}

	if stale, err = pruneReference(repo, files, false); err != nil || len(stale) != 0 {
		t.Fatalf("write mode: %v %v", stale, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "types-a-f.mdx")); !os.IsNotExist(err) {
		t.Fatal("stale page not removed")
	}
	for _, kept := range []string{"index.mdx", "notes.txt"} {
		if _, err := os.Stat(filepath.Join(dir, kept)); err != nil {
			t.Fatalf("%s removed", kept)
		}
	}
}
