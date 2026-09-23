package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"sort"
	"strings"

	gqlast "github.com/vektah/gqlparser/v2/ast"
	"golang.org/x/tools/go/ast/astutil"
)

// subscriptionPrefix names the SDK function generated for a subscription
// operation: Subscribe<Operation>.
const subscriptionPrefix = "Subscribe"

// subscriptionFunctions replaces every genqlient subscription function (the
// ones taking a graphql.WebSocketClient), its <Operation>WsResponse type, and
// its <Operation>ForwardData helper with Subscribe<Operation>, which takes the
// same variables and runs the subscription on the SDK's SubscriptionClient.
// The variables go out as genqlient's input struct, so a variable marked
// @genqlient(omitempty: true) is left out when unset exactly as in queries.
func subscriptionFunctions(src []byte, schema *gqlast.Schema) ([]byte, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "generated.go", src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	offset := func(p token.Pos) int { return fset.Position(p).Offset }
	text := func(n ast.Node) string { return string(src[offset(n.Pos()):offset(n.End())]) }

	operations := map[string]string{}
	for _, match := range operationPattern.FindAllSubmatch(src, -1) {
		operations[string(match[1])] = string(match[2])
	}

	type edit struct {
		start, end int
		text       string
	}
	var edits []edit
	subscriptions := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || len(fn.Type.Params.List) < 2 {
			continue
		}
		client, ok := fn.Type.Params.List[1].Type.(*ast.SelectorExpr)
		if !ok || client.Sel.Name != "WebSocketClient" {
			continue
		}
		name := fn.Name.Name
		query, ok := operations[name]
		if !ok {
			return nil, fmt.Errorf("subscription %s has no %s_Operation", name, name)
		}
		description, descErr := operationDescription(schema, query, name)
		if descErr != nil {
			return nil, fmt.Errorf("%s_Operation: %w", name, descErr)
		}

		var params []string
		for _, p := range fn.Type.Params.List[2:] {
			for _, n := range p.Names {
				// The wrapper's own parameters are ctx and sc.
				if n.Name == "ctx" || n.Name == "sc" {
					return nil, fmt.Errorf("subscription %s has a variable named %s", name, n.Name)
				}
				params = append(params, n.Name+" "+text(p.Type))
			}
		}
		variables := "nil"
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			kv, ok := n.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Variables" {
				variables = text(kv.Value)
				return false
			}
			return true
		})

		var b strings.Builder
		fmt.Fprintf(&b, "// %s%s runs the %s subscription on sc and yields each\n// event; [Subscribe] describes how the sequence ends.\n", subscriptionPrefix, name, name)
		if description != "" {
			b.WriteString("//\n")
			for _, line := range strings.Split(description, "\n") {
				fmt.Fprintf(&b, "// %s\n", line)
			}
		}
		fmt.Fprintf(&b, "func %s%s(ctx context.Context, sc *SubscriptionClient", subscriptionPrefix, name)
		for _, p := range params {
			b.WriteString(", " + p)
		}
		fmt.Fprintf(&b, ") iter.Seq2[*%sResponse, error] {\n\treturn Subscribe[%sResponse](ctx, sc, %q, %s_Operation, %s)\n}", name, name, name, name, variables)

		start := fn.Pos()
		if fn.Doc != nil {
			start = fn.Doc.Pos()
		}
		edits = append(edits, edit{offset(start), offset(fn.End()), b.String()})
		subscriptions[name] = true
	}
	if len(subscriptions) == 0 {
		return src, nil
	}

	// Each subscription's channel type and forwarding helper go with it, and
	// its document's comment names the function that now executes it.
	removed := map[string]bool{}
	for _, decl := range file.Decls {
		var name string
		var doc *ast.CommentGroup
		switch d := decl.(type) {
		case *ast.GenDecl:
			if d.Tok == token.CONST && len(d.Specs) == 1 && d.Doc != nil {
				if vs, ok := d.Specs[0].(*ast.ValueSpec); ok {
					if op := strings.TrimSuffix(vs.Names[0].Name, "_Operation"); op != vs.Names[0].Name && subscriptions[op] {
						edits = append(edits, edit{offset(d.Doc.Pos()), offset(d.Doc.End()), fmt.Sprintf("// The subscription executed by %s%s.", subscriptionPrefix, op)})
						continue
					}
				}
			}
			if d.Tok != token.TYPE || len(d.Specs) != 1 {
				continue
			}
			ts, ok := d.Specs[0].(*ast.TypeSpec)
			if !ok || !strings.HasSuffix(ts.Name.Name, "WsResponse") || !subscriptions[strings.TrimSuffix(ts.Name.Name, "WsResponse")] {
				continue
			}
			name, doc = ts.Name.Name, d.Doc
		case *ast.FuncDecl:
			if d.Recv != nil || !strings.HasSuffix(d.Name.Name, "ForwardData") || !subscriptions[strings.TrimSuffix(d.Name.Name, "ForwardData")] {
				continue
			}
			name, doc = d.Name.Name, d.Doc
		default:
			continue
		}
		start := decl.Pos()
		if doc != nil {
			start = doc.Pos()
		}
		edits = append(edits, edit{offset(start), offset(decl.End()), ""})
		removed[name] = true
	}
	for name := range subscriptions {
		if !removed[name+"WsResponse"] || !removed[name+"ForwardData"] {
			return nil, fmt.Errorf("subscription %s: %sWsResponse or %sForwardData not found", name, name, name)
		}
	}

	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	out := append([]byte(nil), src...)
	for _, e := range edits {
		out = append(out[:e.start], append([]byte(e.text), out[e.end:]...)...)
	}

	// The forwarding helpers were the only users of errors in genqlient's
	// output; the wrappers return iter.Seq2.
	fset = token.NewFileSet()
	file, err = parser.ParseFile(fset, "generated.go", out, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("rewritten subscriptions: %w", err)
	}
	if !usesPackage(file, "errors") {
		astutil.DeleteImport(fset, file, "errors")
	}
	astutil.AddImport(fset, file, "iter")
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, file); err != nil {
		return nil, err
	}
	return format.Source(buf.Bytes())
}

// usesPackage reports whether file refers to an identifier of the package
// imported under name.
func usesPackage(file *ast.File, name string) bool {
	used := false
	ast.Inspect(file, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == name {
				used = true
			}
		}
		return !used
	})
	return used
}
