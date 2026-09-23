package main

import (
	"fmt"
	"html"
	"sort"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
)

const referenceDir = "website_docs/src/content/docs/builders/api-schema"

// These entry points require credentials that an ordinary developer token
// cannot hold. The generated public reference must not expose their subtree.
var privateRootFields = map[string]string{
	"query.platform":                  "platform operator grant",
	"query.bootstrapTokensConnection": "service token",
	"mutation.createBootstrapToken":   "service token",
	"mutation.revokeBootstrapToken":   "service token",
}

func runReference(repo string, check bool) error {
	schema, err := loadSchema(repo, headRef)
	if err != nil {
		return err
	}
	ops, err := loadOperations(repo)
	if err != nil {
		return err
	}
	covered, err := rootCoverage(schema, ops)
	if err != nil {
		return err
	}
	files, err := renderReference(schema, covered)
	if err != nil {
		return err
	}
	stale, err := writeOrCheck(repo, files, check)
	if err != nil {
		return err
	}
	if len(stale) > 0 {
		return failList("GraphQL reference is stale; run make generate-graphql-reference", stale)
	}
	fmt.Printf("sdkcontract: GraphQL reference has %d pages\n", len(files))
	return nil
}

func renderReference(schema *ast.Schema, covered map[string][]string) (map[string]string, error) {
	roots := []struct {
		kind, title, slug string
		def               *ast.Definition
	}{
		{"query", "Queries", "queries", schema.Query},
		{"mutation", "Mutations", "mutations", schema.Mutation},
		{"subscription", "Subscriptions", "subscriptions", schema.Subscription},
	}
	for key := range privateRootFields {
		parts := strings.SplitN(key, ".", 2)
		found := false
		for _, root := range roots {
			if root.kind == parts[0] && root.def != nil && root.def.Fields.ForName(parts[1]) != nil {
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("private GraphQL field %s no longer exists; update its classification", key)
		}
		if len(covered[key]) > 0 {
			return nil, fmt.Errorf("private GraphQL field %s has a public SDK operation", key)
		}
	}

	reachable := map[string]bool{}
	var visitType func(string)
	visitType = func(name string) {
		if reachable[name] || isBuiltinType(name) {
			return
		}
		def := schema.Types[name]
		if def == nil {
			return
		}
		reachable[name] = true
		for _, iface := range def.Interfaces {
			visitType(iface)
		}
		for _, member := range def.Types {
			visitType(member)
		}
		for _, field := range def.Fields {
			visitType(field.Type.Name())
			for _, arg := range field.Arguments {
				visitType(arg.Type.Name())
			}
		}
	}

	files := map[string]string{}
	var index strings.Builder
	index.WriteString("---\ntitle: GraphQL schema reference\ndescription: Generated public GraphQL fields and types.\n---\n\n")
	index.WriteString("Generated from `pkg/graphql/schema.graphql` by `make generate-graphql-reference`. This reference covers GraphQL fields available to tenant and public clients. Operator-only and service-token-only entry points are excluded. A field can still require a token, a role, ownership, or a product capability; read its description and the [authentication guide](/builders/api-reference/#authentication).\n\n")
	index.WriteString("**SDK contract:** A field marked **Typed SDK document** has a named operation in the TypeScript, Go, and Python SDKs. Only that document and the schema elements it selects are checked by `make verify-api-compat` and `make verify-schema-compat`. Other fields are callable through raw GraphQL, but do not carry that SDK compatibility promise. See [SDK versions](/builders/sdks/#versions).\n\n")
	index.WriteString("## Operations\n\n")
	for _, root := range roots {
		if root.def == nil {
			continue
		}
		var fields []*ast.FieldDefinition
		for _, field := range root.def.Fields {
			if strings.HasPrefix(field.Name, "__") {
				continue
			}
			key := root.kind + "." + field.Name
			if _, private := privateRootFields[key]; private {
				continue
			}
			lower := strings.ToLower(field.Description)
			if strings.Contains(lower, "service token required") || strings.Contains(lower, "every field requires the platform_operator grant") {
				return nil, fmt.Errorf("%s requires private credentials; classify it in privateRootFields", key)
			}
			fields = append(fields, field)
			visitType(field.Type.Name())
			for _, arg := range field.Arguments {
				visitType(arg.Type.Name())
			}
		}
		sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
		path := referenceDir + "/" + root.slug + ".mdx"
		files[path] = renderRootPage(root.title, root.kind, fields, covered)
		fmt.Fprintf(&index, "- [%s (%d)](/builders/api-schema/%s/)\n", root.title, len(fields), root.slug)
	}
	index.WriteString("\n## Types\n\n")
	for _, bucket := range []string{"a-f", "g-l", "m-r", "s-z"} {
		var names []string
		for name := range reachable {
			if name == "Query" || name == "Mutation" || name == "Subscription" {
				continue
			}
			if typeBucket(name) == bucket {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		files[referenceDir+"/types-"+bucket+".mdx"] = renderTypePage(schema, bucket, names)
		fmt.Fprintf(&index, "- [Types %s (%d)](/builders/api-schema/types-%s/)\n", strings.ToUpper(bucket), len(names), bucket)
	}
	files[referenceDir+"/index.mdx"] = index.String()
	return files, nil
}

func renderRootPage(title, kind string, fields []*ast.FieldDefinition, covered map[string][]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "---\ntitle: %s\ndescription: Generated GraphQL %s reference.\n---\n\n", title, kind)
	b.WriteString("[GraphQL schema reference](/builders/api-schema/) · [API guide](/builders/api-reference/)\n\n")
	for _, field := range fields {
		fmt.Fprintf(&b, "## %s\n\n", field.Name)
		writeDescription(&b, field.Description)
		fmt.Fprintf(&b, "**Returns:** %s\n\n", typeLink(field.Type))
		if names := covered[kind+"."+field.Name]; len(names) > 0 {
			fmt.Fprintf(&b, "**Typed SDK document:** `%s`\n\n", strings.Join(names, "`, `"))
		} else {
			b.WriteString("**SDK:** Raw GraphQL only\n\n")
		}
		writeDeprecation(&b, field.Directives)
		writeArguments(&b, field.Arguments)
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func renderTypePage(schema *ast.Schema, bucket string, names []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "---\ntitle: Types %s\ndescription: Generated public GraphQL type definitions.\n---\n\n", strings.ToUpper(bucket))
	b.WriteString("[GraphQL schema reference](/builders/api-schema/) · Only types reachable from public entry points are shown.\n\n")
	for _, name := range names {
		def := schema.Types[name]
		fmt.Fprintf(&b, "## %s\n\n", name)
		fmt.Fprintf(&b, "**%s**\n\n", strings.ToLower(string(def.Kind)))
		writeDescription(&b, def.Description)
		writeDeprecation(&b, def.Directives)
		for _, iface := range def.Interfaces {
			fmt.Fprintf(&b, "Implements %s.\n\n", namedTypeLink(iface))
		}
		for _, member := range def.Types {
			fmt.Fprintf(&b, "- Member: %s\n", namedTypeLink(member))
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
			fmt.Fprintf(&b, "- **%s**: %s", field.Name, typeLink(field.Type))
			if field.Description != "" {
				fmt.Fprintf(&b, " — %s", safeText(field.Description))
			}
			if dep := deprecation(field.Directives); dep != "" {
				fmt.Fprintf(&b, " **Deprecated:** %s", dep)
			}
			b.WriteString("\n")
			for _, arg := range field.Arguments {
				fmt.Fprintf(&b, "  - `%s`: %s", arg.Name, typeLink(arg.Type))
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
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func writeArguments(b *strings.Builder, args ast.ArgumentDefinitionList) {
	if len(args) == 0 {
		return
	}
	b.WriteString("**Arguments**\n\n")
	for _, arg := range args {
		fmt.Fprintf(b, "- `%s`: %s", arg.Name, typeLink(arg.Type))
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

func typeLink(t *ast.Type) string {
	name := t.Name()
	if isBuiltinType(name) {
		return "`" + t.String() + "`"
	}
	return fmt.Sprintf("`%s` (%s)", t.String(), namedTypeLink(name))
}

func namedTypeLink(name string) string {
	return fmt.Sprintf("[%s](/builders/api-schema/types-%s/#%s)", name, typeBucket(name), strings.ToLower(name))
}

func typeBucket(name string) string {
	if name == "" {
		return "s-z"
	}
	switch first := strings.ToUpper(name[:1]); {
	case first <= "F":
		return "a-f"
	case first <= "L":
		return "g-l"
	case first <= "R":
		return "m-r"
	default:
		return "s-z"
	}
}

func isBuiltinType(name string) bool {
	switch name {
	case "String", "Int", "Float", "Boolean", "ID":
		return true
	default:
		return strings.HasPrefix(name, "__")
	}
}
