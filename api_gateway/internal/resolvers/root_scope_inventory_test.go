package resolvers

import (
	goast "go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2"
	gqlast "github.com/vektah/gqlparser/v2/ast"
)

// Walk the source schema, including subscriptions, so adding a tenant root
// cannot silently bypass the scope checks that the public API promises.
func TestTenantRootScopeInventory(t *testing.T) {
	raw, err := os.ReadFile("../../../pkg/graphql/schema.graphql")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gqlparser.LoadSchema(&gqlast.Source{Name: "schema.graphql", Input: string(raw)})
	if err != nil {
		t.Fatal(err)
	}
	files := token.NewFileSet()
	parse := func(path string) *goast.File {
		file, parseErr := parser.ParseFile(files, path, nil, parser.ParseComments)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		return file
	}
	functions := map[string]*goast.FuncDecl{}
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		for _, decl := range parse(path).Decls {
			if fn, ok := decl.(*goast.FuncDecl); ok {
				functions[fn.Name.Name] = fn
			}
		}
	}
	public := map[string]bool{}
	for _, decl := range parse("../middleware/graphql_allowlist.go").Decls {
		gen, ok := decl.(*goast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*goast.ValueSpec)
			if !ok || len(value.Names) != 1 {
				continue
			}
			name := value.Names[0].Name
			if name != "allowlistedOperations" && name != "allowlistedMutationOperations" {
				continue
			}
			for _, v := range value.Values {
				goast.Inspect(v, func(node goast.Node) bool {
					if lit, ok := node.(*goast.BasicLit); ok && lit.Kind == token.STRING {
						key, _ := strconv.Unquote(lit.Value)
						public[key] = true
					}
					return true
				})
			}
		}
	}
	roots := map[string]map[string]*goast.FuncDecl{"Query": {}, "Mutation": {}, "Subscription": {}}
	comment := regexp.MustCompile(`is the resolver for the ([A-Za-z_][A-Za-z0-9_]*) field`)
	for _, decl := range parse("../../graph/schema.resolvers.go").Decls {
		fn, ok := decl.(*goast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 || fn.Doc == nil {
			continue
		}
		pointer, ok := fn.Recv.List[0].Type.(*goast.StarExpr)
		if !ok {
			continue
		}
		receiver, ok := pointer.X.(*goast.Ident)
		if !ok {
			continue
		}
		root := map[string]string{"queryResolver": "Query", "mutationResolver": "Mutation", "subscriptionResolver": "Subscription"}[receiver.Name]
		if root == "" {
			continue
		}
		match := comment.FindStringSubmatch(fn.Doc.Text())
		if len(match) == 2 {
			roots[root][match[1]] = fn
		}
	}
	var guarded func(*goast.FuncDecl, map[string]bool) bool
	guarded = func(fn *goast.FuncDecl, seen map[string]bool) bool {
		if fn == nil || fn.Body == nil {
			return false
		}
		// Empty marker namespaces expose no tenant data; their children authorize
		// the selected resource. A constructor carrying data cannot take this path.
		if len(fn.Body.List) == 1 {
			if ret, ok := fn.Body.List[0].(*goast.ReturnStmt); ok && len(ret.Results) == 2 {
				if ptr, ok := ret.Results[0].(*goast.UnaryExpr); ok {
					if literal, ok := ptr.X.(*goast.CompositeLit); ok && len(literal.Elts) == 0 {
						if selector, ok := literal.Type.(*goast.SelectorExpr); ok {
							if namespace, ok := selector.X.(*goast.Ident); ok && namespace.Name == "markers" {
								return true
							}
						}
					}
				}
			}
		}
		// Session/token management explicitly refuses every API token instead of
		// allowing one token to enumerate or mint another token's credentials.
		for _, statement := range fn.Body.List {
			if branch, ok := statement.(*goast.IfStmt); ok {
				if condition, ok := branch.Cond.(*goast.BinaryExpr); ok && condition.Op == token.EQL {
					call, ok := condition.X.(*goast.CallExpr)
					literal, literalOK := condition.Y.(*goast.BasicLit)
					if ok && literalOK && literal.Value == `"api_token"` {
						if selector, ok := call.Fun.(*goast.SelectorExpr); ok && selector.Sel.Name == "GetAuthType" {
							refuses := false
							goast.Inspect(branch.Body, func(node goast.Node) bool {
								if call, ok := node.(*goast.CallExpr); ok {
									if selector, ok := call.Fun.(*goast.SelectorExpr); ok && selector.Sel.Name == "Forbidden" {
										refuses = true
									}
								}
								return true
							})
							if refuses {
								return true
							}
						}
					}
				}
			}
		}
		allowed := false
		goast.Inspect(fn.Body, func(node goast.Node) bool {
			call, ok := node.(*goast.CallExpr)
			if !ok {
				return true
			}
			name := ""
			switch f := call.Fun.(type) {
			case *goast.SelectorExpr:
				name = f.Sel.Name
			case *goast.Ident:
				name = f.Name
			}
			switch name {
			case "RequirePermission", "RequireTenantAction", "RequirePlatformOperator", "PermissionGranted", "HasServiceToken", "requireInteractiveSecurity", "RequireX402Target":
				allowed = true
				return false
			}
			if !seen[name] {
				seen[name] = true
				if guarded(functions[name], seen) {
					allowed = true
				}
			}
			return !allowed
		})
		return allowed
	}
	for _, root := range []*gqlast.Definition{schema.Query, schema.Mutation, schema.Subscription} {
		for _, field := range root.Fields {
			if strings.HasPrefix(field.Name, "__") || public[strings.ToLower(field.Name)] {
				continue
			}
			if !guarded(roots[root.Name][field.Name], map[string]bool{}) {
				t.Errorf("tenant root %s.%s has no reachable scope or platform-operator check", root.Name, field.Name)
			}
		}
	}
}
