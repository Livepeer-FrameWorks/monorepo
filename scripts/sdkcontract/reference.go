package main

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

const (
	referenceDir    = "website_docs/src/content/docs/builders/api-schema"
	referenceRoute  = "/builders/api-schema/"
	publicSchemaURL = "https://github.com/Livepeer-FrameWorks/monorepo/blob/master/" + publicSchemaPath
	// maxReferencePageLines bounds a domain page; past it the domain's types
	// move to sub-pages split at first-letter boundaries.
	maxReferencePageLines = 1200
	sharedDomainSlug      = "shared"
)

// referenceDomain is one page of the reference. A query or mutation root
// field belongs to the domain with a keyword that occurs earliest in its
// name (a longer keyword wins a tie, then the earlier domain); every
// subscription belongs to the domain that sets Subscriptions. A root field
// no keyword matches fails the reference, so a new root field is never left
// out.
type referenceDomain struct {
	Slug, Title, Summary string
	Keywords             []string
	Subscriptions        bool
}

// referenceDomains is in page order.
var referenceDomains = []referenceDomain{
	{Slug: "streams", Title: "Streams", Summary: "Live streams, stream keys, push targets, and ingest endpoint resolution.",
		Keywords: []string{"stream", "pushtarget", "ingestendpoint"}},
	{Slug: "media", Title: "Media and storage", Summary: "Clips, DVR recordings, VOD uploads and assets, storage artifacts, retention, and media placement.",
		Keywords: []string{"clip", "dvr", "vod", "storage", "media", "streamretention", "clustermedia"}},
	{Slug: "access", Title: "Access and playback", Summary: "Playback signing keys, developer tokens, playback policy, and viewer endpoint resolution.",
		Keywords: []string{"signingkey", "developertoken", "playback", "viewerendpoint"}},
	{Slug: "live", Title: "Live subscriptions", Summary: "Every GraphQL subscription: stream, viewer, storage, processing, incident, and tenant event feeds.",
		Subscriptions: true},
	{Slug: "analytics", Title: "Analytics", Summary: "The analytics tree: streaming, viewer, quality, and usage metrics.",
		Keywords: []string{"analytics"}},
	{Slug: "webhooks", Title: "Webhooks", Summary: "Webhook endpoints, deliveries, replays, and event types.",
		Keywords: []string{"webhook"}},
	{Slug: "incidents", Title: "Incidents", Summary: "Incidents affecting the tenant, with acknowledgement, assignment, notes, and resolution.",
		Keywords: []string{"incident"}},
	{Slug: "account", Title: "Account and tenant", Summary: "The tenant, wallet and email identities.",
		Keywords: []string{"tenant", "wallet", "email"}},
	{Slug: "billing", Title: "Billing and usage", Summary: "Billing tiers, invoices, payments, top-ups, prepaid balance, and usage records.",
		Keywords: []string{"billing", "invoice", "payment", "mollie", "prepaid", "balance", "stripe", "topup", "x402", "paid", "usage", "tenantusage"}},
	{Slug: "clusters", Title: "Clusters and infrastructure", Summary: "Clusters, the marketplace, subscriptions and invites, edge enrollment, nodes, services, and orchestrators.",
		Keywords: []string{"cluster", "nodes", "nodemode", "orchestrator", "service", "edge", "enrollment", "mist", "subscription"}},
	{Slug: "platform", Title: "Platform", Summary: "Server info, capabilities, network status, and global object lookup by ID.",
		Keywords: []string{"serverinfo", "capabilities", "networkstatus", "node"}},
	{Slug: "skipper", Title: "Skipper", Summary: "Skipper, the AI assistant: conversations and reports.",
		Keywords: []string{"skipper"}},
	{Slug: "support", Title: "Support conversations", Summary: "Support conversations and their messages.",
		Keywords: []string{"conversation", "message"}},
}

// rootField is one public root field.
type rootField struct {
	Kind  string
	Field *ast.FieldDefinition
}

func (r rootField) key() string { return r.Kind + "." + r.Field.Name }

// heading is the field's heading, which is also its anchor source.
func (r rootField) heading() string {
	return strings.ToUpper(r.Kind[:1]) + r.Kind[1:] + "." + r.Field.Name
}

// sdkBinding is one SDK operation that selects a root field. Path runs from
// the root field to the field the operation targets (operation.Target).
type sdkBinding struct {
	Operation, Kind string
	Path            []string
}

func runReference(repo string, check bool) error {
	full, err := loadFullSchema(repo, headRef)
	if err != nil {
		return err
	}
	if problems := undeclaredPrivateFields(full); len(problems) > 0 {
		return failList("fields need @internal", problems)
	}
	schema, err := publicSchema(full)
	if err != nil {
		return err
	}
	ops, err := loadOperations(repo)
	if err != nil {
		return err
	}
	bindings, err := sdkBindings(schema, ops)
	if err != nil {
		return err
	}
	files, err := renderReference(schema, bindings)
	if err != nil {
		return err
	}
	stale, err := writeOrCheck(repo, files, check)
	if err != nil {
		return err
	}
	removed, err := pruneReference(repo, files, check)
	if err != nil {
		return err
	}
	stale = append(stale, removed...)
	if len(stale) > 0 {
		return failList("GraphQL reference is stale; run make generate-graphql-reference", stale)
	}
	fmt.Printf("sdkcontract: GraphQL reference has %d pages\n", len(files))
	return nil
}

// pruneReference removes the pages in the reference directory that the
// reference no longer renders; with check it only lists them.
func pruneReference(repo string, files map[string]string, check bool) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(repo, referenceDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var stale []string
	for _, entry := range entries {
		rel := referenceDir + "/" + entry.Name()
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".mdx" || files[rel] != "" {
			continue
		}
		stale = append(stale, rel)
		if !check {
			if err := os.Remove(filepath.Join(repo, rel)); err != nil {
				return nil, err
			}
		}
	}
	if check {
		return stale, nil
	}
	return nil, nil
}

// sdkBindings maps each root field key ("query.stream") to the operations
// that select it, sorted by operation name.
func sdkBindings(schema *ast.Schema, ops []operation) (map[string][]sdkBinding, error) {
	out := map[string][]sdkBinding{}
	for _, op := range ops {
		doc, err := parser.ParseQuery(&ast.Source{Name: op.File, Input: op.Document})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", op.File, err)
		}
		for _, definition := range doc.Operations {
			rootType := rootDefinition(schema, definition.Operation)
			if rootType == nil {
				return nil, fmt.Errorf("%s: schema has no %s root", op.Name, definition.Operation)
			}
			for _, selection := range definition.SelectionSet {
				field, ok := selection.(*ast.Field)
				if !ok {
					return nil, fmt.Errorf("%s: root selection must be a field", op.Name)
				}
				if rootType.Fields.ForName(field.Name) == nil {
					return nil, fmt.Errorf("%s: %s.%s is not in the public schema", op.Name, definition.Operation, field.Name)
				}
				key := string(definition.Operation) + "." + field.Name
				path := []string{field.Name}
				if target := strings.Split(op.Target, "."); len(target) > 1 && target[0] == string(definition.Operation) && target[1] == field.Name {
					path = target[1:]
				}
				out[key] = append(out[key], sdkBinding{Operation: op.Name, Kind: string(definition.Operation), Path: path})
			}
		}
	}
	for key := range out {
		sort.Slice(out[key], func(i, j int) bool { return out[key][i].Operation < out[key][j].Operation })
	}
	return out, nil
}

func rootDefinition(schema *ast.Schema, op ast.Operation) *ast.Definition {
	switch op {
	case ast.Query:
		return schema.Query
	case ast.Mutation:
		return schema.Mutation
	case ast.Subscription:
		return schema.Subscription
	}
	return nil
}

// publicRootFields returns the root fields of a public schema in query,
// mutation, subscription order, each sorted by name.
func publicRootFields(schema *ast.Schema) []rootField {
	var out []rootField
	for _, root := range []struct {
		kind string
		def  *ast.Definition
	}{{"query", schema.Query}, {"mutation", schema.Mutation}, {"subscription", schema.Subscription}} {
		if root.def == nil {
			continue
		}
		var fields []rootField
		for _, field := range root.def.Fields {
			if !strings.HasPrefix(field.Name, "__") {
				fields = append(fields, rootField{Kind: root.kind, Field: field})
			}
		}
		sort.Slice(fields, func(i, j int) bool { return fields[i].Field.Name < fields[j].Field.Name })
		out = append(out, fields...)
	}
	return out
}

// assignDomains maps each root field key to its index in domains. Problems
// name the root fields no domain claims, keywords that claim no field, and
// domains without root fields.
func assignDomains(domains []referenceDomain, roots []rootField) (map[string]int, []string) {
	assigned := map[string]int{}
	usedKeyword := map[string]bool{}
	var problems []string
	for _, root := range roots {
		best, bestPos, bestLen, bestKeyword := -1, 0, 0, ""
		name := strings.ToLower(root.Field.Name)
		for i, domain := range domains {
			if root.Kind == "subscription" {
				if domain.Subscriptions && best < 0 {
					best = i
				}
				continue
			}
			for _, keyword := range domain.Keywords {
				pos := strings.Index(name, keyword)
				if pos < 0 {
					continue
				}
				if best < 0 || pos < bestPos || (pos == bestPos && len(keyword) > bestLen) {
					best, bestPos, bestLen, bestKeyword = i, pos, len(keyword), domains[i].Slug+":"+keyword
				}
			}
		}
		if best < 0 {
			problems = append(problems, root.key()+": no reference domain claims this root field; add a keyword to referenceDomains in scripts/sdkcontract/reference.go")
			continue
		}
		assigned[root.key()] = best
		usedKeyword[bestKeyword] = true
	}
	owned := make([]int, len(domains))
	for _, i := range assigned {
		owned[i]++
	}
	for i, domain := range domains {
		if owned[i] == 0 {
			problems = append(problems, "domain "+domain.Slug+" has no root fields")
		}
		for _, keyword := range domain.Keywords {
			if !usedKeyword[domain.Slug+":"+keyword] {
				problems = append(problems, fmt.Sprintf("domain %s keyword %q claims no root field", domain.Slug, keyword))
			}
		}
	}
	sort.Strings(problems)
	return assigned, problems
}

// placeTypes returns, for every named type of the public schema except the
// root types, the index of the one domain that reaches it, or -1 when two or
// more domains reach it or none does. A domain reaches the output and
// argument types of its root fields, and from each type its fields, field
// arguments, interfaces, and union members. An interface does not reach its
// implementations: the global node lookup returns Node, and that alone must
// not pull every Node type onto one page.
func placeTypes(schema *ast.Schema, roots []rootField, assigned map[string]int, domainCount int) map[string]int {
	reached := make([]map[string]bool, domainCount)
	for i := range reached {
		reached[i] = map[string]bool{}
	}
	var visit func(seen map[string]bool, name string)
	visit = func(seen map[string]bool, name string) {
		def := schema.Types[name]
		if seen[name] || isBuiltinType(name) || def == nil || def.BuiltIn {
			return
		}
		seen[name] = true
		for _, iface := range def.Interfaces {
			visit(seen, iface)
		}
		for _, member := range def.Types {
			visit(seen, member)
		}
		for _, field := range def.Fields {
			visit(seen, field.Type.Name())
			for _, arg := range field.Arguments {
				visit(seen, arg.Type.Name())
			}
		}
	}
	for _, root := range roots {
		i, ok := assigned[root.key()]
		if !ok {
			continue
		}
		visit(reached[i], root.Field.Type.Name())
		for _, arg := range root.Field.Arguments {
			visit(reached[i], arg.Type.Name())
		}
	}
	rootNames := map[string]bool{}
	for _, def := range []*ast.Definition{schema.Query, schema.Mutation, schema.Subscription} {
		if def != nil {
			rootNames[def.Name] = true
		}
	}
	placement := map[string]int{}
	for name, def := range schema.Types {
		if def.BuiltIn || isBuiltinType(name) || rootNames[name] {
			continue
		}
		home := -1
		for i := range reached {
			if !reached[i][name] {
				continue
			}
			if home >= 0 {
				home = -1
				break
			}
			home = i
		}
		placement[name] = home
	}
	return placement
}

// referencePage is one rendered page of the reference.
type referencePage struct {
	Slug, Title, Description string
	Order                    int
	Body                     string
}

// referenceLayout decides which page each type is rendered on.
type referenceLayout struct {
	schema   *ast.Schema
	typePage map[string]string
}

func (l *referenceLayout) namedTypeLink(name string) string {
	return fmt.Sprintf("[%s](%s%s/#%s)", name, referenceRoute, l.typePage[name], strings.ToLower(name))
}

func (l *referenceLayout) typeLink(t *ast.Type) string {
	name := t.Name()
	if isBuiltinType(name) {
		return "`" + t.String() + "`"
	}
	return fmt.Sprintf("`%s` (%s)", t.String(), l.namedTypeLink(name))
}

// renderReference renders the reference pages of a public schema (see
// publicSchema); every field it holds is published. The result maps
// repository paths to page contents.
func renderReference(schema *ast.Schema, bindings map[string][]sdkBinding) (map[string]string, error) {
	return renderReferenceDomains(schema, bindings, referenceDomains)
}

func renderReferenceDomains(schema *ast.Schema, bindings map[string][]sdkBinding, domains []referenceDomain) (map[string]string, error) {
	roots := publicRootFields(schema)
	assigned, problems := assignDomains(domains, roots)
	if len(problems) > 0 {
		return nil, failList("GraphQL reference domains are incomplete", problems)
	}
	placement := placeTypes(schema, roots, assigned, len(domains))

	// Every domain plus the shared page, in page order. The last group holds
	// the types two or more domains reach.
	type group struct {
		domain referenceDomain
		roots  []rootField
		types  []string
	}
	groups := make([]group, len(domains)+1)
	for i, domain := range domains {
		groups[i].domain = domain
	}
	groups[len(domains)].domain = referenceDomain{
		Slug:    sharedDomainSlug,
		Title:   "Shared types",
		Summary: "Types that root fields of two or more domains reach: scalars, errors, pagination, and the core objects the domains have in common.",
	}
	for _, root := range roots {
		i := assigned[root.key()]
		groups[i].roots = append(groups[i].roots, root)
	}
	for _, name := range sortedKeys(placement) {
		i := placement[name]
		if i < 0 {
			i = len(domains)
		}
		groups[i].types = append(groups[i].types, name)
	}

	layout := &referenceLayout{schema: schema, typePage: map[string]string{}}
	type plannedPage struct {
		slug, title string
		types       []string
	}
	type plannedGroup struct {
		inline bool
		pages  []plannedPage
	}
	plans := make([]plannedGroup, len(groups))
	for gi, g := range groups {
		rootLines := 0
		for _, root := range g.roots {
			rootLines += strings.Count(layout.renderRoot(root, bindings[root.key()]), "\n")
		}
		typeLines := 0
		for _, name := range g.types {
			typeLines += strings.Count(layout.renderType(name, 3), "\n")
		}
		if rootLines+typeLines <= maxReferencePageLines {
			plans[gi].inline = true
			for _, name := range g.types {
				layout.typePage[name] = g.domain.Slug
			}
			continue
		}
		for _, chunk := range chunkByInitial(g.types, func(name string) int {
			return strings.Count(layout.renderType(name, 2), "\n")
		}) {
			page := plannedPage{types: chunk, slug: g.domain.Slug + "-types", title: g.domain.Title + " types"}
			if g.domain.Slug == sharedDomainSlug {
				page.slug, page.title = g.domain.Slug, g.domain.Title
			}
			plans[gi].pages = append(plans[gi].pages, page)
		}
		if len(plans[gi].pages) > 1 {
			for pi := range plans[gi].pages {
				page := &plans[gi].pages[pi]
				first, last := initial(page.types[0]), initial(page.types[len(page.types)-1])
				page.slug = fmt.Sprintf("%s-types-%s-%s", g.domain.Slug, strings.ToLower(first), strings.ToLower(last))
				page.title = fmt.Sprintf("%s %s–%s", strings.TrimSuffix(g.domain.Title, " types")+" types", first, last)
			}
		}
		for _, page := range plans[gi].pages {
			for _, name := range page.types {
				layout.typePage[name] = page.slug
			}
		}
	}

	var pages []referencePage
	order := 1
	for gi, g := range groups {
		plan := plans[gi]
		isShared := g.domain.Slug == sharedDomainSlug
		if !isShared || plan.inline || plan.pages[0].slug != g.domain.Slug {
			var b strings.Builder
			fmt.Fprintf(&b, "%s\n\n", referenceNav)
			fmt.Fprintf(&b, "%s\n\n", safeText(g.domain.Summary))
			if !isShared {
				fmt.Fprintf(&b, "Types used by more than one domain are on [Shared types](%s%s/).\n\n", referenceRoute, sharedDomainSlug)
			}
			for _, kind := range []struct{ kind, title string }{{"query", "Queries"}, {"mutation", "Mutations"}, {"subscription", "Subscriptions"}} {
				var section []rootField
				for _, root := range g.roots {
					if root.Kind == kind.kind {
						section = append(section, root)
					}
				}
				if len(section) == 0 {
					continue
				}
				fmt.Fprintf(&b, "## %s\n\n", kind.title)
				for _, root := range section {
					b.WriteString(layout.renderRoot(root, bindings[root.key()]))
				}
			}
			if len(g.types) > 0 {
				b.WriteString("## Types\n\n")
				if plan.inline {
					for _, name := range g.types {
						b.WriteString(layout.renderType(name, 3))
					}
				} else {
					for _, page := range plan.pages {
						links := make([]string, 0, len(page.types))
						for _, name := range page.types {
							links = append(links, layout.namedTypeLink(name))
						}
						fmt.Fprintf(&b, "- [%s](%s%s/): %s\n", page.title, referenceRoute, page.slug, strings.Join(links, ", "))
					}
					b.WriteString("\n")
				}
			}
			pages = append(pages, referencePage{
				Slug: g.domain.Slug, Title: g.domain.Title, Order: order,
				Description: g.domain.Title + " in the generated public GraphQL reference.",
				Body:        b.String(),
			})
			order++
		}
		if plan.inline {
			continue
		}
		for _, page := range plan.pages {
			var b strings.Builder
			fmt.Fprintf(&b, "%s · Types of [%s](%s%s/)\n\n", referenceNav, g.domain.Title, referenceRoute, g.domain.Slug)
			if page.slug == g.domain.Slug {
				b.Reset()
				fmt.Fprintf(&b, "%s\n\n%s\n\n", referenceNav, safeText(g.domain.Summary))
			}
			for _, name := range page.types {
				b.WriteString(layout.renderType(name, 2))
			}
			pages = append(pages, referencePage{
				Slug: page.slug, Title: page.title, Order: order,
				Description: page.title + " in the generated public GraphQL reference.",
				Body:        b.String(),
			})
			order++
		}
	}

	files := map[string]string{}
	for _, page := range pages {
		files[referenceDir+"/"+page.Slug+".mdx"] = page.render()
	}
	files[referenceDir+"/index.mdx"] = renderReferenceIndex(groups[len(groups)-1].types, func(i int) (referenceDomain, []rootField, []string) {
		return groups[i].domain, groups[i].roots, groups[i].types
	}, len(domains))
	return files, nil
}

const referenceNav = "[GraphQL schema reference](" + referenceRoute + ") · [API guide](/builders/api-reference/) · [SDKs](/builders/sdks/)"

func (p referencePage) render() string {
	return fmt.Sprintf("---\ntitle: %s\ndescription: %s\nsidebar:\n  order: %d\n---\n\n%s", p.Title, p.Description, p.Order, strings.TrimRight(p.Body, "\n")+"\n")
}

func renderReferenceIndex(shared []string, group func(int) (referenceDomain, []rootField, []string), domainCount int) string {
	var b strings.Builder
	b.WriteString("---\ntitle: GraphQL schema reference\ndescription: Generated public GraphQL fields and types, by domain.\nsidebar:\n  order: 0\n---\n\n")
	fmt.Fprintf(&b, "Generated from the public schema [`%s`](%s) by `make generate-graphql-reference`. This reference covers GraphQL fields available to tenant and public clients. Operator-only and service-token-only fields, marked `@internal` in `pkg/graphql/schema.graphql`, are not part of the public schema. A field can still require a token, a role, ownership, or a product capability; read its description and the [authentication guide](/builders/api-reference/#authentication).\n\n", publicSchemaPath, publicSchemaURL)
	b.WriteString("## Domains\n\n")
	b.WriteString("| Domain | Queries | Mutations | Subscriptions | Types |\n| --- | --- | --- | --- | --- |\n")
	for i := 0; i < domainCount; i++ {
		domain, roots, types := group(i)
		counts := map[string]int{}
		for _, root := range roots {
			counts[root.Kind]++
		}
		fmt.Fprintf(&b, "| [%s](%s%s/) | %d | %d | %d | %d |\n", domain.Title, referenceRoute, domain.Slug, counts["query"], counts["mutation"], counts["subscription"], len(types))
	}
	fmt.Fprintf(&b, "| [Shared types](%s%s/) | – | – | – | %d |\n\n", referenceRoute, sharedDomainSlug, len(shared))
	b.WriteString("## How to read this reference\n\n")
	b.WriteString("- Each domain page lists its root queries, mutations, and subscriptions, then the types that only that domain's root fields reach. Types reached from two or more domains are on [Shared types](" + referenceRoute + sharedDomainSlug + "/).\n")
	b.WriteString("- A root field shows its GraphQL signature, return type, arguments with defaults, description, and deprecation. Every type name links to its definition.\n")
	b.WriteString("- **Experimental until vX.Y.Z** marks a field that is public but not stable yet: it graduates or is removed by that platform release, and it is exempt from the schema compatibility check until then.\n")
	b.WriteString("- **SDK** lists the generated operations that select a root field, with the TypeScript document, Go function, and Python method names. Python generates subscriptions in its async client only. Every root field that is not deprecated has one; a deprecated field is reachable through each SDK's raw GraphQL client. See the [SDK guide](/builders/sdks/).\n")
	return b.String()
}

// renderRoot renders one root field as a level-3 section.
func (l *referenceLayout) renderRoot(root rootField, bindings []sdkBinding) string {
	var b strings.Builder
	field := root.Field
	fmt.Fprintf(&b, "### %s\n\n", root.heading())
	writeExperimental(&b, field)
	writeDeprecation(&b, field.Directives)
	writeDescription(&b, field.Description)
	b.WriteString("```graphql\n")
	b.WriteString(fieldSignature(field))
	b.WriteString("\n```\n\n")
	fmt.Fprintf(&b, "**Returns:** %s\n\n", l.typeLink(field.Type))
	l.writeArguments(&b, field.Arguments)
	writeSDKBindings(&b, field, bindings)
	return b.String()
}

func fieldSignature(field *ast.FieldDefinition) string {
	if len(field.Arguments) == 0 {
		return field.Name + ": " + field.Type.String()
	}
	var b strings.Builder
	b.WriteString(field.Name + "(\n")
	for _, arg := range field.Arguments {
		fmt.Fprintf(&b, "  %s: %s", arg.Name, arg.Type.String())
		if arg.DefaultValue != nil {
			fmt.Fprintf(&b, " = %s", arg.DefaultValue.String())
		}
		b.WriteString("\n")
	}
	b.WriteString("): " + field.Type.String())
	return b.String()
}

func writeSDKBindings(b *strings.Builder, field *ast.FieldDefinition, bindings []sdkBinding) {
	if len(bindings) == 0 {
		if isDeprecated(field.Directives) {
			b.WriteString("**SDK:** none; deprecated fields get no generated operation. Use the replacement the deprecation names, or send a GraphQL document through the SDK's raw client.\n\n")
			return
		}
		b.WriteString("**SDK:** no generated operation; send a GraphQL document through the SDK's raw client.\n\n")
		return
	}
	nested := false
	for _, binding := range bindings {
		nested = nested || len(binding.Path) > 1
	}
	b.WriteString("**SDK**\n\n")
	if nested {
		b.WriteString("| Operation | Selects | TypeScript | Go | Python |\n| --- | --- | --- | --- | --- |\n")
	} else {
		b.WriteString("| Operation | TypeScript | Go | Python |\n| --- | --- | --- | --- |\n")
	}
	for _, binding := range bindings {
		python := "`" + pythonMethodName(binding.Operation) + "`"
		if binding.Kind == string(ast.Subscription) {
			python += " (async)"
		}
		fmt.Fprintf(b, "| %s ", binding.Operation)
		if nested {
			fmt.Fprintf(b, "| `%s` ", strings.Join(binding.Path, "."))
		}
		fmt.Fprintf(b, "| `%sDocument` | `%s` | %s |\n", binding.Operation, goFunctionName(binding.Operation, binding.Kind), python)
	}
	b.WriteString("\n")
}

// goFunctionName is the Go SDK function for an operation: genqlient's
// operation name, and Subscribe<Operation> for a subscription, which
// sdk_go/tools/genclient generates on the SDK's SubscriptionClient.
func goFunctionName(operation, kind string) string {
	if kind == string(ast.Subscription) {
		return "Subscribe" + operation
	}
	return operation
}

// pythonMethodName is ariadne-codegen's str_to_snake_case: words are a
// lower-case run with an optional leading capital, an upper-case run that
// stops before a capital followed by a lower-case letter, or a digit run.
func pythonMethodName(name string) string {
	runes := []rune(name)
	var words []string
	for i := 0; i < len(runes); {
		r := runes[i]
		switch {
		case unicode.IsDigit(r):
			j := i
			for j < len(runes) && unicode.IsDigit(runes[j]) {
				j++
			}
			words = append(words, string(runes[i:j]))
			i = j
		case unicode.IsUpper(r):
			j := i
			for j < len(runes) && unicode.IsUpper(runes[j]) {
				j++
			}
			if j < len(runes) && unicode.IsLower(runes[j]) {
				if j-1 > i {
					words = append(words, string(runes[i:j-1]))
				}
				i = j - 1
				j++
				for j < len(runes) && unicode.IsLower(runes[j]) {
					j++
				}
			}
			words = append(words, string(runes[i:j]))
			i = j
		case unicode.IsLower(r):
			j := i
			for j < len(runes) && unicode.IsLower(runes[j]) {
				j++
			}
			words = append(words, string(runes[i:j]))
			i = j
		default:
			i++
		}
	}
	return strings.ToLower(strings.Join(words, "_"))
}

// renderType renders one named type with a heading at level.
func (l *referenceLayout) renderType(name string, level int) string {
	var b strings.Builder
	def := l.schema.Types[name]
	fmt.Fprintf(&b, "%s %s\n\n", strings.Repeat("#", level), name)
	fmt.Fprintf(&b, "**%s**\n\n", strings.ToLower(string(def.Kind)))
	writeDescription(&b, def.Description)
	writeDeprecation(&b, def.Directives)
	for _, iface := range def.Interfaces {
		fmt.Fprintf(&b, "Implements %s.\n\n", l.namedTypeLink(iface))
	}
	if def.Kind == ast.Interface {
		var impls []string
		for _, impl := range l.schema.PossibleTypes[name] {
			impls = append(impls, impl.Name)
		}
		sort.Strings(impls)
		for i, impl := range impls {
			impls[i] = l.namedTypeLink(impl)
		}
		if len(impls) > 0 {
			fmt.Fprintf(&b, "Implemented by %s.\n\n", strings.Join(impls, ", "))
		}
	}
	for _, member := range def.Types {
		fmt.Fprintf(&b, "- Member: %s\n", l.namedTypeLink(member))
	}
	if len(def.Types) > 0 {
		b.WriteString("\n")
	}
	for _, value := range def.EnumValues {
		fmt.Fprintf(&b, "- `%s`", value.Name)
		if value.Description != "" {
			fmt.Fprintf(&b, " — %s", safeText(value.Description))
		}
		if dep := deprecation(value.Directives); dep != "" {
			fmt.Fprintf(&b, " **Deprecated:** %s", dep)
		}
		b.WriteString("\n")
	}
	if len(def.EnumValues) > 0 {
		b.WriteString("\n")
	}
	for _, field := range def.Fields {
		fmt.Fprintf(&b, "- **%s**: %s", field.Name, l.typeLink(field.Type))
		if _, until, ok := experimentalMark(field); ok {
			fmt.Fprintf(&b, " **Experimental until %s.**", safeText(until))
		}
		if field.Description != "" {
			fmt.Fprintf(&b, " — %s", safeText(field.Description))
		}
		if dep := deprecation(field.Directives); dep != "" {
			fmt.Fprintf(&b, " **Deprecated:** %s", dep)
		}
		b.WriteString("\n")
		for _, arg := range field.Arguments {
			fmt.Fprintf(&b, "  - `%s`: %s", arg.Name, l.typeLink(arg.Type))
			if arg.DefaultValue != nil {
				fmt.Fprintf(&b, " (default `%s`)", safeText(arg.DefaultValue.String()))
			}
			if arg.Description != "" {
				fmt.Fprintf(&b, " — %s", safeText(arg.Description))
			}
			if dep := deprecation(arg.Directives); dep != "" {
				fmt.Fprintf(&b, " **Deprecated:** %s", dep)
			}
			b.WriteString("\n")
		}
	}
	if len(def.Fields) > 0 {
		b.WriteString("\n")
	}
	return b.String()
}

// chunkByInitial splits sorted type names into pages of at most
// maxReferencePageLines, breaking only between first letters so page slugs
// name a letter range. A single letter over the limit gets a page of its own.
func chunkByInitial(names []string, lines func(string) int) [][]string {
	var chunks [][]string
	var current []string
	size := 0
	for start := 0; start < len(names); {
		end := start
		letterLines := 0
		for end < len(names) && initial(names[end]) == initial(names[start]) {
			letterLines += lines(names[end])
			end++
		}
		if len(current) > 0 && size+letterLines > maxReferencePageLines {
			chunks = append(chunks, current)
			current, size = nil, 0
		}
		current = append(current, names[start:end]...)
		size += letterLines
		start = end
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks
}

func initial(name string) string {
	return strings.ToUpper(name[:1])
}

func writeExperimental(b *strings.Builder, field *ast.FieldDefinition) {
	if reason, until, ok := experimentalMark(field); ok {
		fmt.Fprintf(b, "**Experimental until %s:** %s\n\n", safeText(until), safeText(reason))
	}
}

func (l *referenceLayout) writeArguments(b *strings.Builder, args ast.ArgumentDefinitionList) {
	if len(args) == 0 {
		return
	}
	b.WriteString("**Arguments**\n\n")
	for _, arg := range args {
		fmt.Fprintf(b, "- `%s`: %s", arg.Name, l.typeLink(arg.Type))
		if arg.DefaultValue != nil {
			fmt.Fprintf(b, " (default `%s`)", safeText(arg.DefaultValue.String()))
		}
		if arg.Description != "" {
			fmt.Fprintf(b, " — %s", safeText(arg.Description))
		}
		if dep := deprecation(arg.Directives); dep != "" {
			fmt.Fprintf(b, " **Deprecated:** %s", dep)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
}

func writeDescription(b *strings.Builder, description string) {
	if description != "" {
		fmt.Fprintf(b, "%s\n\n", safeText(description))
	}
}

func writeDeprecation(b *strings.Builder, directives ast.DirectiveList) {
	if dep := deprecation(directives); dep != "" {
		fmt.Fprintf(b, "**Deprecated:** %s\n\n", dep)
	}
}

func deprecation(directives ast.DirectiveList) string {
	d := directives.ForName("deprecated")
	if d == nil {
		return ""
	}
	if reason := d.Arguments.ForName("reason"); reason != nil {
		return safeText(reason.Value.Raw)
	}
	return "Use a replacement where available."
}

func safeText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = html.EscapeString(s)
	s = strings.ReplaceAll(s, "{", "&#123;")
	s = strings.ReplaceAll(s, "}", "&#125;")
	s = strings.ReplaceAll(s, "|", "&#124;")
	return s
}

func isBuiltinType(name string) bool {
	switch name {
	case "String", "Int", "Float", "Boolean", "ID":
		return true
	default:
		return strings.HasPrefix(name, "__")
	}
}
