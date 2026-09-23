package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

// schemaCache loads the public schema of each revision once.
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
	s, err := loadPublicSchema(c.repo, ref)
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

func deprecated(dirs ast.DirectiveList) bool { return dirs.ForName("deprecated") != nil }

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
