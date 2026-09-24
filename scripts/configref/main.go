// Command configref generates the operator configuration reference from the
// typed service configuration structs and verifies that migrated services read
// configuration only through them.
//
// A service opts in by declaring its startup configuration in
// api_*/internal/appconfig with a directive on the struct type:
//
//	//configref:service <servicedefs-id> cmd=<command dir> [variant=<subcommand>]
//
// Shared blocks come from pkg/config/blocks.go. Field annotations are validated
// with pkg/config.ParseFieldTag, the same parser the runtime loader uses.
//
// Outputs:
//   - website_docs/src/content/docs/operators/configuration-reference.mdx
//   - cli/internal/configschema/config-schema.json
//
// `make generate-config-reference` writes them; `make verify-config-annotations`
// runs this with -check, which also fails when a migrated command still reads
// the environment directly.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
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

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
)

const (
	referencePagePath = "website_docs/src/content/docs/operators/configuration-reference.mdx"
	schemaPath        = "cli/internal/configschema/config-schema.json"
	catalogPath       = "cli/internal/releases/catalog.yaml"
	blocksDir         = "pkg/config"
	directivePrefix   = "//configref:service "
)

// roleOrder groups services on the reference page the same way the operator
// architecture docs present planes.
var roleOrder = []string{"control", "data", "analytics", "media", "infra", "mesh", "support", "interface", "observability"}

const configImportPath = "github.com/Livepeer-FrameWorks/monorepo/pkg/config"

// envReaders lists, per import path, the functions that read the process
// environment directly and so bypass a service's typed configuration.
var envReaders = map[string]map[string]bool{
	"os":             {"Getenv": true, "LookupEnv": true, "Environ": true, "ExpandEnv": true},
	configImportPath: {"GetEnv": true, "IsProduction": true, "GetLogLevel": true},
}

// sharedPackagesDir is scanned recursively for direct environment reads.
const sharedPackagesDir = "pkg"

// sharedEnvReadAllowlist is every shared-package or service internal file that
// may still read the process environment directly. An entry ending in "/"
// covers a directory tree. No service internal file needs an entry.
var sharedEnvReadAllowlist = []string{
	"pkg/clients/foghorn/grpc_client.go", // BUILD_ENV fail-closed TLS guard
	"pkg/config/env.go",                  // the direct reader helpers
	"pkg/config/load.go",                 // the typed loader's default lookup (os.LookupEnv)
	"pkg/config/reload.go",               // SIGHUP env-file reload into the process environment
	"pkg/grpcutil/tls.go",                // BUILD_ENV fail-closed TLS guard
	"pkg/logging/logger.go",              // LOG_LEVEL bootstrap read before configuration loads
	"pkg/systemd/listenfds.go",           // sd_listen_fds(3) socket-activation handshake, a process protocol
	"pkg/testutil/",                      // test harness image and database overrides
}

var (
	catalogFloorPattern   = regexp.MustCompile(`(?m)^schema_migration_floor:\s*(v[0-9]+\.[0-9]+\.[0-9]+)\s*$`)
	catalogReleasePattern = regexp.MustCompile(`(?m)^\s*-\s*version:\s*(v[0-9]+\.[0-9]+\.[0-9]+)\s*$`)
)

type variable struct {
	Key         string `json:"key"`
	Type        string `json:"type"`
	Default     string `json:"default,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Secret      bool   `json:"secret,omitempty"`
	Introduced  string `json:"introduced"`
	Deprecated  string `json:"deprecated,omitempty"`
	Replacement string `json:"replacement,omitempty"`
	Description string `json:"description"`
}

type section struct {
	Service   string     `json:"service"`
	Variant   string     `json:"variant,omitempty"`
	Command   string     `json:"command"`
	Variables []variable `json:"variables"`
}

type schemaFile struct {
	GeneratedBy string    `json:"generated_by"`
	Services    []section `json:"services"`
}

type structDecl struct {
	name      string
	file      string
	fields    []*ast.Field
	directive string
}

func main() {
	check := flag.Bool("check", false, "verify the generated files are current instead of writing them")
	repo := flag.String("repo", "../..", "repository root")
	flag.Parse()

	outputs, err := generate(*repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, "configref:", err)
		os.Exit(1)
	}
	paths := make([]string, 0, len(outputs))
	for path := range outputs {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	if *check {
		var stale []string
		for _, path := range paths {
			current, readErr := os.ReadFile(filepath.Join(*repo, path))
			if readErr != nil || !bytes.Equal(current, outputs[path]) {
				stale = append(stale, path)
			}
		}
		if len(stale) > 0 {
			fmt.Fprintf(os.Stderr, "configref: generated files are out of date: %s\nrun `make generate-config-reference` and commit the result\n", strings.Join(stale, ", "))
			os.Exit(1)
		}
		fmt.Println("configref: configuration reference and schema are current")
		return
	}
	for _, path := range paths {
		target := filepath.Join(*repo, path)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "configref:", err)
			os.Exit(1)
		}
		if err := os.WriteFile(target, outputs[path], 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "configref:", err)
			os.Exit(1)
		}
		fmt.Println("configref: wrote", path)
	}
}

// generate returns the generated files keyed by repo-relative path.
func generate(repo string) (map[string][]byte, error) {
	floor, latest, err := catalogBounds(filepath.Join(repo, catalogPath))
	if err != nil {
		return nil, err
	}
	blocks, err := parseStructs(filepath.Join(repo, blocksDir))
	if err != nil {
		return nil, err
	}

	dirs, err := filepath.Glob(filepath.Join(repo, "api_*", "internal", "appconfig"))
	if err != nil {
		return nil, err
	}
	sort.Strings(dirs)
	var sections []section
	for _, dir := range dirs {
		found, dirErr := sectionsInDir(repo, dir, blocks, floor, latest)
		if dirErr != nil {
			return nil, dirErr
		}
		sections = append(sections, found...)
	}
	if len(sections) == 0 {
		return nil, errors.New("no //configref:service configuration types found under api_*/internal/appconfig")
	}
	sortSections(sections)

	seen := map[string]bool{}
	scanned := map[string]bool{}
	var violations []string
	for _, s := range sections {
		id := s.Service + "/" + s.Variant
		if seen[id] {
			return nil, fmt.Errorf("service %s variant %q is declared by more than one type", s.Service, s.Variant)
		}
		seen[id] = true
		if scanned[s.Command] {
			continue
		}
		scanned[s.Command] = true
		found, scanErr := scanCommand(repo, s.Command)
		if scanErr != nil {
			return nil, scanErr
		}
		violations = append(violations, found...)
	}
	sharedViolations, err := scanSharedPackages(repo)
	if err != nil {
		return nil, err
	}
	internalViolations, err := scanServiceInternalPackages(repo)
	if err != nil {
		return nil, err
	}
	sharedViolations = append(sharedViolations, internalViolations...)
	var problems []string
	if len(violations) > 0 {
		problems = append(problems, "migrated commands read configuration outside their typed struct:\n  "+strings.Join(violations, "\n  "))
	}
	if len(sharedViolations) > 0 {
		problems = append(problems, "shared and service internal packages read the environment directly; pass the value in from the service's typed configuration:\n  "+strings.Join(sharedViolations, "\n  "))
	}
	if len(problems) > 0 {
		return nil, errors.New(strings.Join(problems, "\n"))
	}

	schema, err := json.MarshalIndent(schemaFile{GeneratedBy: "scripts/configref", Services: sections}, "", "  ")
	if err != nil {
		return nil, err
	}
	return map[string][]byte{
		referencePagePath: renderPage(sections),
		schemaPath:        append(schema, '\n'),
	}, nil
}

func sectionsInDir(repo, dir string, blocks map[string]structDecl, floor, latest string) ([]section, error) {
	local, err := parseStructs(dir)
	if err != nil {
		return nil, err
	}
	relDir, err := filepath.Rel(repo, dir)
	if err != nil {
		return nil, err
	}
	moduleDir := strings.Split(filepath.ToSlash(relDir), "/")[0]

	names := make([]string, 0, len(local))
	for name := range local {
		names = append(names, name)
	}
	sort.Strings(names)

	var sections []section
	for _, name := range names {
		decl := local[name]
		if decl.directive == "" {
			continue
		}
		service, cmd, variant, err := parseDirective(decl.directive)
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %w", decl.file, name, err)
		}
		if _, ok := servicedefs.Lookup(service); !ok {
			return nil, fmt.Errorf("%s: %s: unknown service %q", decl.file, name, service)
		}
		s := section{Service: service, Variant: variant, Command: moduleDir + "/" + strings.TrimSuffix(cmd, "/")}
		keys := map[string]bool{}
		if err := expandFields(decl.fields, local, blocks, service, &s.Variables, keys); err != nil {
			return nil, fmt.Errorf("%s: %s: %w", decl.file, name, err)
		}
		for _, v := range s.Variables {
			if err := checkVersionRange(v, floor, latest); err != nil {
				return nil, fmt.Errorf("%s: %s: %w", decl.file, name, err)
			}
		}
		sections = append(sections, s)
	}
	return sections, nil
}

func parseDirective(directive string) (service, cmd, variant string, err error) {
	fields := strings.Fields(directive)
	if len(fields) == 0 {
		return "", "", "", errors.New("configref directive needs a service id")
	}
	service = fields[0]
	for _, field := range fields[1:] {
		key, value, ok := strings.Cut(field, "=")
		if !ok || value == "" {
			return "", "", "", fmt.Errorf("malformed configref directive option %q", field)
		}
		switch key {
		case "cmd":
			cmd = value
		case "variant":
			variant = value
		default:
			return "", "", "", fmt.Errorf("unknown configref directive option %q", key)
		}
	}
	if cmd == "" {
		return "", "", "", errors.New("configref directive needs cmd=<command dir>")
	}
	return service, cmd, variant, nil
}

func expandFields(fields []*ast.Field, local, blocks map[string]structDecl, service string, out *[]variable, keys map[string]bool) error {
	for _, f := range fields {
		name := fieldName(f)
		tag := ""
		if f.Tag != nil {
			unquoted, err := strconv.Unquote(f.Tag.Value)
			if err != nil {
				return fmt.Errorf("field %s: malformed tag literal: %w", name, err)
			}
			tag = unquoted
		}
		spec, isConfig, err := config.ParseFieldTag(tag)
		if err != nil {
			return fmt.Errorf("field %s: %w", name, err)
		}
		if len(f.Names) > 0 && !ast.IsExported(f.Names[0].Name) {
			continue
		}

		if !isConfig {
			nested, scope, ok := resolveStruct(f.Type, local, blocks)
			if !ok {
				return fmt.Errorf("field %s has no env tag", name)
			}
			if len(f.Names) == 0 && !ast.IsExported(nested.name) {
				return fmt.Errorf("embedded type %s must be exported", nested.name)
			}
			if nestedErr := expandFields(nested.fields, scope, blocks, service, out, keys); nestedErr != nil {
				return nestedErr
			}
			continue
		}

		typeLabel, err := typeLabel(f.Type)
		if err != nil {
			return fmt.Errorf("field %s: %w", name, err)
		}
		if keys[spec.Env] {
			return fmt.Errorf("%s is declared more than once", spec.Env)
		}
		keys[spec.Env] = true
		def := ""
		if spec.HasDefault {
			if def, err = config.ResolveDefault(spec, service); err != nil {
				return err
			}
		}
		*out = append(*out, variable{
			Key:         spec.Env,
			Type:        typeLabel,
			Default:     def,
			Required:    spec.Required,
			Secret:      spec.Secret,
			Introduced:  spec.Introduced,
			Deprecated:  spec.Deprecated,
			Replacement: spec.Replacement,
			Description: spec.Desc,
		})
	}
	return nil
}

// resolveStruct finds the struct a field refers to: a bare identifier in the
// current scope, or config.<Block> from pkg/config. Blocks resolve bare
// identifiers against the other blocks.
func resolveStruct(expr ast.Expr, local, blocks map[string]structDecl) (structDecl, map[string]structDecl, bool) {
	switch t := expr.(type) {
	case *ast.Ident:
		decl, ok := local[t.Name]
		return decl, local, ok
	case *ast.SelectorExpr:
		if pkg, ok := t.X.(*ast.Ident); ok && pkg.Name == "config" {
			decl, ok := blocks[t.Sel.Name]
			return decl, blocks, ok
		}
	}
	return structDecl{}, nil, false
}

func typeLabel(expr ast.Expr) (string, error) {
	switch t := expr.(type) {
	case *ast.Ident:
		switch t.Name {
		case "string":
			return "string", nil
		case "bool":
			return "boolean", nil
		case "int":
			return "integer", nil
		}
	case *ast.SelectorExpr:
		if pkg, ok := t.X.(*ast.Ident); ok && pkg.Name == "time" && t.Sel.Name == "Duration" {
			return "duration", nil
		}
	case *ast.ArrayType:
		if elem, ok := t.Elt.(*ast.Ident); ok && t.Len == nil && elem.Name == "string" {
			return "list", nil
		}
	}
	return "", fmt.Errorf("unsupported configuration field type %T", expr)
}

func fieldName(f *ast.Field) string {
	if len(f.Names) > 0 {
		return f.Names[0].Name
	}
	switch t := f.Type.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return t.Sel.Name
	default:
		return "embedded field"
	}
}

func parseStructs(dir string) (map[string]structDecl, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	decls := map[string]structDecl{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}
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
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					continue
				}
				doc := ts.Doc
				if doc == nil && len(gen.Specs) == 1 {
					doc = gen.Doc
				}
				decls[ts.Name.Name] = structDecl{name: ts.Name.Name, file: path, fields: st.Fields.List, directive: directiveOf(doc)}
			}
		}
	}
	return decls, nil
}

func directiveOf(doc *ast.CommentGroup) string {
	if doc == nil {
		return ""
	}
	for _, c := range doc.List {
		if rest, ok := strings.CutPrefix(c.Text, directivePrefix); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

func catalogBounds(path string) (floor, latest string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	m := catalogFloorPattern.FindSubmatch(data)
	if m == nil {
		return "", "", fmt.Errorf("%s: schema_migration_floor not found", path)
	}
	floor = string(m[1])
	for _, rel := range catalogReleasePattern.FindAllSubmatch(data, -1) {
		if latest == "" || compareVersions(string(rel[1]), latest) > 0 {
			latest = string(rel[1])
		}
	}
	if latest == "" {
		return "", "", fmt.Errorf("%s: no release versions found", path)
	}
	return floor, latest, nil
}

func checkVersionRange(v variable, floor, latest string) error {
	for label, version := range map[string]string{"introduced": v.Introduced, "deprecated": v.Deprecated} {
		if version == "" {
			continue
		}
		if compareVersions(version, floor) < 0 || compareVersions(version, latest) > 0 {
			return fmt.Errorf("%s %s %s is outside the release catalog range %s..%s", v.Key, label, version, floor, latest)
		}
	}
	return nil
}

func compareVersions(a, b string) int {
	pa, pb := versionParts(a), versionParts(b)
	for i := range pa {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func versionParts(v string) [3]int {
	var parts [3]int
	for i, s := range strings.SplitN(strings.TrimPrefix(v, "v"), ".", 3) {
		if n, err := strconv.Atoi(s); err == nil {
			parts[i] = n
		}
	}
	return parts
}

func sortSections(sections []section) {
	rank := map[string]int{}
	for i, role := range roleOrder {
		rank[role] = i
	}
	roleRank := func(service string) int {
		def, _ := servicedefs.Lookup(service)
		if r, ok := rank[def.Role]; ok {
			return r
		}
		return len(roleOrder)
	}
	sort.SliceStable(sections, func(i, j int) bool {
		a, b := sections[i], sections[j]
		if ra, rb := roleRank(a.Service), roleRank(b.Service); ra != rb {
			return ra < rb
		}
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		return a.Variant < b.Variant
	})
}

func scanCommand(repo, command string) ([]string, error) {
	dir := filepath.Join(repo, filepath.FromSlash(command))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("command directory %s: %w", command, err)
	}
	var violations []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		found, err := scanGoFile(filepath.Join(dir, name), command+"/"+name, false)
		if err != nil {
			return nil, err
		}
		violations = append(violations, found...)
	}
	return violations, nil
}

// scanSharedPackages reports direct environment reads in non-test Go files
// under pkg/ that sharedEnvReadAllowlist does not cover.
func scanSharedPackages(repo string) ([]string, error) {
	return scanTree(repo, filepath.Join(repo, sharedPackagesDir))
}

// scanServiceInternalPackages reports direct environment reads in non-test Go
// files under every api_*/internal tree that sharedEnvReadAllowlist does not
// cover. A service reads its configuration through internal/appconfig and
// passes values in, so its internal packages never read the environment.
func scanServiceInternalPackages(repo string) ([]string, error) {
	roots, err := filepath.Glob(filepath.Join(repo, "api_*", "internal"))
	if err != nil {
		return nil, err
	}
	sort.Strings(roots)
	var violations []string
	for _, root := range roots {
		found, err := scanTree(repo, root)
		if err != nil {
			return nil, err
		}
		violations = append(violations, found...)
	}
	return violations, nil
}

// scanTree walks root and reports direct environment reads in its non-test Go
// files outside testdata and sharedEnvReadAllowlist.
func scanTree(repo, root string) ([]string, error) {
	var violations []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(repo, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if d.Name() == "testdata" || sharedEnvReadAllowed(rel+"/") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") || sharedEnvReadAllowed(rel) {
			return nil
		}
		found, err := scanGoFile(path, rel, strings.HasPrefix(rel, blocksDir+"/"))
		if err != nil {
			return err
		}
		violations = append(violations, found...)
		return nil
	})
	return violations, err
}

func sharedEnvReadAllowed(rel string) bool {
	for _, entry := range sharedEnvReadAllowlist {
		if rel == entry || (strings.HasSuffix(entry, "/") && strings.HasPrefix(rel, entry)) {
			return true
		}
	}
	return false
}

// scanGoFile reports calls to envReaders in one file, resolving import
// aliases. inConfigPackage also reports unqualified calls to the pkg/config
// readers from inside that package.
func scanGoFile(path, rel string, inConfigPackage bool) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	readersByName := map[string]map[string]bool{}
	for _, imp := range file.Imports {
		importPath, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return nil, err
		}
		readers, ok := envReaders[importPath]
		if !ok {
			continue
		}
		name := importPath[strings.LastIndex(importPath, "/")+1:]
		if imp.Name != nil {
			name = imp.Name.Name
		}
		readersByName[name] = readers
	}
	var violations []string
	report := func(pos token.Pos, reader string) {
		violations = append(violations, fmt.Sprintf("%s:%d: %s", rel, fset.Position(pos).Line, reader))
	}
	// Identifiers that name something rather than refer to a reader: the
	// selected name of a selector, a declared function, and a struct field.
	naming := map[*ast.Ident]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.SelectorExpr:
			naming[node.Sel] = true
			// Any reference counts, not only a call: `lookup := os.Getenv`
			// or passing os.LookupEnv reads the environment just the same.
			if pkg, ok := node.X.(*ast.Ident); ok && readersByName[pkg.Name][node.Sel.Name] {
				report(node.Pos(), pkg.Name+"."+node.Sel.Name)
			}
		case *ast.FuncDecl:
			naming[node.Name] = true
		case *ast.Field:
			for _, name := range node.Names {
				naming[name] = true
			}
		case *ast.Ident:
			if inConfigPackage && !naming[node] && envReaders[configImportPath][node.Name] {
				report(node.Pos(), node.Name)
			}
		}
		return true
	})
	return violations, nil
}

func renderPage(sections []section) []byte {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("title: \"Configuration Reference\"\n")
	b.WriteString("description: \"Environment variables read at startup by FrameWorks services with typed configuration.\"\n")
	b.WriteString("sidebar:\n  order: 5\n")
	b.WriteString("---\n\n")
	// The comment text must not contain the two-character comment terminator,
	// which MDX would read as the end of the comment.
	b.WriteString("{/* Generated by scripts/configref from each service's internal/appconfig package and pkg/config/blocks.go. Do not edit; run `make generate-config-reference`. */}\n\n")
	b.WriteString("This page is generated from the typed configuration structs of each service. It lists every variable declared in a service's startup configuration, its default, and the release that introduced it.\n\n")
	b.WriteString("A service refuses to start when a required variable in its startup configuration is missing or a value cannot be parsed, and it names every offending variable in one error. A value that is empty or only whitespace counts as unset. Secret values are redacted from the authenticated `/debug/config` endpoint.\n\n")
	b.WriteString("Shared packages read two variables directly from the process environment. `BUILD_ENV` is read by the internal gRPC TLS helpers, which reject `GRPC_ALLOW_INSECURE` and require TLS to Foghorn when it is `production` or `prod`. `LOG_LEVEL` is read once when the logger is created, before configuration loads; each service then applies the `LOG_LEVEL` listed below. Services that are not listed have not adopted typed configuration.\n")

	for i, s := range sections {
		if i == 0 || sections[i-1].Service != s.Service {
			fmt.Fprintf(&b, "\n## %s\n", s.Service)
		}
		if s.Variant != "" {
			fmt.Fprintf(&b, "\n### %s subcommand\n", s.Variant)
		}
		fmt.Fprintf(&b, "\nRead by `%s`.\n\n", s.Command)
		b.WriteString("| Variable | Type | Default | Notes | Since | Description |\n")
		b.WriteString("| --- | --- | --- | --- | --- | --- |\n")
		for _, v := range s.Variables {
			def := ""
			if v.Default != "" {
				def = "`" + v.Default + "`"
			}
			var notes []string
			if v.Required {
				notes = append(notes, "required")
			}
			if v.Secret {
				notes = append(notes, "secret")
			}
			if v.Deprecated != "" {
				note := "deprecated in " + v.Deprecated
				if v.Replacement != "" {
					note += ", use `" + v.Replacement + "`"
				}
				notes = append(notes, note)
			}
			fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s | %s |\n", v.Key, v.Type, def, strings.Join(notes, ", "), v.Introduced, escapeMDX(v.Description))
		}
	}
	return []byte(b.String())
}

func escapeMDX(s string) string {
	return strings.NewReplacer("|", "\\|", "<", "&lt;", ">", "&gt;", "{", "&#123;", "}", "&#125;").Replace(s)
}
