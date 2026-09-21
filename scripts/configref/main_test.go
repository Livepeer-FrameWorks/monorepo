package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func copyFixture(t *testing.T) string {
	t.Helper()
	src := filepath.Join("testdata", "repo")
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	return dst
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// MDX ends a {/* */} comment at the first terminator, so a terminator inside
// the header text leaves stray content that fails the docs site build.
func TestRenderPageHeaderCommentClosesOnce(t *testing.T) {
	page := string(renderPage(nil))
	start := strings.Index(page, "{/*")
	if start < 0 {
		t.Fatal("rendered page has no header comment")
	}
	end := strings.Index(page[start:], "*/}")
	if end < 0 {
		t.Fatal("header comment is not closed")
	}
	if body := page[start+len("{/*") : start+end]; strings.Contains(body, "*/") {
		t.Fatalf("header comment text contains a comment terminator: %q", body)
	}
}

func TestGenerateRendersReferenceAndSchema(t *testing.T) {
	outputs, err := generate(copyFixture(t))
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	page := string(outputs[referencePagePath])
	for _, want := range []string{
		"title: \"Configuration Reference\"",
		"## bridge",
		"Read by `api_gateway/cmd/bridge`.",
		"| `PORT` | string | `18000` |",
		"| `SERVICE_TOKEN` | string |  | required, secret | v0.3.0 |",
		"| `GRAPHQL_MAX_DEPTH` | integer | `10` |  | v0.3.1 |",
		"deprecated in v0.3.1, use `GRAPHQL_MAX_DEPTH`",
		"## commodore",
		"| `PORT` | string | `18001` |",
		"### bootstrap subcommand",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("reference page missing %q\n%s", want, page)
		}
	}
	if strings.Index(page, "## bridge") > strings.Index(page, "## commodore") {
		t.Error("services with the same role must be ordered by id")
	}

	var schema schemaFile
	if err := json.Unmarshal(outputs[schemaPath], &schema); err != nil {
		t.Fatalf("schema JSON: %v", err)
	}
	if len(schema.Services) != 3 || schema.Services[0].Service != "bridge" || schema.Services[2].Variant != "bootstrap" {
		t.Fatalf("unexpected schema sections: %+v", schema.Services)
	}
}

func TestGenerateRejectsDirectEnvReadsInMigratedCommand(t *testing.T) {
	root := copyFixture(t)
	writeFile(t, root, "api_gateway/cmd/bridge/extra.go", "package main\n\nimport \"os\"\n\nvar _ = os.Getenv(\"PORT\")\n")
	writeFile(t, root, "api_gateway/cmd/bridge/extra_test.go", "package main\n\nimport \"os\"\n\nvar _ = os.Getenv(\"TEST_ONLY\")\n")
	_, err := generate(root)
	if err == nil || !strings.Contains(err.Error(), "api_gateway/cmd/bridge/extra.go:5") {
		t.Fatalf("expected violation in extra.go, got %v", err)
	}
	if strings.Contains(err.Error(), "extra_test.go") {
		t.Fatalf("test files must not be scanned: %v", err)
	}
}

func TestGenerateRejectsDirectEnvReadsInSharedPackages(t *testing.T) {
	root := copyFixture(t)
	writeFile(t, root, "pkg/geoip/geoip.go", "package geoip\n\nimport (\n\tstdos \"os\"\n)\n\n// os.Getenv(\"IN_COMMENT\") is not a read.\nvar _ = stdos.Getenv(\"GEOIP_MMDB_PATH\")\n")
	writeFile(t, root, "pkg/auth/service.go", "package auth\n\nimport cfg \"github.com/Livepeer-FrameWorks/monorepo/pkg/config\"\n\nvar _ = cfg.GetEnv(\"SERVICE_TOKEN\", \"\")\n")
	writeFile(t, root, "pkg/config/extra.go", "package config\n\nvar _ = IsProduction()\n")
	writeFile(t, root, "pkg/geoip/geoip_test.go", "package geoip\n\nimport \"os\"\n\nvar _ = os.Getenv(\"TEST_ONLY\")\n")
	writeFile(t, root, "pkg/mist/testdata/fixture.go", "package fixture\n\nimport \"os\"\n\nvar _ = os.Getenv(\"FIXTURE\")\n")
	_, err := generate(root)
	if err == nil {
		t.Fatal("expected shared-package env reads to fail generation")
	}
	for _, want := range []string{"pkg/geoip/geoip.go:8: stdos.Getenv", "pkg/auth/service.go:5: cfg.GetEnv", "pkg/config/extra.go:3: IsProduction"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
	for _, unwanted := range []string{"IN_COMMENT", "geoip_test.go", "testdata"} {
		if strings.Contains(err.Error(), unwanted) {
			t.Errorf("error reports %q, which must not be scanned: %v", unwanted, err)
		}
	}
}

// Service internal packages are scanned like shared packages, and a reader
// referenced as a value (assigned or passed) counts as a read, not only a
// call.
func TestGenerateRejectsEnvReadsInServiceInternalPackagesAndReaderValues(t *testing.T) {
	root := copyFixture(t)
	writeFile(t, root, "api_gateway/internal/handlers/tokens.go", "package handlers\n\nimport \"os\"\n\nvar _ = os.Getenv(\"BRIDGE_TOKEN\")\n")
	writeFile(t, root, "api_gateway/internal/handlers/tokens_test.go", "package handlers\n\nimport \"os\"\n\nvar _ = os.Getenv(\"TEST_ONLY\")\n")
	writeFile(t, root, "api_control/internal/lookup/lookup.go", "package lookup\n\nimport \"os\"\n\nfunc read() string {\n\tlookup := os.Getenv\n\treturn lookup(\"X\")\n}\n")
	writeFile(t, root, "pkg/geoip/source.go", "package geoip\n\nimport \"os\"\n\nfunc use(f func(string) (string, bool)) {}\n\nfunc init() { use(os.LookupEnv) }\n")
	writeFile(t, root, "api_gateway/cmd/bridge/extra.go", "package main\n\nimport \"os\"\n\nvar environ = os.Environ\n")
	_, err := generate(root)
	if err == nil {
		t.Fatal("expected internal-package and reader-value env reads to fail generation")
	}
	for _, want := range []string{
		"api_gateway/internal/handlers/tokens.go:5: os.Getenv",
		"api_control/internal/lookup/lookup.go:6: os.Getenv",
		"pkg/geoip/source.go:7: os.LookupEnv",
		"api_gateway/cmd/bridge/extra.go:5: os.Environ",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "tokens_test.go") {
		t.Errorf("test files must not be scanned: %v", err)
	}
}

func TestGenerateAllowsListedSharedPackageEnvReads(t *testing.T) {
	root := copyFixture(t)
	writeFile(t, root, "pkg/config/env.go", "package config\n\nimport \"os\"\n\nfunc GetLogLevel() string { return os.Getenv(\"LOG_LEVEL\") }\n")
	writeFile(t, root, "pkg/logging/logger.go", "package logging\n\nimport \"github.com/Livepeer-FrameWorks/monorepo/pkg/config\"\n\nvar _ = config.GetLogLevel()\n")
	writeFile(t, root, "pkg/testutil/dockerpg/dockerpg.go", "package dockerpg\n\nimport \"os\"\n\nvar _ = os.Getenv(\"FRAMEWORKS_POSTGRES_TEST_IMAGE\")\n")
	if _, err := generate(root); err != nil {
		t.Fatalf("allowlisted reads rejected: %v", err)
	}
}

func TestGenerateRejectsVersionsOutsideCatalog(t *testing.T) {
	root := copyFixture(t)
	writeFile(t, root, "cli/internal/releases/catalog.yaml", "schema_migration_floor: v0.3.0\n\nreleases:\n  - version: v0.3.0\n")
	_, err := generate(root)
	if err == nil || !strings.Contains(err.Error(), "outside the release catalog range") {
		t.Fatalf("expected catalog range error for v0.3.1 fields, got %v", err)
	}
}

func TestGenerateRejectsInvalidAnnotationsAndDirectives(t *testing.T) {
	cases := map[string]string{
		"missing desc":     "package appconfig\n\n//configref:service bridge cmd=cmd/bridge\ntype Bridge struct {\n\tA string `env:\"A_KEY\" introduced:\"v0.3.0\"`\n}\n",
		"untagged field":   "package appconfig\n\n//configref:service bridge cmd=cmd/bridge\ntype Bridge struct {\n\tA string\n}\n",
		"unknown service":  "package appconfig\n\n//configref:service nosuch cmd=cmd/bridge\ntype Bridge struct {}\n",
		"missing cmd":      "package appconfig\n\n//configref:service bridge\ntype Bridge struct {}\n",
		"unsupported type": "package appconfig\n\n//configref:service bridge cmd=cmd/bridge\ntype Bridge struct {\n\tA float64 `env:\"A_KEY\" desc:\"a valid description\" introduced:\"v0.3.0\"`\n}\n",
		"duplicate key":    "package appconfig\n\nimport \"x/config\"\n\n//configref:service bridge cmd=cmd/bridge\ntype Bridge struct {\n\tconfig.HTTPListen\n\tA string `env:\"PORT\" desc:\"a valid description\" introduced:\"v0.3.0\"`\n}\n",
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			root := copyFixture(t)
			writeFile(t, root, "api_gateway/internal/appconfig/config.go", source)
			if _, err := generate(root); err == nil {
				t.Fatal("expected generate to fail")
			}
		})
	}
}
