package introspection

import (
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"sync"

	graphqlpkg "frameworks/pkg/graphql"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

const maxFieldPathDepth = 6

// Template represents a loaded GraphQL operation template.
type Template struct {
	Name          string                 `json:"name"`
	Description   string                 `json:"description"`
	Query         string                 `json:"query"`
	Variables     map[string]interface{} `json:"variables"`
	FilePath      string                 `json:"file_path"`
	OperationType string                 `json:"operation_type"`
	FieldPaths    []string               `json:"field_paths"`
}

// TemplateLoader loads and caches GraphQL templates from embedded files.
type TemplateLoader struct {
	mu        sync.RWMutex
	templates map[string]*Template // key: "query:fieldPath"
	loaded    bool
}

// NewTemplateLoader creates a new template loader.
func NewTemplateLoader() *TemplateLoader {
	return &TemplateLoader{
		templates: make(map[string]*Template),
	}
}

// Load loads all templates from the embedded filesystem.
func (tl *TemplateLoader) Load() error {
	tl.mu.Lock()
	defer tl.mu.Unlock()

	if tl.loaded {
		return nil
	}

	// Load fragments first for resolution
	fragments := make(map[string]string)
	if err := fs.WalkDir(graphqlpkg.OperationsFS, "operations/fragments", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".gql") {
			return err
		}
		content, readErr := fs.ReadFile(graphqlpkg.OperationsFS, path)
		if readErr != nil {
			return readErr
		}
		// Extract fragment name
		if match := regexp.MustCompile(`fragment\s+(\w+)`).FindSubmatch(content); match != nil {
			fragments[string(match[1])] = string(content)
		}
		return nil
	}); err != nil {
		// Fragments directory may not exist, which is fine
	}

	// Load queries, mutations, subscriptions
	for _, opType := range []string{"queries", "mutations", "subscriptions"} {
		dirPath := "operations/" + opType
		if err := fs.WalkDir(graphqlpkg.OperationsFS, dirPath, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".gql") {
				return err
			}
			content, readErr := fs.ReadFile(graphqlpkg.OperationsFS, path)
			if readErr != nil {
				return readErr
			}

			template := tl.parseTemplate(string(content), path, fragments)
			if template != nil {
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
		}); err != nil {
			// Directory may not exist
		}
	}

	tl.loaded = true
	return nil
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

	result := make([]*Template, 0, len(tl.templates))
	seen := make(map[*Template]bool)
	for _, t := range tl.templates {
		if seen[t] {
			continue
		}
		seen[t] = true
		result = append(result, t)
	}
	return result
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

	// Extract operation name from content
	opNameRe := regexp.MustCompile(`(?:query|mutation|subscription)\s+(\w+)`)
	nameMatch := opNameRe.FindStringSubmatch(content)
	if nameMatch == nil {
		return nil
	}

	name := nameMatch[1]
	description := extractDescription(content)

	// Resolve fragment spreads for output
	resolvedQuery := resolveFragments(content, fragments)

	fieldPaths := extractFieldPaths(resolvedQuery)

	return &Template{
		Name:          name,
		Description:   description,
		Query:         resolvedQuery,
		Variables:     extractDefaultVariables(content),
		FilePath:      cleanPath(path),
		OperationType: opType,
		FieldPaths:    fieldPaths,
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

// extractDefaultVariables extracts variable names and generates defaults.
func extractDefaultVariables(content string) map[string]interface{} {
	vars := make(map[string]interface{})

	// Match variable definitions: $varName: Type
	varRe := regexp.MustCompile(`\$(\w+)\s*:\s*([^,\)\s!]+)(!)?`)
	matches := varRe.FindAllStringSubmatch(content, -1)

	for _, match := range matches {
		name := match[1]
		typeName := match[2]

		// Generate sensible defaults
		switch {
		case name == "page":
			vars[name] = map[string]interface{}{"first": 50}
		case name == "timeRange":
			vars[name] = nil // Will be filled by caller
		case name == "streamId":
			vars[name] = "stream_global_id"
		case name == "nodeId":
			vars[name] = "node_id"
		case typeName == "ID":
			vars[name] = "id_placeholder"
		case typeName == "String":
			vars[name] = ""
		case typeName == "Int":
			vars[name] = 0
		case typeName == "Boolean":
			vars[name] = false
		default:
			if strings.HasSuffix(typeName, "Input") {
				vars[name] = map[string]interface{}{}
			} else {
				vars[name] = nil
			}
		}
	}

	return vars
}

// resolveFragments inlines fragment definitions into the query.
func resolveFragments(query string, fragments map[string]string) string {
	// Find all fragment spreads: ...FragmentName (no space between ... and name)
	// Inline fragments have space: "... on Type" so won't match
	spreadRe := regexp.MustCompile(`\.\.\.(\w+)`)
	matches := spreadRe.FindAllStringSubmatch(query, -1)

	required := make(map[string]bool)
	toProcess := make([]string, 0)
	for _, m := range matches {
		toProcess = append(toProcess, m[1])
	}

	// Recursively collect required fragments
	for len(toProcess) > 0 {
		name := toProcess[0]
		toProcess = toProcess[1:]

		if required[name] {
			continue
		}

		if def, ok := fragments[name]; ok {
			required[name] = true
			// Find nested spreads
			nested := spreadRe.FindAllStringSubmatch(def, -1)
			for _, n := range nested {
				if !required[n[1]] {
					toProcess = append(toProcess, n[1])
				}
			}
		}
	}

	// Append fragment definitions
	result := query
	for name := range required {
		if def, ok := fragments[name]; ok {
			result += "\n\n" + strings.TrimSpace(def)
		}
	}

	return result
}

// cleanPath cleans the file path for display.
func cleanPath(path string) string {
	// Remove leading directories up to operations/
	if idx := strings.Index(path, "operations/"); idx >= 0 {
		return path[idx:]
	}
	return path
}
