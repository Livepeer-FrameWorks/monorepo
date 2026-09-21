// Command genclient generates the Go SDK's typed operations: it runs
// genqlient on the given config and then opens every generated union to
// members a newer server adds.
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
	"sort"
	"strings"

	"github.com/Khan/genqlient/generate"
)

const unknownMember = "UnknownMember"

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
	files, err := generate.Generate(config)
	if err != nil {
		return err
	}
	for name, src := range files {
		if strings.HasSuffix(name, ".go") {
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
	}
	return nil
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
