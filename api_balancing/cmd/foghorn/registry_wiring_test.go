package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// StreamRegistryInstance is a plain global: it is safe only because it is
// written before the first goroutine that reads it starts. The peer manager's
// run loop and the restream renewal loop are the first such readers.
func TestMainInstallsStreamRegistryBeforeItsReadersStart(t *testing.T) {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}
	first := map[string]token.Pos{}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := ""
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			name = fun.Name
		case *ast.SelectorExpr:
			name = fun.Sel.Name
		}
		if _, seen := first[name]; !seen && name != "" {
			first[name] = call.Pos()
		}
		return true
	})
	install, ok := first["SetStreamRegistry"]
	if !ok {
		t.Fatal("main.go does not install the stream registry")
	}
	for _, reader := range []string{"newFederationPeers", "RunRestreamCapacityRenewal"} {
		start, found := first[reader]
		if !found {
			t.Fatalf("main.go no longer calls %s; name the first reader that replaced it", reader)
		}
		if install > start {
			t.Fatalf("stream registry installed at %s, after %s starts at %s", fileSet.Position(install), reader, fileSet.Position(start))
		}
	}
}
