package mcp

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func TestMCPReferenceMatchesRegisteredSurface(t *testing.T) {
	ctx, session := newTestMCPSession(t)

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	resources, err := session.ListResources(ctx, nil)
	if err != nil {
		t.Fatalf("list resources: %v", err)
	}
	templates, err := session.ListResourceTemplates(ctx, nil)
	if err != nil {
		t.Fatalf("list resource templates: %v", err)
	}

	docs := readMCPReference(t)
	documentedTools := markdownTableIdentifiers(t, docs, "Tools")
	documentedResources := markdownTableIdentifiers(t, docs, "Resources")

	registeredTools := make(map[string]struct{}, len(tools.Tools))
	for _, tool := range tools.Tools {
		registeredTools[tool.Name] = struct{}{}
	}
	registeredResources := make(map[string]struct{}, len(resources.Resources)+len(templates.ResourceTemplates))
	for _, resource := range resources.Resources {
		if _, ok := resourcePolicyForURI(resource.URI); !ok {
			t.Errorf("registered resource %q has no security policy", resource.URI)
		}
		registeredResources[resource.URI] = struct{}{}
	}
	for _, template := range templates.ResourceTemplates {
		if _, ok := resourcePolicyForURI(template.URITemplate); !ok {
			t.Errorf("registered resource template %q has no security policy", template.URITemplate)
		}
		registeredResources[template.URITemplate] = struct{}{}
	}

	assertSameIdentifiers(t, "tools", registeredTools, documentedTools)
	assertSameIdentifiers(t, "resources", registeredResources, documentedResources)
}

func readMCPReference(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate parity test source")
	}
	path := filepath.Join(filepath.Dir(filename), "../../../website_docs/src/content/docs/agents/mcp.mdx")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read MCP reference %s: %v", path, err)
	}
	return string(contents)
}

func markdownTableIdentifiers(t *testing.T, markdown, heading string) map[string]struct{} {
	t.Helper()
	marker := "\n## " + heading + "\n"
	start := strings.Index(markdown, marker)
	if start < 0 {
		t.Fatalf("MCP reference has no %q section", heading)
	}
	section := markdown[start+len(marker):]
	if end := strings.Index(section, "\n## "); end >= 0 {
		section = section[:end]
	}

	result := make(map[string]struct{})
	inTable := false
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			if inTable {
				break
			}
			continue
		}
		inTable = true
		cells := strings.Split(line, "|")
		if len(cells) < 3 {
			continue
		}
		identifier := strings.TrimSpace(cells[1])
		if !strings.HasPrefix(identifier, "`") || !strings.HasSuffix(identifier, "`") {
			continue
		}
		identifier = strings.Trim(identifier, "`")
		result[identifier] = struct{}{}
	}
	if len(result) == 0 {
		t.Fatalf("MCP reference %q table has no identifiers", heading)
	}
	return result
}

func assertSameIdentifiers(t *testing.T, kind string, registered, documented map[string]struct{}) {
	t.Helper()
	missing := setDifference(registered, documented)
	extra := setDifference(documented, registered)
	if len(missing) > 0 || len(extra) > 0 {
		t.Errorf("MCP %s reference drift: missing=%v extra=%v", kind, missing, extra)
	}
}

func setDifference(left, right map[string]struct{}) []string {
	result := make([]string, 0)
	for value := range left {
		if _, ok := right[value]; !ok {
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}
