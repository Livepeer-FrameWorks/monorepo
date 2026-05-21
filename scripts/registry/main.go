// Validator + JSON generator for docs/platform-features.yaml.
//
// Validates that every GraphQL operation, MCP tool, webapp route, and docs page
// referenced by the registry actually exists in the repo. Emits a flattened
// JSON copy at website_application/src/lib/features/registry.json for the
// webapp's /developer/features browser to consume.
//
// Run via `make verify-feature-registry` from repo root.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type SurfaceReq struct {
	Required              bool   `yaml:"required" json:"required"`
	Reason                string `yaml:"reason,omitempty" json:"reason,omitempty"`
	Sensitive             bool   `yaml:"sensitive,omitempty" json:"sensitive,omitempty"`
	ReturnsSecretMaterial bool   `yaml:"returns_secret_material,omitempty" json:"returns_secret_material,omitempty"`
	RequiresConfirmation  bool   `yaml:"requires_confirmation,omitempty" json:"requires_confirmation,omitempty"`
	AuditEvents           bool   `yaml:"audit_events,omitempty" json:"audit_events,omitempty"`
	PaymentAuthority      bool   `yaml:"payment_authority,omitempty" json:"payment_authority,omitempty"`
	CostAffecting         bool   `yaml:"cost_affecting,omitempty" json:"cost_affecting,omitempty"`
	DestructiveAdjacent   bool   `yaml:"destructive_adjacent,omitempty" json:"destructive_adjacent,omitempty"`
}

type GraphQLSurface struct {
	Mutations     []string `yaml:"mutations,omitempty" json:"mutations,omitempty"`
	Queries       []string `yaml:"queries,omitempty" json:"queries,omitempty"`
	Subscriptions []string `yaml:"subscriptions,omitempty" json:"subscriptions,omitempty"`
	Fields        []string `yaml:"fields,omitempty" json:"fields,omitempty"`
}

type MCPSurface struct {
	Tools []string `yaml:"tools,omitempty" json:"tools,omitempty"`
}

type WebappSurface struct {
	Routes []string `yaml:"routes,omitempty" json:"routes,omitempty"`
}

type DocsSurface struct {
	Pages []string `yaml:"pages,omitempty" json:"pages,omitempty"`
}

type Surfaces struct {
	GraphQL GraphQLSurface `yaml:"graphql,omitempty" json:"graphql"`
	MCP     MCPSurface     `yaml:"mcp,omitempty" json:"mcp"`
	Webapp  WebappSurface  `yaml:"webapp,omitempty" json:"webapp"`
	Docs    DocsSurface    `yaml:"docs,omitempty" json:"docs"`
}

type Example struct {
	Title string `yaml:"title" json:"title"`
	Query string `yaml:"query" json:"query"`
}

type Configurability struct {
	CostAffecting         bool   `yaml:"cost_affecting,omitempty" json:"cost_affecting,omitempty"`
	SecurityAffecting     bool   `yaml:"security_affecting,omitempty" json:"security_affecting,omitempty"`
	TenantDefault         string `yaml:"tenant_default,omitempty" json:"tenant_default,omitempty"`
	PerResourceOverride   string `yaml:"per_resource_override,omitempty" json:"per_resource_override,omitempty"`
	EffectiveValueVisible string `yaml:"effective_value_visible,omitempty" json:"effective_value_visible,omitempty"`
	EntitlementBounds     string `yaml:"entitlement_bounds,omitempty" json:"entitlement_bounds,omitempty"`
	AuditEvents           string `yaml:"audit_events,omitempty" json:"audit_events,omitempty"`
	Undo                  string `yaml:"undo,omitempty" json:"undo,omitempty"`
	DryRun                string `yaml:"dry_run,omitempty" json:"dry_run,omitempty"`
}

type Feature struct {
	Slug             string                `yaml:"slug" json:"slug"`
	Name             string                `yaml:"name" json:"name"`
	Area             string                `yaml:"area" json:"area"`
	Description      string                `yaml:"description,omitempty" json:"description,omitempty"`
	Status           string                `yaml:"status" json:"status"`
	GapReason        string                `yaml:"gap_reason,omitempty" json:"gap_reason,omitempty"`
	RequiredSurfaces map[string]SurfaceReq `yaml:"required_surfaces" json:"required_surfaces"`
	Configurability  *Configurability      `yaml:"configurability,omitempty" json:"configurability,omitempty"`
	Surfaces         Surfaces              `yaml:"surfaces,omitempty" json:"surfaces"`
	Examples         []Example             `yaml:"examples,omitempty" json:"examples,omitempty"`

	// Computed (not in YAML)
	ActualSurfaces map[string]bool `yaml:"-" json:"actual_surfaces"`
}

type Registry struct {
	Features []Feature `yaml:"features" json:"features"`
}

type validator struct {
	repoRoot            string
	schemaMutations     map[string]bool
	schemaQueries       map[string]bool
	schemaSubscriptions map[string]bool
	schemaFields        map[string]bool
	mcpTools            map[string]string // tool name → file path
	errors              []string
}

func main() {
	var emitJSON bool
	flag.BoolVar(&emitJSON, "emit-json", true, "emit website_application/src/lib/features/registry.json")
	flag.Parse()

	repoRoot, err := findRepoRoot()
	if err != nil {
		die("locate repo root: %v", err)
	}

	yamlPath := filepath.Join(repoRoot, "docs", "platform-features.yaml")
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		die("read %s: %v", yamlPath, err)
	}

	var reg Registry
	if err := yaml.Unmarshal(data, &reg); err != nil {
		die("parse %s: %v", yamlPath, err)
	}

	v := &validator{repoRoot: repoRoot}
	if err := v.loadSchema(); err != nil {
		die("load schema: %v", err)
	}
	if err := v.loadMCPTools(); err != nil {
		die("load MCP tools: %v", err)
	}

	for i := range reg.Features {
		v.validateFeature(&reg.Features[i])
		reg.Features[i].computeActualSurfaces()
	}

	// Cross-cutting check: any reachable GraphQL field must be wired to a
	// real resolver. gqlgen can regenerate panic stubs when resolver method
	// signatures drift; this catches that before a request can hit the field.
	v.checkResolverStubs(&reg)

	if len(v.errors) > 0 {
		fmt.Fprintln(os.Stderr, "✗ Feature registry validation failed:")
		for _, e := range v.errors {
			fmt.Fprintln(os.Stderr, "  - "+e)
		}
		os.Exit(1)
	}
	fmt.Printf("✓ Feature registry validated (%d features)\n", len(reg.Features))

	if emitJSON {
		outPath := filepath.Join(repoRoot, "website_application", "src", "lib", "features", "registry.json")
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			die("mkdir %s: %v", filepath.Dir(outPath), err)
		}
		buf, err := json.MarshalIndent(reg, "", "  ")
		if err != nil {
			die("marshal json: %v", err)
		}
		if err := os.WriteFile(outPath, append(buf, '\n'), 0o644); err != nil {
			die("write %s: %v", outPath, err)
		}
		fmt.Printf("✓ Wrote %s\n", relativeOrAbs(repoRoot, outPath))

		mdxPath := filepath.Join(repoRoot, "website_docs", "src", "content", "docs", "platform", "feature-matrix.mdx")
		if err := os.MkdirAll(filepath.Dir(mdxPath), 0o755); err != nil {
			die("mkdir %s: %v", filepath.Dir(mdxPath), err)
		}
		if err := os.WriteFile(mdxPath, []byte(renderMatrixMDX(reg)), 0o644); err != nil {
			die("write %s: %v", mdxPath, err)
		}
		fmt.Printf("✓ Wrote %s\n", relativeOrAbs(repoRoot, mdxPath))
	}
}

// relativeOrAbs returns the path relative to base, falling back to the
// absolute path when filepath.Rel would error (different volumes etc.).
func relativeOrAbs(base, target string) string {
	if rel, err := filepath.Rel(base, target); err == nil {
		return rel
	}
	return target
}

// renderMatrixMDX emits a Starlight-compatible MDX feature matrix grouped by area.
func renderMatrixMDX(reg Registry) string {
	areas := map[string][]Feature{}
	for _, f := range reg.Features {
		if f.Status == "roadmap" {
			continue
		}
		f.computeActualSurfaces()
		areas[f.Area] = append(areas[f.Area], f)
	}
	keys := make([]string, 0, len(areas))
	for k := range areas {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	statusOrder := map[string]int{"shipped": 0, "partial": 1, "gap": 2}
	for _, k := range keys {
		fs := areas[k]
		sort.SliceStable(fs, func(i, j int) bool {
			if statusOrder[fs[i].Status] != statusOrder[fs[j].Status] {
				return statusOrder[fs[i].Status] < statusOrder[fs[j].Status]
			}
			return fs[i].Name < fs[j].Name
		})
	}

	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("title: Platform capabilities\n")
	b.WriteString("description: What FrameWorks exposes across APIs, agents, dashboard workflows, and docs.\n")
	b.WriteString("---\n\n")
	b.WriteString("FrameWorks is built as a platform, not only a dashboard. The matrix shows which capabilities are available today and where you can use them: GraphQL API, MCP tools for agents, dashboard workflows, and developer/operator docs.\n\n")
	b.WriteString("## Why teams pick FrameWorks\n\n")
	b.WriteString("- **Sovereign deployment options** — run FrameWorks as SaaS, add your own edge clusters, or self-host the full stack without changing platforms.\n")
	b.WriteString("- **Complete live-video workflow** — ingest, playback, multistreaming, playback access control, 24/7 DVR, chapters, clips, VOD, thumbnails, and analytics in one control plane.\n")
	b.WriteString("- **Deep routing and QoE visibility** — see viewer routing, stream health, geography, quality, and node performance instead of treating delivery as a black box.\n")
	b.WriteString("- **Agent-native operations** — the dashboard, GraphQL API, and MCP tools expose the same platform controls for humans, automation, and AI agents.\n\n")
	b.WriteString("## Availability key\n\n")
	b.WriteString("- **Available** — ready to use today; availability can still depend on account tier, deployment, and configuration.\n")
	b.WriteString("- **Expanding** — the core capability is live; a specific surface or workflow is actively shipping.\n")
	b.WriteString("- **Planned** — tracked on the [Roadmap](/roadmap), not represented as a shipped capability in this matrix.\n\n")
	b.WriteString("## Capability matrix\n\n")

	surfaceLabel := map[string]string{"graphql": "API", "mcp": "Agents (MCP)", "webapp": "Dashboard", "docs": "Docs"}
	surfaceKeys := []string{"graphql", "mcp", "webapp", "docs"}
	surfaceSummary := func(f Feature) string {
		available := []string{}
		for _, s := range surfaceKeys {
			if f.ActualSurfaces[s] {
				available = append(available, surfaceLabel[s])
			}
		}
		if len(available) == 0 {
			return "Contact us"
		}
		return strings.Join(available, ", ")
	}

	for _, area := range keys {
		fmt.Fprintf(&b, "### %s\n\n", publicAreaLabel(area))
		b.WriteString("| Capability | Availability | Surfaces | What it unlocks |\n")
		b.WriteString("| --- | --- | --- | --- |\n")
		for _, f := range areas[area] {
			description := strings.TrimSpace(f.Description)
			if description == "" {
				description = f.Name
			}
			fmt.Fprintf(&b, "| **%s** | %s | %s | %s |\n", f.Name, publicStatusLabel(f.Status), escapeMDXCell(surfaceSummary(f)), escapeMDXCell(compactWhitespace(description)))
		}
		b.WriteString("\n")
	}
	b.WriteString("Planned product work lives on the [Roadmap](/roadmap). This page stays focused on capabilities that are already usable or actively expanding across shipped surfaces.\n")
	return b.String()
}

func publicStatusLabel(status string) string {
	switch status {
	case "shipped":
		return "Available"
	case "partial", "gap":
		return "Expanding"
	case "roadmap":
		return "Planned"
	default:
		return status
	}
}

func publicAreaLabel(area string) string {
	labels := map[string]string{
		"account":        "Account",
		"agents":         "Agents",
		"analytics":      "Analytics",
		"billing":        "Billing",
		"developer":      "Developer",
		"infrastructure": "Infrastructure",
		"playback":       "Playback",
		"storage":        "Storage",
		"streaming":      "Streaming",
	}
	if label, ok := labels[area]; ok {
		return label
	}
	if area == "" {
		return "Other"
	}
	return strings.ToUpper(area[:1]) + area[1:]
}

func compactWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// escapeMDXCell escapes characters that would break a Markdown table cell
// rendered through MDX: pipes terminate cells; `<` opens a JSX tag.
func escapeMDXCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

func (f *Feature) computeActualSurfaces() {
	f.ActualSurfaces = map[string]bool{
		"graphql": len(f.Surfaces.GraphQL.Mutations) > 0 || len(f.Surfaces.GraphQL.Queries) > 0 || len(f.Surfaces.GraphQL.Subscriptions) > 0 || len(f.Surfaces.GraphQL.Fields) > 0,
		"mcp":     len(f.Surfaces.MCP.Tools) > 0,
		"webapp":  len(f.Surfaces.Webapp.Routes) > 0,
		"docs":    len(f.Surfaces.Docs.Pages) > 0,
	}
}

func (v *validator) validateFeature(f *Feature) {
	if f.Status == "roadmap" && strings.TrimSpace(f.GapReason) == "" {
		v.errf("%s: status=roadmap requires gap_reason", f.Slug)
	}
	v.validateConfigurability(f)

	if f.Status == "roadmap" {
		// Roadmap rows skip reference validation; they describe future intent.
		return
	}

	for _, m := range f.Surfaces.GraphQL.Mutations {
		if !v.schemaMutations[m] {
			v.errf("%s: GraphQL mutation %q not found in schema.graphql", f.Slug, m)
		}
	}
	for _, q := range f.Surfaces.GraphQL.Queries {
		if !v.schemaQueries[q] {
			v.errf("%s: GraphQL query %q not found in schema.graphql", f.Slug, q)
		}
	}
	for _, s := range f.Surfaces.GraphQL.Subscriptions {
		if !v.schemaSubscriptions[s] {
			v.errf("%s: GraphQL subscription %q not found in schema.graphql", f.Slug, s)
		}
	}
	for _, field := range f.Surfaces.GraphQL.Fields {
		if !v.schemaFields[field] {
			v.errf("%s: GraphQL field %q not found in schema.graphql (use Type.field)", f.Slug, field)
		}
	}
	for _, t := range f.Surfaces.MCP.Tools {
		if _, ok := v.mcpTools[t]; !ok {
			v.errf("%s: MCP tool %q not registered under api_gateway/internal/mcp/tools/", f.Slug, t)
		}
	}
	for _, r := range f.Surfaces.Webapp.Routes {
		if !v.routeExists(r) {
			v.errf("%s: webapp route %q has no +page.svelte under website_application/src/routes/", f.Slug, r)
		}
	}
	for _, p := range f.Surfaces.Docs.Pages {
		if !v.docsPageExists(p) {
			v.errf("%s: docs page %q not found under website_docs/src/content/docs/", f.Slug, p)
		}
	}

	// Status sanity: if required surface is unmet, status must be partial or gap.
	missing := []string{}
	for surface, req := range f.RequiredSurfaces {
		if !req.Required {
			continue
		}
		f.computeActualSurfaces()
		if !f.ActualSurfaces[surface] {
			missing = append(missing, surface)
		}
	}
	switch f.Status {
	case "shipped":
		if len(missing) > 0 {
			sort.Strings(missing)
			v.errf("%s: status=shipped but required surfaces missing: %s", f.Slug, strings.Join(missing, ", "))
		}
	case "partial":
		// All required surfaces being filled at the row level (any-op-present)
		// doesn't mean a feature is complete — it might still be missing specific
		// operations (e.g. signing-keys MCP exists for read but not for create).
		// Require gap_reason so the partial status is justified in writing.
		if f.GapReason == "" {
			v.errf("%s: status=partial requires gap_reason", f.Slug)
		}
	case "gap":
		if len(missing) == 0 {
			v.errf("%s: status=gap but all required surfaces are filled — should be shipped or partial", f.Slug)
		}
		if f.GapReason == "" {
			v.errf("%s: status=gap requires gap_reason", f.Slug)
		}
	case "roadmap":
		// handled above
	default:
		v.errf("%s: invalid status %q (must be shipped|partial|gap|roadmap)", f.Slug, f.Status)
	}
}

func (v *validator) validateConfigurability(f *Feature) {
	if f.Configurability == nil {
		return
	}
	c := f.Configurability
	if c.CostAffecting {
		v.requireConfig(f, "effective_value_visible", c.EffectiveValueVisible)
		v.requireConfig(f, "audit_events", c.AuditEvents)
		v.requireConfig(f, "undo", c.Undo)
	}
	if c.SecurityAffecting {
		v.requireConfig(f, "per_resource_override", c.PerResourceOverride)
		v.requireConfig(f, "effective_value_visible", c.EffectiveValueVisible)
		v.requireConfig(f, "audit_events", c.AuditEvents)
		v.requireConfig(f, "undo", c.Undo)
	}
}

func (v *validator) requireConfig(f *Feature, field, value string) {
	if strings.TrimSpace(value) == "" {
		v.errf("%s: configurability.%s must be set", f.Slug, field)
	}
}

func (v *validator) loadSchema() error {
	path := filepath.Join(v.repoRoot, "pkg", "graphql", "schema.graphql")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	v.schemaMutations = parseTypeBlock(string(data), "Mutation")
	v.schemaQueries = parseTypeBlock(string(data), "Query")
	v.schemaSubscriptions = parseTypeBlock(string(data), "Subscription")
	v.schemaFields = parseObjectFields(string(data))
	return nil
}

// parseTypeBlock extracts top-level field names from `type <name> { ... }` blocks.
// Handles `extend type` blocks and multiple non-contiguous declarations.
// Top-level fields are indented with exactly two spaces in schema.graphql;
// argument names within field declarations are indented four or more, so
// requiring the two-space anchor avoids matching nested args as fields.
var (
	fieldRe     = regexp.MustCompile(`(?m)^  ([a-z][A-Za-z0-9_]*)\s*[(:]`)
	docstringRe = regexp.MustCompile(`(?s)"""(.*?)"""`)
)

func parseTypeBlock(schema, typeName string) map[string]bool {
	out := map[string]bool{}
	headerRe := regexp.MustCompile(`(?m)^\s*(extend\s+)?type\s+` + regexp.QuoteMeta(typeName) + `\b[^{]*\{`)
	for _, idx := range headerRe.FindAllStringIndex(schema, -1) {
		body, ok := extractBraceBlock(schema, idx[1]-1)
		if !ok {
			continue
		}
		// Strip docstrings so field-shaped lines inside descriptions don't get
		// matched (e.g. "viewers continue (possibly with...)" inside a """...""").
		body = docstringRe.ReplaceAllString(body, "")
		for _, m := range fieldRe.FindAllStringSubmatch(body, -1) {
			out[m[1]] = true
		}
	}
	return out
}

func parseObjectFields(schema string) map[string]bool {
	out := map[string]bool{}
	headerRe := regexp.MustCompile(`(?m)^\s*(extend\s+)?type\s+([A-Za-z][A-Za-z0-9_]*)\b[^{]*\{`)
	for _, m := range headerRe.FindAllStringSubmatchIndex(schema, -1) {
		typeName := schema[m[4]:m[5]]
		body, ok := extractBraceBlock(schema, m[1]-1)
		if !ok {
			continue
		}
		body = docstringRe.ReplaceAllString(body, "")
		for _, field := range fieldRe.FindAllStringSubmatch(body, -1) {
			out[typeName+"."+field[1]] = true
		}
	}
	return out
}

// extractBraceBlock returns the content between matching braces, starting at the
// position of the opening `{`.
func extractBraceBlock(s string, openIdx int) (string, bool) {
	if openIdx < 0 || openIdx >= len(s) || s[openIdx] != '{' {
		return "", false
	}
	depth := 0
	for i := openIdx; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[openIdx+1 : i], true
			}
		}
	}
	return "", false
}

var (
	mcpToolNameRe = regexp.MustCompile(`Name:\s*"([a-z][a-z0-9_]*)"`)
	// Match "panic(fmt.Errorf(\"not implemented: <Field> - <fieldName>\"))"
	// in api_gateway/graph/schema.resolvers.go. Captures the GraphQL field
	// name (the lowercase form after the dash).
	resolverStubRe = regexp.MustCompile(`panic\(fmt\.Errorf\("not implemented: [^ ]+ - ([a-z][A-Za-z0-9]*)"\)\)`)
)

// checkResolverStubs scans schema.resolvers.go for gqlgen panic stubs.
// Any surviving stub is a runtime panic for a reachable schema field, so the
// registry validator fails even when the field is not listed as a root
// operation in docs/platform-features.yaml.
func (v *validator) checkResolverStubs(_ *Registry) {
	resolversPath := filepath.Join(v.repoRoot, "api_gateway", "graph", "schema.resolvers.go")
	data, err := os.ReadFile(resolversPath)
	if err != nil {
		// Resolver file is regenerated by `make graphql`; absence isn't fatal
		// for the validator (registry can still be checked) but log it.
		fmt.Fprintf(os.Stderr, "warning: could not read %s for stub check: %v\n", resolversPath, err)
		return
	}
	stubs := map[string]bool{}
	for _, m := range resolverStubRe.FindAllStringSubmatch(string(data), -1) {
		stubs[m[1]] = true
	}
	if len(stubs) == 0 {
		return
	}
	names := make([]string, 0, len(stubs))
	for name := range stubs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		v.errf("GraphQL resolver %q is still wired to a not-implemented panic stub in schema.resolvers.go", name)
	}
}

func (v *validator) loadMCPTools() error {
	dir := filepath.Join(v.repoRoot, "api_gateway", "internal", "mcp", "tools")
	v.mcpTools = map[string]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range mcpToolNameRe.FindAllStringSubmatch(string(data), -1) {
			v.mcpTools[m[1]] = path
		}
	}
	return nil
}

// routeExists checks for a +page.svelte at the route path. Dynamic segments
// in the registry use `:name`; SvelteKit stores them as `[name]`.
func (v *validator) routeExists(route string) bool {
	if !strings.HasPrefix(route, "/") {
		return false
	}
	segs := strings.Split(strings.TrimPrefix(route, "/"), "/")
	for i, s := range segs {
		if rest, ok := strings.CutPrefix(s, ":"); ok {
			segs[i] = "[" + rest + "]"
		}
	}
	dir := filepath.Join(append([]string{v.repoRoot, "website_application", "src", "routes"}, segs...)...)
	for _, name := range []string{"+page.svelte", "+page.server.ts", "+page.ts"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

func (v *validator) docsPageExists(page string) bool {
	return fileExists(filepath.Join(v.repoRoot, "website_docs", "src", "content", "docs", page))
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func findRepoRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := cwd
	for {
		if fileExists(filepath.Join(dir, "go.work")) || fileExists(filepath.Join(dir, "Makefile")) && fileExists(filepath.Join(dir, "pkg", "graphql", "schema.graphql")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find repo root from %s", cwd)
		}
		dir = parent
	}
}

func (v *validator) errf(format string, args ...any) {
	v.errors = append(v.errors, fmt.Sprintf(format, args...))
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "registry: "+format+"\n", args...)
	os.Exit(1)
}
