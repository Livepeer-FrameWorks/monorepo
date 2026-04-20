// Package platformauth resolves platform/service-to-service auth tokens
// from the active manifest/gitops source. SERVICE_TOKEN is platform
// configuration (deployment-time, versioned in gitops), not operator
// identity — it is never read from the OS credential store.
package platformauth

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	fwcfg "frameworks/cli/internal/config"
	fwgitops "frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/inventory"
)

// ResolveManifestServiceToken resolves SERVICE_TOKEN from the active
// context's persisted gitops settings. It reads only the manifest's
// top-level YAML (env_files) — host inventory merge and full-manifest
// validation are skipped so unrelated host-topology issues can't block
// admin or direct-edge flows, and so manifests that carry host data in
// a separate hosts_file still work here.
//
// Resolution is strict: only fwcfg.Context.Gitops is consulted. Env
// overrides like FRAMEWORKS_MANIFEST / FRAMEWORKS_GITOPS_DIR /
// FRAMEWORKS_GITHUB_REPO are NOT honored — admin/service flows must not
// silently pick up a different gitops source than the one the operator
// configured via 'frameworks setup'.
//
// Errors when the context has no gitops source, when the manifest can't
// be resolved, or when SERVICE_TOKEN is missing from env_files. No
// credential-store, keychain, or env-var fallback.
func ResolveManifestServiceToken(ctx context.Context, ctxCfg fwcfg.Context, cfg fwcfg.Config) (string, error) {
	if ctxCfg.Gitops == nil || ctxCfg.Gitops.Source == "" {
		return "", fmt.Errorf(`no gitops source configured for the active context.
Run 'frameworks setup' to point this context at your gitops repo so the CLI
can load SERVICE_TOKEN and other platform secrets from your manifest env_files`)
	}

	in := inventory.ResolveInput{
		Env:         fwcfg.MapEnv{},
		Context:     ctxCfg,
		GitHubCfg:   cfg.GitHub,
		GithubFetch: fwgitops.NewGithubAppFetcher(),
		Stdout:      io.Discard,
		Ctx:         ctx,
	}

	rm, err := inventory.ResolveManifestSource(in)
	if err != nil {
		return "", fmt.Errorf("resolve manifest for service token: %w", err)
	}
	if rm.Cleanup != nil {
		defer rm.Cleanup()
	}

	data, err := os.ReadFile(rm.Path)
	if err != nil {
		return "", fmt.Errorf("read manifest %s: %w", rm.Path, err)
	}
	manifest, err := inventory.ParseManifest(data)
	if err != nil {
		return "", fmt.Errorf("parse manifest %s: %w", rm.Path, err)
	}

	env, err := inventory.LoadSharedEnv(manifest, filepath.Dir(rm.Path), rm.AgeKey)
	if err != nil {
		return "", fmt.Errorf("load manifest env_files: %w", err)
	}

	token := strings.TrimSpace(env["SERVICE_TOKEN"])
	if token == "" {
		return "", fmt.Errorf("SERVICE_TOKEN missing from manifest env_files (%s) — add it to your gitops secrets", rm.Path)
	}
	return token, nil
}
