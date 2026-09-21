package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

// schemaCache loads each schema revision once.
type schemaCache struct {
	repo    string
	schemas map[string]*ast.Schema
}

func newSchemaCache(repo string) *schemaCache {
	return &schemaCache{repo: repo, schemas: map[string]*ast.Schema{}}
}

func (c *schemaCache) get(ref string) (*ast.Schema, error) {
	if s, ok := c.schemas[ref]; ok {
		return s, nil
	}
	s, err := loadSchema(c.repo, ref)
	if err != nil {
		return nil, err
	}
	c.schemas[ref] = s
	return s, nil
}

func validates(schema *ast.Schema, document string) error {
	_, errs := gqlparser.LoadQueryWithRules(schema, document, nil)
	if len(errs) > 0 {
		msgs := make([]string, 0, len(errs))
		for _, e := range errs {
			msgs = append(msgs, e.Message)
		}
		return errors.New(strings.Join(msgs, "; "))
	}
	return nil
}

// releaseMatrix is the set of schemas an SDK line is checked against: every
// reachable stable tag at or above the line's minimum server, then the
// working tree as the pending release.
type releaseMatrix struct {
	Tags    []semver
	Pending semver
}

func (m releaseMatrix) from(min semver) []semver {
	var out []semver
	for _, t := range m.Tags {
		if !t.Less(min) {
			out = append(out, t)
		}
	}
	return out
}

// sinceOf returns the first release an operation validates on, and fails when
// it stops validating on a later release or on the working tree.
func sinceOf(cache *schemaCache, matrix releaseMatrix, min semver, document string) (semver, error) {
	var since *semver
	for _, tag := range matrix.from(min) {
		schema, err := cache.get(tag.String())
		if err != nil {
			return semver{}, err
		}
		verr := validates(schema, document)
		switch {
		case verr == nil && since == nil:
			t := tag
			since = &t
		case verr != nil && since != nil:
			return semver{}, fmt.Errorf("validates on %s but not on %s: %w", since, tag, verr)
		}
	}
	head, err := cache.get(headRef)
	if err != nil {
		return semver{}, err
	}
	if verr := validates(head, document); verr != nil {
		return semver{}, fmt.Errorf("does not validate on the working tree (%s): %w", matrix.Pending, verr)
	}
	if since == nil {
		return matrix.Pending, nil
	}
	return *since, nil
}

func loadMatrix(repo string) (releaseMatrix, error) {
	tags, err := stableTags(repo)
	if err != nil {
		return releaseMatrix{}, err
	}
	pending, err := pendingRelease(repo)
	if err != nil {
		return releaseMatrix{}, err
	}
	if len(tags) > 0 && pending.Less(tags[len(tags)-1]) {
		pending = tags[len(tags)-1]
	}
	return releaseMatrix{Tags: tags, Pending: pending}, nil
}

// currentLineOperations computes the manifest entry of the current line from
// the operation files.
func currentLineOperations(cache *schemaCache, matrix releaseMatrix, line supportLine, ops []operation) ([]manifestOperation, []string) {
	min, _ := parseSemver(line.MinServer)
	var out []manifestOperation
	var problems []string
	for _, op := range ops {
		since, err := sinceOf(cache, matrix, min, op.Document)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", op.Name, err))
			continue
		}
		out = append(out, manifestOperation{Name: op.Name, Kind: op.Kind, Since: since.String(), Hash: op.Hash, Document: op.Document})
	}
	return out, problems
}

// latestSDKRelease returns the newest sdk-v<line>.<patch> tag of a line, or ""
// when the line has not been released.
func latestSDKRelease(repo, line string) (string, error) {
	out, err := gitOutput(repo, "tag", "--merged", "HEAD", "--list", "sdk-v"+line+".*")
	if err != nil {
		return "", err
	}
	best, bestPatch := "", -1
	for _, tag := range strings.Fields(string(out)) {
		var patch int
		if _, err := fmt.Sscanf(strings.TrimPrefix(tag, "sdk-v"+line+"."), "%d", &patch); err == nil && patch > bestPatch {
			best, bestPatch = tag, patch
		}
	}
	return best, nil
}

// releasedLine reads a line's manifest entry as of an SDK release tag.
func releasedLine(repo, tag string, major int, line string) (*manifestLine, error) {
	path := fmt.Sprintf("%s/v%d.json", majorsDir, major)
	out, err := gitOutput(repo, "show", tag+":"+path)
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(out, &m); err != nil {
		return nil, err
	}
	l, ok := m.Lines[line]
	if !ok {
		return nil, nil
	}
	return &l, nil
}

// usage is the part of a schema the live operations depend on.
type usage struct {
	outputFields map[string]bool // Type.field
	arguments    map[string]bool // Type.field(arg)
	inputTypes   map[string]bool
	enumsInInput map[string]bool
}

func newUsage() *usage {
	return &usage{outputFields: map[string]bool{}, arguments: map[string]bool{}, inputTypes: map[string]bool{}, enumsInInput: map[string]bool{}}
}

// collectUsage records what document selects, resolved against schema. Parts
// the schema does not define are skipped: an operation newer than the schema
// cannot be broken by it.
func collectUsage(schema *ast.Schema, document string, u *usage) error {
	doc, err := parser.ParseQuery(&ast.Source{Input: document})
	if err != nil {
		return err
	}
	fragments := map[string]*ast.FragmentDefinition{}
	for _, f := range doc.Fragments {
		fragments[f.Name] = f
	}
	for _, op := range doc.Operations {
		var root *ast.Definition
		switch op.Operation {
		case ast.Query:
			root = schema.Query
		case ast.Mutation:
			root = schema.Mutation
		case ast.Subscription:
			root = schema.Subscription
		}
		if root == nil {
			continue
		}
		for _, v := range op.VariableDefinitions {
			markInput(schema, v.Type.Name(), u)
		}
		walkUsage(schema, root, op.SelectionSet, fragments, u, map[string]bool{})
	}
	return nil
}

func walkUsage(schema *ast.Schema, parent *ast.Definition, set ast.SelectionSet, fragments map[string]*ast.FragmentDefinition, u *usage, visiting map[string]bool) {
	for _, sel := range set {
		switch s := sel.(type) {
		case *ast.Field:
			if strings.HasPrefix(s.Name, "__") {
				continue
			}
			def := parent.Fields.ForName(s.Name)
			if def == nil {
				continue
			}
			key := parent.Name + "." + s.Name
			u.outputFields[key] = true
			for _, arg := range s.Arguments {
				u.arguments[key+"("+arg.Name+")"] = true
			}
			if child := schema.Types[def.Type.Name()]; child != nil && len(s.SelectionSet) > 0 {
				walkUsage(schema, child, s.SelectionSet, fragments, u, visiting)
			}
		case *ast.InlineFragment:
			target := parent
			if s.TypeCondition != "" {
				target = schema.Types[s.TypeCondition]
			}
			if target != nil {
				walkUsage(schema, target, s.SelectionSet, fragments, u, visiting)
			}
		case *ast.FragmentSpread:
			frag := fragments[s.Name]
			if frag == nil || visiting[s.Name] {
				continue
			}
			if target := schema.Types[frag.TypeCondition]; target != nil {
				visiting[s.Name] = true
				walkUsage(schema, target, frag.SelectionSet, fragments, u, visiting)
				delete(visiting, s.Name)
			}
		}
	}
}

func markInput(schema *ast.Schema, name string, u *usage) {
	def := schema.Types[name]
	if def == nil {
		return
	}
	switch def.Kind {
	case ast.Enum:
		u.enumsInInput[name] = true
	case ast.InputObject:
		if u.inputTypes[name] {
			return
		}
		u.inputTypes[name] = true
		for _, f := range def.Fields {
			markInput(schema, f.Type.Name(), u)
		}
	}
}

func deprecated(dirs ast.DirectiveList) bool { return dirs.ForName("deprecated") != nil }

func enumValueDeprecated(schema *ast.Schema, enum, value string) bool {
	if schema == nil || schema.Types[enum] == nil {
		return false
	}
	v := schema.Types[enum].EnumValues.ForName(value)
	return v != nil && deprecated(v.Directives)
}

// outputCompatible reports whether a field of type newT still satisfies a
// client written for oldT: the same named type and list nesting, and no
// position that was non-null becoming nullable.
func outputCompatible(oldT, newT *ast.Type) bool {
	if oldT == nil || newT == nil {
		return oldT == newT
	}
	if oldT.NonNull && !newT.NonNull {
		return false
	}
	if (oldT.Elem == nil) != (newT.Elem == nil) {
		return false
	}
	if oldT.Elem != nil {
		return outputCompatible(oldT.Elem, newT.Elem)
	}
	return oldT.NamedType == newT.NamedType
}

// inputCompatible reports whether a value a client built for oldT is still
// accepted as newT: the same named type and list nesting, and no nullable
// position becoming required.
func inputCompatible(oldT, newT *ast.Type) bool {
	if oldT == nil || newT == nil {
		return oldT == newT
	}
	if !oldT.NonNull && newT.NonNull {
		return false
	}
	if (oldT.Elem == nil) != (newT.Elem == nil) {
		return false
	}
	if oldT.Elem != nil {
		return inputCompatible(oldT.Elem, newT.Elem)
	}
	return oldT.NamedType == newT.NamedType
}

// breakingChanges lists the changes from oldS to newS that break a part of
// the schema u records. Elements deprecated in exempt (the oldest live
// minimum release, when it is tagged) may be removed or changed.
func breakingChanges(oldS, newS, exempt *ast.Schema, u *usage) []string {
	var out []string
	isExempt := func(typeName, field string) bool {
		if exempt == nil {
			return false
		}
		def := exempt.Types[typeName]
		if def == nil {
			return false
		}
		if field == "" {
			return deprecated(def.Directives)
		}
		if f := def.Fields.ForName(field); f != nil {
			return deprecated(f.Directives)
		}
		return false
	}

	for key := range u.outputFields {
		typeName, field, _ := strings.Cut(key, ".")
		oldDef := oldS.Types[typeName].Fields.ForName(field)
		newType := newS.Types[typeName]
		var newDef *ast.FieldDefinition
		if newType != nil {
			newDef = newType.Fields.ForName(field)
		}
		switch {
		case isExempt(typeName, field):
		case newDef == nil:
			out = append(out, fmt.Sprintf("%s was removed", key))
		case !outputCompatible(oldDef.Type, newDef.Type):
			out = append(out, fmt.Sprintf("%s changed type from %s to %s", key, oldDef.Type, newDef.Type))
		default:
			for _, arg := range newDef.Arguments {
				if arg.Type.NonNull && arg.DefaultValue == nil && oldDef.Arguments.ForName(arg.Name) == nil {
					out = append(out, fmt.Sprintf("%s gained required argument %s", key, arg.Name))
				}
			}
		}
	}
	for key := range u.arguments {
		fieldKey, argPart, _ := strings.Cut(key, "(")
		argName := strings.TrimSuffix(argPart, ")")
		typeName, field, _ := strings.Cut(fieldKey, ".")
		if isExempt(typeName, field) {
			continue
		}
		oldArg := oldS.Types[typeName].Fields.ForName(field).Arguments.ForName(argName)
		newType := newS.Types[typeName]
		if oldArg == nil || newType == nil || newType.Fields.ForName(field) == nil {
			continue
		}
		newArg := newType.Fields.ForName(field).Arguments.ForName(argName)
		switch {
		case newArg == nil:
			out = append(out, fmt.Sprintf("argument %s was removed", key))
		case !inputCompatible(oldArg.Type, newArg.Type):
			out = append(out, fmt.Sprintf("argument %s changed type from %s to %s", key, oldArg.Type, newArg.Type))
		}
	}
	for name := range u.inputTypes {
		oldDef, newDef := oldS.Types[name], newS.Types[name]
		if isExempt(name, "") {
			continue
		}
		if newDef == nil {
			out = append(out, fmt.Sprintf("input %s was removed", name))
			continue
		}
		for _, f := range oldDef.Fields {
			nf := newDef.Fields.ForName(f.Name)
			switch {
			case nf == nil:
				out = append(out, fmt.Sprintf("input field %s.%s was removed", name, f.Name))
			case !inputCompatible(f.Type, nf.Type):
				out = append(out, fmt.Sprintf("input field %s.%s changed type from %s to %s", name, f.Name, f.Type, nf.Type))
			}
		}
		for _, nf := range newDef.Fields {
			if oldDef.Fields.ForName(nf.Name) == nil && nf.Type.NonNull && nf.DefaultValue == nil {
				out = append(out, fmt.Sprintf("input %s gained required field %s", name, nf.Name))
			}
		}
	}
	for name := range u.enumsInInput {
		oldDef, newDef := oldS.Types[name], newS.Types[name]
		if newDef == nil {
			out = append(out, fmt.Sprintf("enum %s was removed", name))
			continue
		}
		for _, v := range oldDef.EnumValues {
			if newDef.EnumValues.ForName(v.Name) == nil && !enumValueDeprecated(exempt, name, v.Name) {
				out = append(out, fmt.Sprintf("enum value %s.%s, accepted as input, was removed", name, v.Name))
			}
		}
	}
	sort.Strings(out)
	return out
}
