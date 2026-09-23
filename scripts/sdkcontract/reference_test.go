package main

import (
	"strings"
	"testing"
)

func TestReferenceExcludesPrivateRootsAndShowsSDKBoundary(t *testing.T) {
	schema, err := loadSchema("../..", headRef)
	if err != nil {
		t.Fatal(err)
	}
	files, err := renderReference(schema, map[string][]string{"query.stream": {"GetStream"}})
	if err != nil {
		t.Fatal(err)
	}
	queries := files[referenceDir+"/queries.mdx"]
	if strings.Contains(queries, "## platform\n") || strings.Contains(queries, "## bootstrapTokensConnection\n") {
		t.Fatal("private query exposed")
	}
	if !strings.Contains(queries, "**Typed SDK document:** `GetStream`") {
		t.Fatal("SDK document boundary missing")
	}
	mutations := files[referenceDir+"/mutations.mdx"]
	if strings.Contains(mutations, "## createBootstrapToken\n") || strings.Contains(mutations, "## revokeBootstrapToken\n") {
		t.Fatal("private mutation exposed")
	}
	for path, page := range files {
		if strings.Contains(page, "## Platform\n") || strings.Contains(page, "## PlatformTenantIndex\n") {
			t.Fatalf("private platform type exposed in %s", path)
		}
	}
}
