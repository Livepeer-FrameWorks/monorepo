package main

import (
	"bufio"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestComputeWebappSourceHashIncludesWorkspaceSources(t *testing.T) {
	root := writeFakeMonorepo(t, map[string]string{
		".npmrc":                                  "link-workspace-packages=true\n",
		"package.json":                            `{"name":"root","private":true}`,
		"pnpm-lock.yaml":                          "lockfileVersion: '9.0'\n",
		"pnpm-workspace.yaml":                     "packages:\n  - webapp\n  - packages/player\n",
		"webapp/Dockerfile":                       "FROM node:24-alpine\n",
		"webapp/package.json":                     `{"name":"webapp"}`,
		"webapp/src/main.ts":                      "console.log('app')\n",
		"packages/player/package.json":            `{"name":"player"}`,
		"packages/player/src/index.ts":            "export const player = 1\n",
		"packages/player/node_modules/ignored.js": "generated\n",
	})
	inputs := WebappHashInputs{
		MonorepoRoot: root,
		Webapp: ReleaseWebapp{
			Name: "webapp", Context: "webapp", BuildDir: "build",
			ExtraHashPaths: []string{"packages/player"},
		},
		WorkflowSalt:         "workflow",
		NodeToolchainVersion: "24",
	}

	first, contributing, err := ComputeWebappSourceHash(inputs)
	if err != nil {
		t.Fatal(err)
	}
	playerSource := filepath.Join(root, "packages", "player", "src", "index.ts")
	if !slices.Contains(contributing, playerSource) {
		t.Fatalf("workspace source missing from contributing files: %v", contributing)
	}
	ignoredDependency := filepath.Join(root, "packages", "player", "node_modules", "ignored.js")
	if slices.Contains(contributing, ignoredDependency) {
		t.Fatalf("generated dependency entered source hash: %v", contributing)
	}
	if writeErr := os.WriteFile(filepath.Join(root, "packages/player/src/index.ts"), []byte("export const player = 2\n"), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}
	second, _, err := ComputeWebappSourceHash(inputs)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("workspace source edit did not invalidate webapp hash")
	}
}

func TestReleaseWebappsDeclareCompiledWorkspaceSources(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	components, err := LoadComponentsFromFile(filepath.Join(root, ".github", "release-components.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"chartroom": {"npm_player", "npm_studio", "packages/site-config", "packages/map-core"},
		"foredeck":  {"npm_player", "packages/site-config", "packages/map-core"},
		"logbook":   {"packages/site-config"},
	}
	for _, webapp := range components.Webapps {
		for _, path := range want[webapp.Name] {
			if !slices.Contains(webapp.ExtraHashPaths, path) {
				t.Errorf("%s does not hash compiled workspace source %q", webapp.Name, path)
			}
		}
	}
}

func TestReleaseWebappDockerCopyInputsAreHashed(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	components, err := LoadComponentsFromFile(filepath.Join(root, ".github", "release-components.json"))
	if err != nil {
		t.Fatal(err)
	}
	rootInputs := []string{"package.json", "pnpm-lock.yaml", "pnpm-workspace.yaml", ".npmrc"}
	graphqlInputs := []string{"pkg/graphql/schema.graphql", "pkg/graphql/operations"}

	for _, webapp := range components.Webapps {
		webapp := webapp
		t.Run(webapp.Name, func(t *testing.T) {
			dockerfile := filepath.Join(root, webapp.Context, "Dockerfile")
			file, openErr := os.Open(dockerfile)
			if openErr != nil {
				t.Fatal(openErr)
			}
			defer file.Close()

			covered := append([]string{}, rootInputs...)
			covered = append(covered, webapp.Context)
			covered = append(covered, webapp.ExtraHashPaths...)
			if webappConsumesGraphQL(filepath.Join(root, webapp.Context)) {
				covered = append(covered, graphqlInputs...)
			}

			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if !strings.HasPrefix(line, "COPY ") || strings.HasPrefix(line, "COPY --from=") {
					continue
				}
				fields := strings.Fields(line)
				if len(fields) < 3 || strings.HasPrefix(fields[1], "[") {
					t.Fatalf("unsupported COPY form %q; update the release-hash coverage test", line)
				}
				for _, source := range fields[1 : len(fields)-1] {
					source = filepath.ToSlash(filepath.Clean(strings.TrimSuffix(source, "/")))
					if !pathCoveredBy(source, covered) {
						t.Errorf("Docker COPY source %q is absent from root inputs, automatic GraphQL inputs, and extra_hash_paths", source)
					}
				}
			}
			if scanErr := scanner.Err(); scanErr != nil {
				t.Fatal(scanErr)
			}
		})
	}
}

func pathCoveredBy(path string, roots []string) bool {
	for _, root := range roots {
		root = filepath.ToSlash(filepath.Clean(strings.TrimSuffix(root, "/")))
		if path == root || strings.HasPrefix(path, root+"/") {
			return true
		}
	}
	return false
}
