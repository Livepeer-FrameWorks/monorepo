package cmd

import (
	"bytes"
	"strings"
	"testing"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/pkg/inventory"
)

func TestRequirePlatformIfImplicitManifest(t *testing.T) {
	cases := []struct {
		name        string
		source      inventory.ManifestSource
		persona     fwcfg.Persona
		wantErr     bool
		wantWarn    bool
		errContains string
	}{
		{name: "manifest flag + selfhosted", source: inventory.SourceManifestFlag, persona: fwcfg.PersonaSelfHosted, wantErr: false},
		{name: "manifest flag + user", source: inventory.SourceManifestFlag, persona: fwcfg.PersonaUser, wantErr: false},
		{name: "manifest env + selfhosted", source: inventory.SourceManifestEnv, persona: fwcfg.PersonaSelfHosted, wantErr: false},
		{name: "gitops dir flag + selfhosted", source: inventory.SourceGitopsDirFlag, persona: fwcfg.PersonaSelfHosted, wantErr: false},
		{name: "github repo flag + user", source: inventory.SourceGithubRepoFlag, persona: fwcfg.PersonaUser, wantErr: false},
		{name: "cwd + platform (no warn)", source: inventory.SourceCwdHeuristic, persona: fwcfg.PersonaPlatform, wantErr: false, wantWarn: false},
		{name: "cwd + selfhosted (warn)", source: inventory.SourceCwdHeuristic, persona: fwcfg.PersonaSelfHosted, wantErr: false, wantWarn: true},
		{name: "cwd + empty persona (no warn)", source: inventory.SourceCwdHeuristic, persona: "", wantErr: false, wantWarn: false},
		{name: "context + platform", source: inventory.SourceContext, persona: fwcfg.PersonaPlatform, wantErr: false},
		{name: "context + selfhosted", source: inventory.SourceContext, persona: fwcfg.PersonaSelfHosted, wantErr: true, errContains: "--manifest"},
		{name: "context + user", source: inventory.SourceContext, persona: fwcfg.PersonaUser, wantErr: true, errContains: "platform"},
		{name: "context-last-manifest + selfhosted", source: inventory.SourceContextLastManifest, persona: fwcfg.PersonaSelfHosted, wantErr: true, errContains: "--manifest"},
		{name: "context-last-manifest + platform", source: inventory.SourceContextLastManifest, persona: fwcfg.PersonaPlatform, wantErr: false},
		{name: "context + no active context", source: inventory.SourceContext, persona: "", wantErr: true, errContains: "no active context"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rc := &resolvedCluster{
				Source:       tc.source,
				Persona:      tc.persona,
				ContextName:  "test-ctx",
				ManifestPath: "/tmp/cluster.yaml",
			}
			out := &bytes.Buffer{}
			err := requirePlatformIfImplicitManifest(rc, out)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if tc.errContains != "" && !strings.Contains(err.Error(), tc.errContains) {
					t.Fatalf("error %q missing %q", err.Error(), tc.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			gotWarn := strings.Contains(out.String(), "[warn]")
			if gotWarn != tc.wantWarn {
				t.Fatalf("warn = %v (output=%q), want %v", gotWarn, out.String(), tc.wantWarn)
			}
		})
	}
}
