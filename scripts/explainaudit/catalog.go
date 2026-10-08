package main

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// service names one platform database and the Go module that owns its queries. sqlcDir is empty when the
// service has no sqlc catalog and every statement is hand-written.
type service struct {
	database string
	module   string
	sqlcDir  string
}

var services = []service{
	{database: "bosun", module: "api_webhooks"},
	{database: "commodore", module: "api_control", sqlcDir: "internal/database/commodoredb"},
	{database: "foghorn", module: "api_balancing", sqlcDir: "internal/database/foghorndb"},
	{database: "lookout", module: "api_incidents", sqlcDir: "internal/database/lookoutdb"},
	{database: "navigator", module: "api_dns", sqlcDir: "internal/database/navigatordb"},
	{database: "periscope", module: "api_analytics_query", sqlcDir: "internal/database/meteringdb"},
	{database: "purser", module: "api_billing", sqlcDir: "internal/database/purserdb"},
	{database: "quartermaster", module: "api_tenants", sqlcDir: "internal/database/quartermasterdb"},
	{database: "skipper", module: "api_consultant", sqlcDir: "internal/database/skipperdb"},
}

// sharedModuleDir holds code every service links; its statements are audited on each database they prepare on.
const sharedModuleDir = "pkg"

// dynamicMarker stands in for every part of a statement the extractor cannot resolve statically (a schema passed as
// a parameter, a table name from configuration). Before preparing, it is replaced by the database's schema name.
const dynamicMarker = "__DYN__"

// query is one statement the audit explains.
type query struct {
	ID         string   `json:"id"`
	Database   string   `json:"database"`
	Kind       string   `json:"kind"` // sqlc or handwritten
	Name       string   `json:"name"`
	Source     string   `json:"source"`
	CallSites  []string `json:"call_sites,omitempty"`
	ParamNames []string `json:"param_names,omitempty"`
	SQL        string   `json:"sql"`
	Dynamic    bool     `json:"dynamic,omitempty"`
	Shared     bool     `json:"shared,omitempty"`
}

var sqlStartRe = regexp.MustCompile(`(?is)^\s*(?:(?:--[^\n]*\n|/\*.*?\*/)\s*)*(SELECT|INSERT|UPDATE|DELETE|WITH)\b`)

var sqlMethods = map[string]bool{
	"QueryContext": true, "QueryRowContext": true, "ExecContext": true, "PrepareContext": true,
	"Query": true, "QueryRow": true, "Exec": true, "Prepare": true, "SendBatch": false,
}

// catalogBuilder walks Go packages and records every statement a database call site passes.
type catalogBuilder struct {
	repo        string
	modulePaths map[string]string // Go module path -> directory
	constCache  map[string]map[string]ast.Expr
	fset        *token.FileSet
	unresolved  []string
}

func newCatalogBuilder(repo string) (*catalogBuilder, error) {
	b := &catalogBuilder{repo: repo, modulePaths: map[string]string{}, constCache: map[string]map[string]ast.Expr{}, fset: token.NewFileSet()}
	mods, err := filepath.Glob(filepath.Join(repo, "*", "go.mod"))
	if err != nil {
		return nil, err
	}
	for _, mod := range mods {
		f, err := os.Open(mod)
		if err != nil {
			return nil, err
		}
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if strings.HasPrefix(line, "module ") {
				b.modulePaths[strings.TrimSpace(strings.TrimPrefix(line, "module "))] = filepath.Dir(mod)
				break
			}
		}
		_ = f.Close()
	}
	return b, nil
}

// packageDir maps an import path inside the repository to its directory.
func (b *catalogBuilder) packageDir(importPath string) (string, bool) {
	best := ""
	for mod := range b.modulePaths {
		if (importPath == mod || strings.HasPrefix(importPath, mod+"/")) && len(mod) > len(best) {
			best = mod
		}
	}
	if best == "" {
		return "", false
	}
	return filepath.Join(b.modulePaths[best], strings.TrimPrefix(strings.TrimPrefix(importPath, best), "/")), true
}

// packageConsts returns the package-level constant and variable string initialisers of the package in dir.
func (b *catalogBuilder) packageConsts(dir string) map[string]ast.Expr {
	if consts, ok := b.constCache[dir]; ok {
		return consts
	}
	consts := map[string]ast.Expr{}
	b.constCache[dir] = consts
	for _, file := range goFiles(dir) {
		parsed, err := parser.ParseFile(b.fset, file, nil, 0)
		if err != nil {
			continue
		}
		for _, decl := range parsed.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || (gen.Tok != token.CONST && gen.Tok != token.VAR) {
				continue
			}
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range value.Names {
					if i < len(value.Values) {
						consts[name.Name] = value.Values[i]
					}
				}
			}
		}
	}
	return consts
}

func goFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var files []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files = append(files, filepath.Join(dir, name))
	}
	sort.Strings(files)
	return files
}

// evaluator folds string expressions: literals, concatenation, constants (also of other repository packages),
// locals assigned earlier in the function, and fmt.Sprintf with a literal format. What it cannot resolve becomes
// dynamicMarker.
type evaluator struct {
	b       *catalogBuilder
	dir     string
	imports map[string]string
	locals  map[string]string
	depth   int
}

func (e *evaluator) eval(expr ast.Expr) (string, bool) {
	e.depth++
	defer func() { e.depth-- }()
	if e.depth > 40 {
		return dynamicMarker, true
	}
	switch x := expr.(type) {
	case *ast.BasicLit:
		if x.Kind == token.STRING {
			s, err := strconv.Unquote(x.Value)
			return s, err == nil
		}
		if x.Kind == token.INT {
			return x.Value, true
		}
	case *ast.ParenExpr:
		return e.eval(x.X)
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return "", false
		}
		l, lok := e.eval(x.X)
		r, rok := e.eval(x.Y)
		if !lok && !rok {
			return "", false
		}
		if !lok {
			l = dynamicMarker
		}
		if !rok {
			r = dynamicMarker
		}
		return l + r, true
	case *ast.Ident:
		if v, ok := e.locals[x.Name]; ok {
			return v, true
		}
		if c, ok := e.b.packageConsts(e.dir)[x.Name]; ok {
			sub := &evaluator{b: e.b, dir: e.dir, imports: e.imports, locals: map[string]string{}, depth: e.depth}
			return sub.eval(c)
		}
		return "", false
	case *ast.SelectorExpr:
		if pkg, ok := x.X.(*ast.Ident); ok {
			if path, ok := e.imports[pkg.Name]; ok {
				if dir, ok := e.b.packageDir(path); ok {
					if c, ok := e.b.packageConsts(dir)[x.Sel.Name]; ok {
						sub := &evaluator{b: e.b, dir: dir, imports: fileImportsForDir(e.b, dir), locals: map[string]string{}, depth: e.depth}
						return sub.eval(c)
					}
				}
			}
		}
		return "", false
	case *ast.CallExpr:
		if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "fmt" && sel.Sel.Name == "Sprintf" && len(x.Args) > 0 {
				format, ok := e.eval(x.Args[0])
				if !ok {
					return "", false
				}
				args := make([]string, 0, len(x.Args)-1)
				for _, arg := range x.Args[1:] {
					v, ok := e.eval(arg)
					if !ok {
						v = dynamicMarker
					}
					args = append(args, v)
				}
				return sprintfFold(format, args), true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "strings" && (sel.Sel.Name == "TrimSpace") && len(x.Args) == 1 {
				v, ok := e.eval(x.Args[0])
				return strings.TrimSpace(v), ok
			}
		}
	}
	return "", false
}

var verbRe = regexp.MustCompile(`%(?:\[[0-9]+\])?[-+# 0]*[0-9]*(?:\.[0-9]+)?[a-zA-Z%]`)

func sprintfFold(format string, args []string) string {
	i := 0
	return verbRe.ReplaceAllStringFunc(format, func(verb string) string {
		if verb == "%%" {
			return "%"
		}
		if strings.HasPrefix(verb, "%[") {
			end := strings.IndexByte(verb, ']')
			i = catalogInteger(verb[2:end]) - 1
		}
		v := dynamicMarker
		if i >= 0 && i < len(args) {
			v = args[i]
		}
		i++
		if strings.HasSuffix(verb, "q") {
			return strconv.Quote(v)
		}
		return v
	})
}

var importDirCache = map[string]map[string]string{}

// fileImportsForDir merges the imports of every file in dir; constants in another package resolve their own
// selectors through these.
func fileImportsForDir(b *catalogBuilder, dir string) map[string]string {
	if imports, ok := importDirCache[dir]; ok {
		return imports
	}
	imports := map[string]string{}
	importDirCache[dir] = imports
	for _, file := range goFiles(dir) {
		parsed, err := parser.ParseFile(b.fset, file, nil, parser.ImportsOnly)
		if err != nil {
			continue
		}
		for name, path := range fileImports(parsed) {
			imports[name] = path
		}
	}
	return imports
}

func fileImports(file *ast.File) map[string]string {
	imports := map[string]string{}
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		name := filepath.Base(path)
		if strings.HasPrefix(name, "v") && len(name) <= 3 {
			name = filepath.Base(filepath.Dir(path))
		}
		if spec.Name != nil {
			name = spec.Name.Name
		}
		imports[name] = path
	}
	return imports
}

// scanDir walks every Go package under root and calls emit for each database call whose statement text resolves.
func (b *catalogBuilder) scanDir(root string, skip func(path string) bool, emit func(q query)) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			name := entry.Name()
			if name == "node_modules" || name == "testdata" || name == "vendor" || (strings.HasPrefix(name, ".") && path != root) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || skip(path) {
			return nil
		}
		return b.scanFile(path, emit)
	})
}

func (b *catalogBuilder) scanFile(path string, emit func(q query)) error {
	parsed, err := parser.ParseFile(b.fset, path, nil, parser.ParseComments)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if isGeneratedFile(parsed) {
		return nil
	}
	b.scanParsed(parsed, filepath.Dir(path), emit)
	return nil
}

func (b *catalogBuilder) scanParsed(parsed *ast.File, dir string, emit func(q query)) {
	imports := fileImports(parsed)
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ev := &evaluator{b: b, dir: dir, imports: imports, locals: map[string]string{}}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.AssignStmt:
				if len(n.Lhs) == len(n.Rhs) {
					for i, lhs := range n.Lhs {
						id, ok := lhs.(*ast.Ident)
						if !ok {
							continue
						}
						v, ok := ev.eval(n.Rhs[i])
						if !ok {
							continue
						}
						if n.Tok == token.ADD_ASSIGN {
							v = ev.locals[id.Name] + v
						}
						ev.locals[id.Name] = v
					}
				}
			case *ast.ValueSpec:
				for i, name := range n.Names {
					if i < len(n.Values) {
						if v, ok := ev.eval(n.Values[i]); ok {
							ev.locals[name.Name] = v
						}
					}
				}
			case *ast.CallExpr:
				sel, ok := n.Fun.(*ast.SelectorExpr)
				if !ok || !sqlMethods[sel.Sel.Name] {
					return true
				}
				for idx := 0; idx < 2 && idx < len(n.Args); idx++ {
					text, ok := ev.eval(n.Args[idx])
					if !ok || !sqlStartRe.MatchString(text) {
						if !ok && idx == 1 {
							if _, isIdent := n.Args[idx].(*ast.Ident); isIdent {
								b.unresolved = append(b.unresolved, b.position(n.Pos()))
							}
						}
						continue
					}
					var names []string
					for _, arg := range n.Args[idx+1:] {
						names = append(names, paramHint(arg))
					}
					text = renumberDynamicParams(text)
					emit(query{
						Name:       fn.Name.Name,
						Source:     b.position(n.Pos()),
						SQL:        text,
						ParamNames: names,
						Dynamic:    strings.Contains(text, dynamicMarker),
					})
					break
				}
			}
			return true
		})
	}
}

var dynamicParamRe = regexp.MustCompile(`\$` + dynamicMarker)

// renumberDynamicParams gives each placeholder built as fmt.Sprintf("$%d", len(args)) the next free parameter
// number, in text order, which is the order such builders append their arguments.
func renumberDynamicParams(text string) string {
	highest := 0
	for _, m := range paramRe.FindAllStringSubmatch(text, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil && n > highest {
			highest = n
		}
	}
	return dynamicParamRe.ReplaceAllStringFunc(text, func(string) string {
		highest++
		return "$" + strconv.Itoa(highest)
	})
}

func isGeneratedFile(file *ast.File) bool {
	for _, group := range file.Comments {
		for _, comment := range group.List {
			if strings.HasPrefix(comment.Text, "// Code generated") {
				return true
			}
		}
		if group.Pos() > file.Package {
			break
		}
	}
	return false
}

func (b *catalogBuilder) position(pos token.Pos) string {
	p := b.fset.Position(pos)
	rel, err := filepath.Rel(b.repo, p.Filename)
	if err != nil {
		rel = p.Filename
	}
	return fmt.Sprintf("%s:%d", rel, p.Line)
}

// paramHint names a bound argument from its Go expression: arg.TenantID, tenantID, req.GetStreamId(),
// pq.Array(ids) and sql.NullString{String: name} all name the value they carry.
func paramHint(expr ast.Expr) string {
	switch x := expr.(type) {
	case *ast.Ident:
		return snakeCase(x.Name)
	case *ast.SelectorExpr:
		return snakeCase(x.Sel.Name)
	case *ast.StarExpr:
		return paramHint(x.X)
	case *ast.UnaryExpr:
		return paramHint(x.X)
	case *ast.CallExpr:
		if sel, ok := x.Fun.(*ast.SelectorExpr); ok && strings.HasPrefix(sel.Sel.Name, "Get") && len(x.Args) == 0 {
			return snakeCase(strings.TrimPrefix(sel.Sel.Name, "Get"))
		}
		if len(x.Args) > 0 {
			return paramHint(x.Args[0])
		}
	case *ast.CompositeLit:
		if len(x.Elts) > 0 {
			elt := x.Elts[0]
			if kv, ok := elt.(*ast.KeyValueExpr); ok {
				return paramHint(kv.Value)
			}
			return paramHint(elt)
		}
	case *ast.IndexExpr:
		return paramHint(x.X)
	}
	return ""
}

func snakeCase(name string) string {
	name = strings.NewReplacer("UUIDs", "Uuids", "IDs", "Ids", "URLs", "Urls").Replace(name)
	runes := []rune(name)
	var out []rune
	for i, r := range runes {
		if unicode.IsUpper(r) {
			if i > 0 && (unicode.IsLower(runes[i-1]) || unicode.IsDigit(runes[i-1]) || (i+1 < len(runes) && unicode.IsLower(runes[i+1]))) {
				out = append(out, '_')
			}
			out = append(out, unicode.ToLower(r))
			continue
		}
		out = append(out, r)
	}
	return strings.Trim(string(out), "_")
}

// sqlcQueries returns every statement of a service's sqlc catalog, named by its Go method, sourced at its
// queries/*.sql definition, with the parameter names its generated call passes.
func (b *catalogBuilder) sqlcQueries(svc service) ([]query, error) {
	dir := filepath.Join(b.repo, svc.module, svc.sqlcDir)
	files, err := filepath.Glob(filepath.Join(dir, "*.sql.go"))
	if err != nil {
		return nil, err
	}
	definitions, err := sqlcDefinitions(b.repo, filepath.Join(b.repo, svc.module, "internal/database/queries"))
	if err != nil {
		return nil, err
	}
	var out []query
	for _, file := range files {
		parsed, err := parser.ParseFile(b.fset, file, nil, 0)
		if err != nil {
			return nil, err
		}
		imports := fileImports(parsed)
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Recv == nil {
				continue
			}
			ev := &evaluator{b: b, dir: dir, imports: imports, locals: map[string]string{}}
			found := false
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok || found {
					return !found
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !sqlMethods[sel.Sel.Name] || len(call.Args) < 2 {
					return true
				}
				text, ok := ev.eval(call.Args[1])
				if !ok || !strings.HasPrefix(text, "-- name:") {
					return true
				}
				var names []string
				for _, arg := range call.Args[2:] {
					names = append(names, paramHint(arg))
				}
				source := definitions[fn.Name.Name]
				if source == "" {
					source = b.position(call.Pos())
				}
				out = append(out, query{Name: fn.Name.Name, Kind: "sqlc", Source: source, SQL: text, ParamNames: names})
				found = true
				return false
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

var sqlcNameRe = regexp.MustCompile(`^--\s*name:\s*(\w+)`)

func sqlcDefinitions(repo, dir string) (map[string]string, error) {
	defs := map[string]string{}
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(repo, file)
		if err != nil {
			return nil, err
		}
		for i, line := range strings.Split(string(data), "\n") {
			if m := sqlcNameRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
				defs[m[1]] = fmt.Sprintf("%s:%d", rel, i+1)
			}
		}
	}
	return defs, nil
}

// callSites indexes every method call by name in a module's non-test, non-generated code.
func (b *catalogBuilder) callSites(moduleDir string) (map[string][]string, error) {
	sites := map[string][]string{}
	err := filepath.WalkDir(moduleDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "node_modules" || entry.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, ".sql.go") {
			return nil
		}
		parsed, err := parser.ParseFile(b.fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				sites[sel.Sel.Name] = append(sites[sel.Sel.Name], b.position(call.Pos()))
			}
			return true
		})
		return nil
	})
	return sites, err
}

// buildCatalog returns, per database, the sqlc and hand-written statements to audit, plus the shared statements
// that every database tries.
func buildCatalog(repo string) (map[string][]query, []query, []string, error) {
	b, err := newCatalogBuilder(repo)
	if err != nil {
		return nil, nil, nil, err
	}
	perDB := map[string][]query{}
	for _, svc := range services {
		moduleDir := filepath.Join(repo, svc.module)
		sites, sitesErr := b.callSites(moduleDir)
		if sitesErr != nil {
			return nil, nil, nil, sitesErr
		}
		seen := map[string]bool{}
		if svc.sqlcDir != "" {
			qs, queryErr := b.sqlcQueries(svc)
			if queryErr != nil {
				return nil, nil, nil, queryErr
			}
			for _, q := range qs {
				q.Database = svc.database
				q.ID = svc.database + ":" + q.Name
				q.CallSites = sites[q.Name]
				seen[normalizeSQL(q.SQL)] = true
				perDB[svc.database] = append(perDB[svc.database], q)
			}
		}
		sqlcAbs := ""
		if svc.sqlcDir != "" {
			sqlcAbs = filepath.Join(moduleDir, svc.sqlcDir)
		}
		counter := map[string]int{}
		err = b.scanDir(moduleDir, func(path string) bool {
			return sqlcAbs != "" && strings.HasPrefix(path, sqlcAbs+string(filepath.Separator)) && strings.HasSuffix(path, ".sql.go")
		}, func(q query) {
			key := normalizeSQL(q.SQL)
			if seen[key] {
				return
			}
			seen[key] = true
			q.Kind = "handwritten"
			q.Database = svc.database
			counter[q.Name]++
			name := q.Name
			if counter[q.Name] > 1 {
				name = fmt.Sprintf("%s#%d", q.Name, counter[q.Name])
			}
			q.Name = name
			q.ID = svc.database + ":hw:" + name
			q.CallSites = []string{q.Source}
			perDB[svc.database] = append(perDB[svc.database], q)
		})
		if err != nil {
			return nil, nil, nil, err
		}
	}
	var shared []query
	seen := map[string]bool{}
	counter := map[string]int{}
	err = b.scanDir(filepath.Join(repo, sharedModuleDir), func(path string) bool {
		return strings.Contains(path, string(filepath.Separator)+"testutil"+string(filepath.Separator))
	}, func(q query) {
		key := normalizeSQL(q.SQL)
		if seen[key] {
			return
		}
		seen[key] = true
		q.Kind = "handwritten"
		q.Shared = true
		counter[q.Name]++
		name := q.Name
		if counter[q.Name] > 1 {
			name = fmt.Sprintf("%s#%d", q.Name, counter[q.Name])
		}
		q.Name = name
		q.CallSites = []string{q.Source}
		shared = append(shared, q)
	})
	if err != nil {
		return nil, nil, nil, err
	}
	return perDB, shared, b.unresolved, nil
}

var spaceRe = regexp.MustCompile(`\s+`)

func normalizeSQL(s string) string {
	lines := strings.Split(s, "\n")
	var kept []string
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "-- name:") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(spaceRe.ReplaceAllString(strings.Join(kept, " "), " "))
}
