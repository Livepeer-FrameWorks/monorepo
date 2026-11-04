package introspection

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"sync"

	graphqlpkg "github.com/Livepeer-FrameWorks/monorepo/pkg/graphql"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/formatter"
	"github.com/vektah/gqlparser/v2/parser"
)

const maxFieldPathDepth = 6

// Template represents a loaded GraphQL operation template.
type Template struct {
	Name              string                 `json:"name"`
	Description       string                 `json:"description"`
	Query             string                 `json:"query"`
	Variables         map[string]interface{} `json:"variables"`
	FilePath          string                 `json:"file_path"`
	OperationType     string                 `json:"operation_type"`
	FieldPaths        []string               `json:"field_paths"`
	RequiredVariables []string               `json:"required_variables,omitempty"`
	SDKOperations     []string               `json:"sdk_operations,omitempty"`
	Source            string                 `json:"source,omitempty"`
}

// TemplateLoader loads and caches GraphQL templates from embedded files.
type TemplateLoader struct {
	mu        sync.RWMutex
	templates map[string]*Template // key: "query:fieldPath"
	loaded    bool
	all       []*Template
}

// NewTemplateLoader creates a new template loader.
func NewTemplateLoader() *TemplateLoader {
	return &TemplateLoader{
		templates: make(map[string]*Template),
	}
}

// Load loads all templates from the embedded filesystem.
func (tl *TemplateLoader) Load() error {
	return tl.loadFrom(graphqlpkg.OperationsFS, graphqlpkg.PublicCatalog)
}

func (tl *TemplateLoader) loadFrom(operationsFS fs.FS, catalogJSON []byte) error {
	tl.mu.Lock()
	defer tl.mu.Unlock()

	if tl.loaded {
		return nil
	}
	candidate := NewTemplateLoader()
	if err := candidate.load(operationsFS, catalogJSON); err != nil {
		return err
	}
	tl.templates, tl.all, tl.loaded = candidate.templates, candidate.all, true
	return nil
}

func (tl *TemplateLoader) load(operationsFS fs.FS, catalogJSON []byte) error {
	// Load fragments first for resolution
	fragments := make(map[string]string)
	if err := fs.WalkDir(operationsFS, "operations/fragments", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".gql") {
			return err
		}
		content, readErr := fs.ReadFile(operationsFS, path)
		if readErr != nil {
			return readErr
		}
		doc, parseErr := parser.ParseQuery(&ast.Source{Input: string(content)})
		if parseErr != nil {
			return fmt.Errorf("%s: %w", path, parseErr)
		}
		for _, fragment := range doc.Fragments {
			if _, exists := fragments[fragment.Name]; exists {
				return fmt.Errorf("duplicate fragment %s", fragment.Name)
			}
			fragments[fragment.Name] = string(content)
		}

		return nil
	}); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}

	// Load queries, mutations, subscriptions
	for _, opType := range []string{"queries", "mutations", "subscriptions"} {
		dirPath := "operations/" + opType
		if err := fs.WalkDir(operationsFS, dirPath, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".gql") {
				return err
			}
			content, readErr := fs.ReadFile(operationsFS, path)
			if readErr != nil {
				return readErr
			}

			template := tl.parseTemplate(string(content), path, fragments)
			if template == nil {
				return fmt.Errorf("invalid operation template %s", path)
			}
			if template != nil {
				tl.all = append(tl.all, template)
				fieldPaths := template.FieldPaths
				if len(fieldPaths) == 0 {
					fieldPaths = []string{extractFieldName(template.Query)}
				}

				for _, fieldPath := range fieldPaths {
					if fieldPath == "" {
						continue
					}
					key := template.OperationType + ":" + fieldPath
					if _, exists := tl.templates[key]; !exists {
						tl.templates[key] = template
					}
				}
			}
			return nil
		}); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}

	var catalog struct {
		Operations []struct {
			Name      string `json:"name"`
			Kind      string `json:"kind"`
			Target    string `json:"target"`
			Document  string `json:"document"`
			Variables []struct {
				Name     string `json:"name"`
				Required bool   `json:"required"`
			} `json:"variables"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(catalogJSON, &catalog); err != nil {
		return err
	}
	for _, op := range catalog.Operations {
		path := strings.TrimPrefix(op.Target, op.Kind+".")
		key := op.Kind + ":" + path
		if existing := tl.templates[key]; existing != nil && resolveFragments(existing.Query, nil) == resolveFragments(op.Document, nil) {
			existing.SDKOperations = append(existing.SDKOperations, op.Name)
			continue
		}
		required := []string{}
		for _, variable := range op.Variables {
			if variable.Required {
				required = append(required, variable.Name)
			}
		}
		template := &Template{Name: op.Name, Query: op.Document, Variables: map[string]interface{}{}, RequiredVariables: required, FilePath: "public/catalog.json", OperationType: op.Kind, FieldPaths: []string{path}, SDKOperations: []string{op.Name}, Source: "sdk"}
		// A wrapper or dashboard template can mention a field incidentally. Prefer
		// its dedicated SDK document while keeping every application example listed.
		if existing := tl.templates[key]; existing == nil || primaryFieldPath(existing.Query) != path {
			tl.templates[key] = template
		}
		tl.all = append(tl.all, template)
	}

	tl.loaded = true
	return nil
}

// primaryFieldPath follows a single selection through wrapper fields. Identity
// fields do not make a wrapper into an entity-detail query; multiple root fields
// describe an overview rather than a dedicated example for any one root.
func primaryFieldPath(query string) string {
	doc, err := parser.ParseQuery(&ast.Source{Input: query})
	if err != nil || len(doc.Operations) != 1 {
		return ""
	}
	active := map[string]bool{}
	var fields func(ast.SelectionSet) []*ast.Field
	fields = func(set ast.SelectionSet) []*ast.Field {
		var result []*ast.Field
		for _, selection := range set {
			switch selected := selection.(type) {
			case *ast.Field:
				if selected.Name != "__typename" {
					result = append(result, selected)
				}
			case *ast.InlineFragment:
				result = append(result, fields(selected.SelectionSet)...)
			case *ast.FragmentSpread:
				if fragment := doc.Fragments.ForName(selected.Name); fragment != nil && !active[selected.Name] {
					active[selected.Name] = true
					result = append(result, fields(fragment.SelectionSet)...)
					delete(active, selected.Name)
				}
			}
		}
		return result
	}
	root := fields(doc.Operations[0].SelectionSet)
	if len(root) != 1 {
		return ""
	}
	current := root[0]
	path := []string{current.Name}
	for {
		var content []*ast.Field
		for _, child := range fields(current.SelectionSet) {
			if child.Name != "id" {
				content = append(content, child)
			}
		}
		if len(content) != 1 || len(content[0].SelectionSet) == 0 {
			break
		}
		current = content[0]
		path = append(path, current.Name)
	}
	return strings.Join(path, ".")
}

// FindByField finds a template by operation type and field path.
func (tl *TemplateLoader) FindByField(operationType, fieldPath string) *Template {
	tl.mu.RLock()
	defer tl.mu.RUnlock()

	key := operationType + ":" + fieldPath
	return tl.templates[key]
}

// GetAll returns all loaded templates.
func (tl *TemplateLoader) GetAll() []*Template {
	tl.mu.RLock()
	defer tl.mu.RUnlock()

	return append([]*Template(nil), tl.all...)
}

// parseTemplate parses a .gql file content into a Template.
func (tl *TemplateLoader) parseTemplate(content, path string, fragments map[string]string) *Template {
	// Determine operation type from path
	var opType string
	if strings.Contains(path, "/queries/") {
		opType = "query"
	} else if strings.Contains(path, "/mutations/") {
		opType = "mutation"
	} else if strings.Contains(path, "/subscriptions/") {
		opType = "subscription"
	} else {
		return nil
	}

	doc, err := parser.ParseQuery(&ast.Source{Input: content})
	if err != nil || len(doc.Operations) != 1 {
		return nil
	}
	op := doc.Operations[0]
	name := op.Name
	description := extractDescription(content)
	resolvedQuery := resolveFragments(content, fragments)
	if resolvedQuery == "" {
		return nil
	}
	required := []string{}
	for _, variable := range op.VariableDefinitions {
		if variable.Type.NonNull && variable.DefaultValue == nil {
			required = append(required, variable.Variable)
		}
	}

	fieldPaths := extractFieldPaths(resolvedQuery)

	return &Template{
		Name:              name,
		Description:       description,
		Query:             resolvedQuery,
		Variables:         map[string]interface{}{},
		RequiredVariables: required,
		Source:            "template",
		FilePath:          cleanPath(path),
		OperationType:     opType,
		FieldPaths:        fieldPaths,
	}
}

// extractFieldName extracts the first field name from a query.
func extractFieldName(query string) string {
	// Match the first field after the operation definition: { fieldName(...) { ... } }
	re := regexp.MustCompile(`\{\s*(\w+)`)
	if match := re.FindStringSubmatch(query); match != nil {
		return match[1]
	}
	return ""
}

// extractFieldPaths extracts field paths from a query using the GraphQL AST.
func extractFieldPaths(query string) []string {
	doc, err := parser.ParseQuery(&ast.Source{Input: query})
	if err != nil {
		return nil
	}

	fragmentMap := make(map[string]*ast.FragmentDefinition)
	for _, frag := range doc.Fragments {
		fragmentMap[frag.Name] = frag
	}

	paths := make(map[string]struct{})
	for _, op := range doc.Operations {
		collectFieldPaths(op.SelectionSet, nil, 1, fragmentMap, paths)
	}

	result := make([]string, 0, len(paths))
	for path := range paths {
		result = append(result, path)
	}
	sort.Strings(result)
	return result
}

func collectFieldPaths(selectionSet ast.SelectionSet, prefix []string, depth int, fragments map[string]*ast.FragmentDefinition, out map[string]struct{}) {
	if depth > maxFieldPathDepth {
		return
	}

	for _, selection := range selectionSet {
		switch sel := selection.(type) {
		case *ast.Field:
			name := sel.Name
			if name == "" || strings.HasPrefix(name, "__") {
				continue
			}

			path := append(prefix, name)
			if len(prefix) == 0 || len(sel.SelectionSet) > 0 {
				out[strings.Join(path, ".")] = struct{}{}
			}

			if len(sel.SelectionSet) > 0 {
				collectFieldPaths(sel.SelectionSet, path, depth+1, fragments, out)
			}
		case *ast.FragmentSpread:
			if frag := fragments[sel.Name]; frag != nil {
				collectFieldPaths(frag.SelectionSet, prefix, depth, fragments, out)
			}
		case *ast.InlineFragment:
			collectFieldPaths(sel.SelectionSet, prefix, depth, fragments, out)
		}
	}
}

// extractDescription extracts leading comments as description.
func extractDescription(content string) string {
	lines := strings.Split(content, "\n")
	var desc []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			desc = append(desc, strings.TrimPrefix(line, "#"))
		} else if line != "" {
			break
		}
	}
	return strings.TrimSpace(strings.Join(desc, " "))
}

// resolveFragments normalizes the complete executable document, including
// directives inside fragments that came from the application template set.
func resolveFragments(query string, fragments map[string]string) string {
	doc, err := parser.ParseQuery(&ast.Source{Input: query})
	if err != nil {
		return ""
	}
	definitions := map[string]*ast.FragmentDefinition{}
	for _, f := range doc.Fragments {
		definitions[f.Name] = f
	}
	for _, source := range fragments {
		parsed, err := parser.ParseQuery(&ast.Source{Input: source})
		if err != nil {
			return ""
		}
		for _, f := range parsed.Fragments {
			definitions[f.Name] = f
		}
	}
	used := map[string]bool{}
	active := map[string]bool{}
	var walk func(ast.SelectionSet) bool
	walk = func(set ast.SelectionSet) bool {
		for _, selection := range set {
			switch s := selection.(type) {
			case *ast.Field:
				s.Directives = serverDirectives(s.Directives)
				if !walk(s.SelectionSet) {
					return false
				}
			case *ast.InlineFragment:
				s.Directives = serverDirectives(s.Directives)
				if !walk(s.SelectionSet) {
					return false
				}
			case *ast.FragmentSpread:
				s.Directives = serverDirectives(s.Directives)
				if active[s.Name] {
					return false
				}
				if used[s.Name] {
					continue
				}
				f := definitions[s.Name]
				if f == nil {
					return false
				}
				active[s.Name] = true
				f.Directives = serverDirectives(f.Directives)
				if !walk(f.SelectionSet) {
					return false
				}
				delete(active, s.Name)
				used[s.Name] = true
			}
		}
		return true
	}
	for _, op := range doc.Operations {
		op.Directives = serverDirectives(op.Directives)
		for _, v := range op.VariableDefinitions {
			v.Directives = serverDirectives(v.Directives)
		}
		if !walk(op.SelectionSet) {
			return ""
		}
	}
	doc.Fragments = nil
	names := make([]string, 0, len(used))
	for name := range used {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		doc.Fragments = append(doc.Fragments, definitions[name])
	}
	var out bytes.Buffer
	formatter.NewFormatter(&out, formatter.WithIndent("  "), formatter.WithoutDescription()).FormatQueryDocument(doc)
	return out.String()
}

func serverDirectives(directives ast.DirectiveList) ast.DirectiveList {
	kept := make(ast.DirectiveList, 0, len(directives))
	for _, d := range directives {
		switch d.Name {
		case "paginate", "list", "prepend", "append", "allLists", "parentID", "loading", "required", "optimisticKey", "blocking", "cache", "mask_disable", "mask":
		default:
			kept = append(kept, d)
		}
	}
	return kept
}

// cleanPath cleans the file path for display.
func cleanPath(path string) string {
	// Remove leading directories up to operations/
	if idx := strings.Index(path, "operations/"); idx >= 0 {
		return path[idx:]
	}
	return path
}
