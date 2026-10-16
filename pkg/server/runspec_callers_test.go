package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"testing"
)

// runSpecWithoutBindAddrs are services whose listeners bind their own
// addresses: Helmsman runs on edges outside the mesh and binds the addresses
// HELMSMAN_BIND_ADDR and HELMSMAN_MANAGEMENT_BIND_ADDR name.
var runSpecWithoutBindAddrs = map[string]bool{"api_sidecar/cmd/helmsman/main.go": true}

// Every service entrypoint passes BIND_ADDRS to Run, so the loopback and
// WireGuard addresses provisioning sets reach every listener. A RunSpec
// without BindAddrs binds every interface whatever the env file says.
func TestEveryServiceRunSpecPassesBindAddrs(t *testing.T) {
	_, source, _, _ := runtime.Caller(0)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	mains, err := filepath.Glob(filepath.Join(repoRoot, "api_*", "cmd", "*", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	specs := 0
	for _, path := range mains {
		rel, _ := filepath.Rel(repoRoot, path)
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", rel, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			lit, ok := node.(*ast.CompositeLit)
			if !ok {
				return true
			}
			sel, ok := lit.Type.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "RunSpec" {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "server" {
				return true
			}
			specs++
			if runSpecWithoutBindAddrs[filepath.ToSlash(rel)] {
				return true
			}
			for _, elt := range lit.Elts {
				if kv, ok := elt.(*ast.KeyValueExpr); ok {
					if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "BindAddrs" {
						return true
					}
				}
			}
			t.Errorf("%s: server.RunSpec has no BindAddrs, so its listeners ignore BIND_ADDRS and bind every interface", rel)
			return true
		})
	}
	if specs < 15 {
		t.Fatalf("found %d server.RunSpec literals under api_*/cmd, want every service entrypoint", specs)
	}
}
