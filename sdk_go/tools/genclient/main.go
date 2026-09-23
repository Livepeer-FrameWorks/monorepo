// Command genclient generates the Go SDK's typed operations: it runs
// genqlient on the given config, replaces each subscription function with
// one on the SDK's SubscriptionClient, and then opens every generated union
// to members a newer server adds.
//
// genqlient's subscription functions take its own graphql.WebSocketClient
// and deliver events on a channel. The SDK runs subscriptions on
// SubscriptionClient (graphql-transport-ws with reconnects and typed
// errors), so genclient replaces each one, with its WsResponse type and
// ForwardData helper, by Subscribe<Operation>, which passes genqlient's
// variables struct to the SDK's generic Subscribe.
//
// genqlient decodes a union by switching on __typename and fails on a
// __typename it was not generated with. The SDK declares a range of server
// releases it supports, and a later release may add a member to a union, so
// genclient rewrites each union's decode helper to store such a member as
// *UnknownMember (defined with newUnknownMember in the SDK's results.go), its
// encode helper to write the member back unchanged, and adds the marker
// method that lets *UnknownMember satisfy each union interface. The
// generated nodes carry no positions, so the printer keeps them in place.
//
//	go run ./genclient ../genqlient.yaml
package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Khan/genqlient/generate"
	"github.com/vektah/gqlparser/v2"
	gqlast "github.com/vektah/gqlparser/v2/ast"
)

const unknownMember = "UnknownMember"

var operationPattern = regexp.MustCompile("(?ms)^const ([A-Za-z0-9_]+)_Operation = `(.+?)`$")

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: genclient <genqlient.yaml>")
		os.Exit(2)
	}
	if err := run(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "genclient:", err)
		os.Exit(1)
	}
}

func run(configPath string) error {
	// genqlient resolves the generated file's package with go list from the
	// working directory, so it has to run inside the SDK module.
	if err := os.Chdir(filepath.Dir(configPath)); err != nil {
		return err
	}
	config, err := generate.ReadAndValidateConfig(filepath.Base(configPath))
	if err != nil {
		return err
	}
	schema, err := loadSchema(config.Schema)
	if err != nil {
		return err
	}
	files, err := generate.Generate(config)
	if err != nil {
		return err
	}
	for name, src := range files {
		if strings.HasSuffix(name, ".go") {
			if src, err = subscriptionFunctions(src, schema); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			if src, err = documentOperations(src, schema); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			if src, err = openUnions(src); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(name, src, 0o644); err != nil {
			return err
		}
		if filepath.Base(name) == filepath.Base(config.Generated) {
			calls, err := operationCalls(src)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			if err := os.WriteFile(filepath.Join(filepath.Dir(name), callsFile), calls, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

// callsFile holds, next to the generated operations, the test table that
// calls each of them. Its function signatures come from genqlient, so the
// table is generated from them rather than kept by hand.
const callsFile = "operation_calls_gen_test.go"

type generatedFunc struct {
	name   string
	params [][2]string // name, Go type
	ws     bool
}

// operationCalls renders the SDK test tables for the functions in generated:
// operationCalls runs each query and mutation function with fixture
// variables decoded into its parameter types, and subscriptionCalls runs each
// subscription's Subscribe function the same way.
func operationCalls(generated []byte) ([]byte, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "generated.go", generated, 0)
	if err != nil {
		return nil, err
	}
	var funcs []generatedFunc
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || !fn.Name.IsExported() || len(fn.Type.Params.List) < 2 {
			continue
		}
		var f generatedFunc
		switch client := fn.Type.Params.List[1].Type.(type) {
		case *ast.SelectorExpr:
			if client.Sel.Name != "Client" {
				continue
			}
			f = generatedFunc{name: fn.Name.Name}
		case *ast.StarExpr:
			sc, ok := client.X.(*ast.Ident)
			if !ok || sc.Name != "SubscriptionClient" || !strings.HasPrefix(fn.Name.Name, subscriptionPrefix) {
				continue
			}
			f = generatedFunc{name: fn.Name.Name, ws: true}
		default:
			continue
		}
		for _, p := range fn.Type.Params.List[2:] {
			var typ bytes.Buffer
			if err := format.Node(&typ, fset, p.Type); err != nil {
				return nil, err
			}
			for _, n := range p.Names {
				f.params = append(f.params, [2]string{n.Name, typ.String()})
			}
		}
		funcs = append(funcs, f)
	}
	sort.Slice(funcs, func(i, j int) bool { return funcs[i].name < funcs[j].name })

	var b strings.Builder
	b.WriteString("// operationCalls calls every generated query and mutation function with the\n// fixture's variables.\nvar operationCalls = map[string]operationCall{\n")
	for _, f := range funcs {
		if f.ws {
			continue
		}
		v := "v"
		if len(f.params) == 0 {
			v = "_"
		}
		fmt.Fprintf(&b, "\t%q: {%s_Operation, func(ctx context.Context, c *Client, %s vars) (any, error) {\n\t\treturn %s(ctx, c", f.name, f.name, v, f.name)
		for _, p := range f.params {
			fmt.Fprintf(&b, ", arg[%s](v, %q)", p[1], p[0])
		}
		b.WriteString(")\n\t}},\n")
	}
	b.WriteString("}\n\n// subscriptionCalls subscribes to every generated subscription through its\n// Subscribe function with the fixture's variables.\nvar subscriptionCalls = map[string]func(ctx context.Context, sc *SubscriptionClient, v vars) iter.Seq2[any, error]{\n")
	for _, f := range funcs {
		if !f.ws {
			continue
		}
		v := "v"
		if len(f.params) == 0 {
			v = "_"
		}
		fmt.Fprintf(&b, "\t%q: func(ctx context.Context, sc *SubscriptionClient, %s vars) iter.Seq2[any, error] {\n\t\treturn anyEvents(%s(ctx, sc", strings.TrimPrefix(f.name, subscriptionPrefix), v, f.name)
		for _, p := range f.params {
			fmt.Fprintf(&b, ", arg[%s](v, %q)", p[1], p[0])
		}
		b.WriteString("))\n\t},\n")
	}
	b.WriteString("}\n")
	body := b.String()
	imports := []string{"context", "iter"}
	if strings.Contains(body, "json.") {
		imports = append(imports, "encoding/json")
	}
	if strings.Contains(body, "time.") {
		imports = append(imports, "time")
	}
	sort.Strings(imports)
	var head strings.Builder
	head.WriteString("// Code generated by sdk_go/tools/genclient from generated.go. DO NOT EDIT.\n\npackage frameworks\n\nimport (\n")
	for _, imp := range imports {
		fmt.Fprintf(&head, "\t%q\n", imp)
	}
	head.WriteString(")\n\n")
	return format.Source([]byte(head.String() + body))
}

func loadSchema(paths []string) (*gqlast.Schema, error) {
	sources := make([]*gqlast.Source, 0, len(paths))
	for _, path := range paths {
		contents, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read schema %s: %w", path, err)
		}
		sources = append(sources, &gqlast.Source{Name: path, Input: string(contents)})
	}
	schema, err := gqlparser.LoadSchema(sources...)
	if err != nil {
		return nil, fmt.Errorf("parse schema: %w", err)
	}
	return schema, nil
}

// documentOperations adds schema descriptions to the generated Go functions.
func documentOperations(src []byte, schema *gqlast.Schema) ([]byte, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "generated.go", src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	descriptions := map[string]string{}
	for _, match := range operationPattern.FindAllSubmatch(src, -1) {
		name, query := string(match[1]), string(match[2])
		description, err := operationDescription(schema, query, name)
		if err != nil {
			return nil, fmt.Errorf("%s_Operation: %w", name, err)
		}
		if description != "" {
			descriptions[name] = description
		}
	}

	type insertion struct {
		offset int
		text   string
	}
	insertions := []insertion{}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv != nil {
			continue
		}
		description := descriptions[function.Name.Name]
		if description == "" {
			continue
		}
		var comment strings.Builder
		fmt.Fprintf(&comment, "// %s executes the corresponding GraphQL operation.\n//\n", function.Name.Name)
		for _, line := range strings.Split(description, "\n") {
			fmt.Fprintf(&comment, "// %s\n", line)
		}
		position := function.Pos()
		if function.Doc != nil {
			position = function.Doc.Pos()
		}
		insertions = append(insertions, insertion{
			offset: fset.Position(position).Offset,
			text:   comment.String(),
		})
	}

	sort.Slice(insertions, func(i, j int) bool { return insertions[i].offset > insertions[j].offset })
	documented := append([]byte(nil), src...)
	for _, item := range insertions {
		documented = append(documented[:item.offset], append([]byte(item.text), documented[item.offset:]...)...)
	}
	return format.Source(documented)
}

func operationDescription(schema *gqlast.Schema, source, operationName string) (string, error) {
	document, errors := gqlparser.LoadQuery(schema, source)
	if errors != nil {
		return "", errors
	}
	operation := document.Operations.ForName(operationName)
	if operation == nil {
		return "", fmt.Errorf("operation not found")
	}
	// The operation's target is its root field, or the last field of a
	// path such as analytics { usage { streaming { streamAnalyticsSummary } } }
	// (operationTarget in scripts/sdkcontract/ops.go).
	description := ""
	selections := operation.SelectionSet
	for len(selections) == 1 {
		field, ok := selections[0].(*gqlast.Field)
		if !ok || field.Definition == nil {
			break
		}
		description = field.Definition.Description
		if len(field.SelectionSet) != 1 {
			break
		}
		child, ok := field.SelectionSet[0].(*gqlast.Field)
		if !ok || len(child.SelectionSet) == 0 {
			break
		}
		selections = field.SelectionSet
	}
	return description, nil
}

// openUnions rewrites genqlient output so each union accepts *UnknownMember.
func openUnions(src []byte) ([]byte, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "generated.go", src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	interfaces := map[string]*ast.InterfaceType{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if it, ok := ts.Type.(*ast.InterfaceType); ok {
				interfaces[ts.Name.Name] = it
			}
		}
	}

	opened := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Body == nil {
			continue
		}
		switch {
		case strings.HasPrefix(fn.Name.Name, "__unmarshal"):
			union, err := openDecode(fn)
			if err != nil {
				return nil, err
			}
			opened[union] = true
		case strings.HasPrefix(fn.Name.Name, "__marshal"):
			if err := openEncode(fn); err != nil {
				return nil, err
			}
		}
	}

	names := make([]string, 0, len(opened))
	for name := range opened {
		it, ok := interfaces[name]
		if !ok {
			return nil, fmt.Errorf("union %s has a decode helper but no interface", name)
		}
		// *UnknownMember implements only GetTypename; an interface with
		// shared fields (a GraphQL interface type) needs getters it cannot
		// supply.
		for _, m := range it.Methods.List {
			if len(m.Names) != 1 {
				return nil, fmt.Errorf("union %s embeds an interface", name)
			}
			if n := m.Names[0].Name; n != "GetTypename" && n != "implementsGraphQLInterface"+name {
				return nil, fmt.Errorf("%s declares %s, which %s cannot implement", name, n, unknownMember)
			}
		}
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		file.Decls = append(file.Decls, &ast.FuncDecl{
			Recv: &ast.FieldList{List: []*ast.Field{{
				Names: []*ast.Ident{ast.NewIdent("v")},
				Type:  &ast.StarExpr{X: ast.NewIdent(unknownMember)},
			}}},
			Name: ast.NewIdent("implementsGraphQLInterface" + name),
			Type: &ast.FuncType{Params: &ast.FieldList{}},
			Body: &ast.BlockStmt{},
		})
	}

	var out bytes.Buffer
	if err := format.Node(&out, fset, file); err != nil {
		return nil, err
	}
	return format.Source(out.Bytes())
}

// openDecode replaces the default case of a decode helper's __typename
// switch, which fails, with one that stores an *UnknownMember. It returns the
// union's Go interface name.
func openDecode(fn *ast.FuncDecl) (string, error) {
	params := fn.Type.Params.List
	if len(params) != 2 {
		return "", fmt.Errorf("%s: unexpected signature", fn.Name.Name)
	}
	star, ok := params[1].Type.(*ast.StarExpr)
	if !ok {
		return "", fmt.Errorf("%s: unexpected signature", fn.Name.Name)
	}
	ident, ok := star.X.(*ast.Ident)
	if !ok {
		return "", fmt.Errorf("%s: unexpected signature", fn.Name.Name)
	}
	union := ident.Name
	clause, err := defaultClause(fn, func(s ast.Stmt) *ast.BlockStmt {
		if sw, ok := s.(*ast.SwitchStmt); ok {
			return sw.Body
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	// *v = newUnknownMember(tn.TypeName, b); return nil. The two statements
	// take the start and end positions of the statement they replace, so the
	// printer lays them out on consecutive lines.
	start, end := clause.Body[0].Pos(), clause.Body[len(clause.Body)-1].End()
	clause.Body = []ast.Stmt{
		&ast.AssignStmt{
			Lhs: []ast.Expr{&ast.StarExpr{Star: start, X: ast.NewIdent("v")}},
			Tok: token.ASSIGN,
			Rhs: []ast.Expr{&ast.CallExpr{
				Fun:  ast.NewIdent("newUnknownMember"),
				Args: []ast.Expr{&ast.SelectorExpr{X: ast.NewIdent("tn"), Sel: ast.NewIdent("TypeName")}, ast.NewIdent("b")},
			}},
		},
		&ast.ReturnStmt{Return: end, Results: []ast.Expr{ast.NewIdent("nil")}},
	}
	return union, nil
}

// openEncode adds a case to an encode helper's type switch that writes an
// *UnknownMember as the server sent it.
func openEncode(fn *ast.FuncDecl) error {
	var sw *ast.BlockStmt
	clause, err := defaultClause(fn, func(s ast.Stmt) *ast.BlockStmt {
		if ts, ok := s.(*ast.TypeSwitchStmt); ok {
			sw = ts.Body
			return ts.Body
		}
		return nil
	})
	if err != nil {
		return err
	}
	// case *UnknownMember: return v.MarshalJSON()
	unknown := &ast.CaseClause{
		List: []ast.Expr{&ast.StarExpr{X: ast.NewIdent(unknownMember)}},
		Body: []ast.Stmt{&ast.ReturnStmt{Results: []ast.Expr{&ast.CallExpr{
			Fun: &ast.SelectorExpr{X: ast.NewIdent("v"), Sel: ast.NewIdent("MarshalJSON")},
		}}}},
	}
	for i, s := range sw.List {
		if s == clause {
			sw.List = append(sw.List[:i], append([]ast.Stmt{unknown}, sw.List[i:]...)...)
			return nil
		}
	}
	return fmt.Errorf("%s: default case not found", fn.Name.Name)
}

// defaultClause returns the default case of the only switch statement at
// the top level of fn's body.
func defaultClause(fn *ast.FuncDecl, switchBody func(ast.Stmt) *ast.BlockStmt) (*ast.CaseClause, error) {
	var found *ast.CaseClause
	for _, s := range fn.Body.List {
		body := switchBody(s)
		if body == nil {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("%s: more than one switch", fn.Name.Name)
		}
		for _, c := range body.List {
			if cc, ok := c.(*ast.CaseClause); ok && cc.List == nil {
				found = cc
			}
		}
	}
	if found == nil {
		return nil, fmt.Errorf("%s: no default case", fn.Name.Name)
	}
	return found, nil
}
