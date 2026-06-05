// Command proto-split-codemod rewrites every consumer of the old flat
// github.com/Livepeer-FrameWorks/monorepo/pkg/proto package to the new
// per-.proto sub-packages (commonpb, ipcpb, commodorepb, ...).
//
// It builds a symbol->subpackage map from the generated pkg/proto/*/*.pb.go
// files (the map is collision-free), then for each consumer .go file:
//   - finds the import of the old flat path (aliased pb, aliased proto, or
//     unaliased default name "proto"),
//   - rewrites every `<local>.Sym` selector to `<subpkgpb>.Sym`,
//   - replaces the single old import with one import per used sub-package.
//
// It fails hard (non-zero exit, nothing written) if it cannot account for a
// use of the local proto name, so no reference is ever silently dropped.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/ast/astutil"
)

const oldPath = "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

// target is the destination sub-package for a symbol.
type target struct {
	importPath string // e.g. .../pkg/proto/common
	alias      string // package name, e.g. commonpb
}

func main() {
	repoRoot := flag.String("repo", "", "absolute path to monorepo root")
	apply := flag.Bool("apply", false, "write changes (default: dry run)")
	flag.Parse()
	if *repoRoot == "" {
		fmt.Fprintln(os.Stderr, "usage: proto-split-codemod -repo <monorepo-root> [-apply]")
		os.Exit(2)
	}

	protoRoot := filepath.Join(*repoRoot, "pkg", "proto")
	symMap, err := buildSymbolMap(protoRoot)
	if err != nil {
		fmt.Fprintln(os.Stderr, "symbol map:", err)
		os.Exit(1)
	}
	fmt.Printf("symbol map: %d exported symbols across sub-packages\n", len(symMap))

	files, err := consumerFiles(*repoRoot)
	if err != nil {
		fmt.Fprintln(os.Stderr, "walk:", err)
		os.Exit(1)
	}

	// Pass 1: validate and compute every rewrite in memory. Nothing is written
	// until all files succeed, so a hard failure leaves the tree untouched.
	var planned []*result
	var hardErrors []string
	for _, path := range files {
		res, err := rewriteFile(path, symMap)
		if err != nil {
			hardErrors = append(hardErrors, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		if res == nil {
			continue // did not import old path
		}
		planned = append(planned, res)
		aliases := make([]string, 0, len(res.used))
		for a := range res.used {
			aliases = append(aliases, a)
		}
		sort.Strings(aliases)
		fmt.Printf("rewrote %s -> [%s]\n", rel(*repoRoot, path), strings.Join(aliases, " "))
	}

	fmt.Printf("\n%d files rewritten\n", len(planned))
	if len(hardErrors) > 0 {
		fmt.Fprintf(os.Stderr, "\n%d FILES FAILED (nothing written):\n", len(hardErrors))
		for _, e := range hardErrors {
			fmt.Fprintln(os.Stderr, "  "+e)
		}
		os.Exit(1)
	}

	// Pass 2: all files validated — write.
	if !*apply {
		fmt.Println("(dry run — re-run with -apply to write)")
		return
	}
	for _, res := range planned {
		if err := os.WriteFile(res.path, res.newSrc, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write %s: %v\n", res.path, err)
			os.Exit(1)
		}
	}
}

// buildSymbolMap parses every generated *.pb.go under each sub-directory of
// protoRoot and maps each exported top-level identifier to its sub-package.
func buildSymbolMap(protoRoot string) (map[string]target, error) {
	entries, err := os.ReadDir(protoRoot)
	if err != nil {
		return nil, err
	}
	symMap := map[string]target{}
	owner := map[string]string{} // symbol -> dir (collision detection)
	fset := token.NewFileSet()

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := e.Name()
		subDir := filepath.Join(protoRoot, dir)
		pbFiles, err := filepath.Glob(filepath.Join(subDir, "*.pb.go"))
		if err != nil {
			return nil, fmt.Errorf("glob %s: %w", subDir, err)
		}
		if len(pbFiles) == 0 {
			continue
		}
		var pkgName string
		for _, pf := range pbFiles {
			f, err := parser.ParseFile(fset, pf, nil, parser.SkipObjectResolution)
			if err != nil {
				return nil, fmt.Errorf("parse %s: %w", pf, err)
			}
			pkgName = f.Name.Name
			t := target{importPath: oldPath + "/" + dir, alias: pkgName}
			for _, sym := range exportedTopLevel(f) {
				if prev, ok := owner[sym]; ok && prev != dir {
					return nil, fmt.Errorf("symbol collision: %s defined in both %s and %s", sym, prev, dir)
				}
				owner[sym] = dir
				symMap[sym] = t
			}
		}
	}
	if len(symMap) == 0 {
		return nil, fmt.Errorf("no symbols found under %s (did you run make proto?)", protoRoot)
	}
	return symMap, nil
}

// exportedTopLevel returns the exported top-level identifiers declared in f:
// types, receiver-less funcs, consts, and vars (incl. File_* descriptors and
// *_ServiceDesc).
func exportedTopLevel(f *ast.File) []string {
	var out []string
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil && d.Name.IsExported() {
				out = append(out, d.Name.Name)
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if s.Name.IsExported() {
						out = append(out, s.Name.Name)
					}
				case *ast.ValueSpec:
					for _, n := range s.Names {
						if n.IsExported() {
							out = append(out, n.Name)
						}
					}
				}
			}
		}
	}
	return out
}

// consumerFiles returns all .go files that may import the old proto path,
// excluding the proto package itself, generated GraphQL output, the codemod
// tool, vendor and VCS dirs.
func consumerFiles(repoRoot string) ([]string, error) {
	skipDirs := map[string]bool{
		filepath.Join(repoRoot, "pkg", "proto"):                      true,
		filepath.Join(repoRoot, "api_gateway", "graph", "generated"): true,
		filepath.Join(repoRoot, "scripts", "proto-split-codemod"):    true,
	}
	skipFiles := map[string]bool{
		filepath.Join(repoRoot, "api_gateway", "graph", "model", "models_gen.go"): true,
	}
	var out []string
	err := filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			base := info.Name()
			if base == ".git" || base == "vendor" || base == "node_modules" {
				return filepath.SkipDir
			}
			if skipDirs[path] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if skipFiles[path] {
			return nil
		}
		out = append(out, path)
		return nil
	})
	return out, err
}

type result struct {
	path   string
	used   map[string]string // alias -> importPath
	newSrc []byte
}

// rewriteFile computes the rewritten source for one consumer file but does NOT
// write it — main() writes only after every file has been validated, so a hard
// failure anywhere leaves the tree untouched.
func rewriteFile(path string, symMap map[string]target) (*result, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}

	// Locate the old-path import and its local name.
	var localName string
	var found, named bool
	for _, imp := range f.Imports {
		if importPathValue(imp) != oldPath {
			continue
		}
		found = true
		if imp.Name != nil {
			named = true
			localName = imp.Name.Name
		} else {
			localName = "proto" // default package name of the old flat package
		}
		if imp.Name != nil && (imp.Name.Name == "_" || imp.Name.Name == ".") {
			return nil, fmt.Errorf("blank/dot import of old proto path is not auto-migratable")
		}
	}
	if !found {
		return nil, nil
	}

	// Rewrite `<localName>.Sym` selectors; track the *ast.Ident we touch.
	rewritten := map[*ast.Ident]bool{}
	used := map[string]string{}
	var unknown []string
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		x, ok := sel.X.(*ast.Ident)
		if !ok || x.Name != localName {
			return true
		}
		t, ok := symMap[sel.Sel.Name]
		if !ok {
			unknown = append(unknown, sel.Sel.Name)
			return true
		}
		x.Name = t.alias
		rewritten[x] = true
		used[t.alias] = t.importPath
		return true
	})
	if len(unknown) > 0 {
		return nil, fmt.Errorf("selectors %q on %q not found in symbol map", dedup(unknown), localName)
	}

	// Fail hard on any remaining bare use of the local name (shadow/value use).
	var bare int
	ast.Inspect(f, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok || id.Name != localName || rewritten[id] {
			return true
		}
		// The import spec's own name ident (only when explicitly named).
		if named && isImportNameIdent(f, id) {
			return true
		}
		bare++
		return true
	})
	if bare > 0 {
		return nil, fmt.Errorf("found %d non-selector use(s) of local name %q; manual review required", bare, localName)
	}

	if len(used) == 0 {
		return nil, fmt.Errorf("imports old proto path but no symbols used (unexpected; manual review)")
	}

	// Swap imports.
	if named {
		astutil.DeleteNamedImport(fset, f, localName, oldPath)
	} else {
		astutil.DeleteImport(fset, f, oldPath)
	}
	for alias, ip := range used {
		astutil.AddNamedImport(fset, f, alias, ip)
	}

	var buf strings.Builder
	if err := format.Node(&buf, fset, f); err != nil {
		return nil, fmt.Errorf("format: %w", err)
	}
	return &result{path: path, used: used, newSrc: []byte(buf.String())}, nil
}

func isImportNameIdent(f *ast.File, id *ast.Ident) bool {
	for _, imp := range f.Imports {
		if imp.Name == id {
			return true
		}
	}
	return false
}

func importPathValue(imp *ast.ImportSpec) string {
	return strings.Trim(imp.Path.Value, `"`)
}

func dedup(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func rel(root, p string) string {
	r, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return r
}
