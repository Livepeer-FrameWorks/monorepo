package inventory

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	fwcfg "frameworks/cli/internal/config"
)

type ManifestSource string

const (
	SourceManifestFlag   ManifestSource = "manifest-flag"
	SourceGitopsDirFlag  ManifestSource = "gitops-dir-flag"
	SourceGithubRepoFlag ManifestSource = "github-repo-flag"
	SourceManifestEnv    ManifestSource = "manifest-env"
	SourceGitopsDirEnv   ManifestSource = "gitops-dir-env"
	SourceGithubRepoEnv  ManifestSource = "github-repo-env"
	SourceContext        ManifestSource = "context"
	SourceCwdHeuristic   ManifestSource = "cwd"
)

// StringFlag carries a flag value plus cobra's Changed() bit so the
// resolver can distinguish "not passed" from "explicitly set to empty".
type StringFlag struct {
	Value   string
	Changed bool
}

type Int64Flag struct {
	Value   int64
	Changed bool
}

type GithubFetcher interface {
	Fetch(ctx context.Context, in GithubFetchInput) (GithubFetchResult, error)
}

type GithubFetchInput struct {
	Repo           string
	Ref            string
	ManifestPath   string
	AgeKeyPath     string
	AppID          int64
	InstallationID int64
	PrivateKeyPath string
	Stdout         io.Writer
}

type GithubFetchResult struct {
	Path    string
	TmpDir  string
	Cleanup func()
}

type ResolveInput struct {
	Manifest    StringFlag
	GitopsDir   StringFlag
	GithubRepo  StringFlag
	GithubRef   StringFlag
	Cluster     StringFlag
	AgeKey      StringFlag
	GithubAppID Int64Flag
	GithubInst  Int64Flag
	GithubKey   StringFlag

	Env         fwcfg.Env
	Context     fwcfg.Context
	GitHubCfg   *fwcfg.GitHubApp
	Cwd         string
	GithubFetch GithubFetcher
	Stdout      io.Writer

	Ctx context.Context
}

type Resolved struct {
	Path    string
	AgeKey  string
	Source  ManifestSource
	Cluster string
	Cleanup func()
}

func noopCleanup() {}

func ResolveManifestSource(in ResolveInput) (Resolved, error) {
	if in.Env == nil {
		in.Env = fwcfg.OSEnv{}
	}
	if in.Stdout == nil {
		in.Stdout = os.Stdout
	}

	ageKey := firstNonEmpty(in.AgeKey.valueIfChanged(), in.Env.Get("SOPS_AGE_KEY_FILE"))
	if ageKey == "" && in.Context.Gitops != nil {
		ageKey = in.Context.Gitops.AgeKeyPath
	}

	if in.Manifest.Changed && in.Manifest.Value != "" {
		return Resolved{Path: in.Manifest.Value, AgeKey: ageKey, Source: SourceManifestFlag, Cleanup: noopCleanup}, nil
	}
	if in.GitopsDir.Changed && in.GitopsDir.Value != "" {
		path, cluster, err := fromLocalRepo(in.GitopsDir.Value, in.Cluster.valueIfChanged(), in.Stdout)
		if err != nil {
			return Resolved{}, err
		}
		return Resolved{Path: path, AgeKey: ageKey, Source: SourceGitopsDirFlag, Cluster: cluster, Cleanup: noopCleanup}, nil
	}
	if in.GithubRepo.Changed && in.GithubRepo.Value != "" {
		res, err := fromGithub(in, in.GithubRepo.Value, in.GithubRef.valueIfChanged(), in.Cluster.valueIfChanged(), ageKey, SourceGithubRepoFlag)
		return res, err
	}
	if v := in.Env.Get("FRAMEWORKS_MANIFEST"); v != "" {
		return Resolved{Path: v, AgeKey: ageKey, Source: SourceManifestEnv, Cleanup: noopCleanup}, nil
	}
	if v := in.Env.Get("FRAMEWORKS_GITOPS_DIR"); v != "" {
		path, cluster, err := fromLocalRepo(v, in.Env.Get("FRAMEWORKS_CLUSTER"), in.Stdout)
		if err != nil {
			return Resolved{}, err
		}
		return Resolved{Path: path, AgeKey: ageKey, Source: SourceGitopsDirEnv, Cluster: cluster, Cleanup: noopCleanup}, nil
	}
	if v := in.Env.Get("FRAMEWORKS_GITHUB_REPO"); v != "" {
		res, err := fromGithub(in, v, in.Env.Get("FRAMEWORKS_GITHUB_REF"), in.Env.Get("FRAMEWORKS_CLUSTER"), ageKey, SourceGithubRepoEnv)
		return res, err
	}
	if g := in.Context.Gitops; g != nil && g.Source != "" {
		return fromContext(in, g, ageKey)
	}
	if looksLikeGitopsRoot(in.Cwd) {
		path, cluster, err := fromLocalRepo(in.Cwd, "", in.Stdout)
		if err != nil {
			return Resolved{}, err
		}
		return Resolved{Path: path, AgeKey: ageKey, Source: SourceCwdHeuristic, Cluster: cluster, Cleanup: noopCleanup}, nil
	}

	return Resolved{}, noManifestError()
}

func (f StringFlag) valueIfChanged() string {
	if f.Changed {
		return f.Value
	}
	return ""
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

func fromLocalRepo(repo, cluster string, stdout io.Writer) (path, picked string, err error) {
	abs, err := filepath.Abs(repo)
	if err != nil {
		return "", "", fmt.Errorf("resolve gitops dir %s: %w", repo, err)
	}
	if st, err := os.Stat(abs); err != nil || !st.IsDir() {
		return "", "", fmt.Errorf("gitops dir %s is not a directory", abs)
	}
	if cluster == "" {
		names, err := listClusters(abs)
		if err != nil {
			return "", "", err
		}
		switch len(names) {
		case 0:
			return "", "", fmt.Errorf("no clusters/ subdirectory under %s", abs)
		case 1:
			cluster = names[0]
			fmt.Fprintf(stdout, "Using cluster: %s (auto-picked)\n", cluster)
		default:
			return "", "", fmt.Errorf("multiple clusters under %s: %v. Pass --cluster <name> or set it in this context (context set-gitops-cluster)", abs, names)
		}
	}
	path = filepath.Join(abs, "clusters", cluster, "cluster.yaml")
	if _, err := os.Stat(path); err != nil {
		return "", "", fmt.Errorf("manifest %s: %w", path, err)
	}
	return path, cluster, nil
}

func listClusters(root string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, "clusters"))
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func fromGithub(in ResolveInput, repo, ref, cluster, ageKey string, source ManifestSource) (Resolved, error) {
	if in.GithubFetch == nil {
		return Resolved{}, fmt.Errorf("github manifest source requires a fetcher; none provided")
	}
	appID, instID, keyPath, err := resolveGithubCreds(in)
	if err != nil {
		return Resolved{}, err
	}
	if cluster == "" {
		return Resolved{}, fmt.Errorf("github manifest source requires --cluster <name> (or FRAMEWORKS_CLUSTER, or context set-gitops-cluster)")
	}
	if ref == "" {
		if in.GitHubCfg != nil && in.GitHubCfg.Ref != "" {
			ref = in.GitHubCfg.Ref
		} else {
			ref = "main"
		}
	}
	manifestPath := fmt.Sprintf("clusters/%s/cluster.yaml", cluster)

	res, err := in.GithubFetch.Fetch(in.Ctx, GithubFetchInput{
		Repo:           repo,
		Ref:            ref,
		ManifestPath:   manifestPath,
		AgeKeyPath:     ageKey,
		AppID:          appID,
		InstallationID: instID,
		PrivateKeyPath: keyPath,
		Stdout:         in.Stdout,
	})
	if err != nil {
		return Resolved{}, err
	}
	return Resolved{
		Path:    res.Path,
		AgeKey:  ageKey,
		Source:  source,
		Cluster: cluster,
		Cleanup: res.Cleanup,
	}, nil
}

// resolveGithubCreds precedence: flag → env → cfg.GitHub.
func resolveGithubCreds(in ResolveInput) (appID, instID int64, keyPath string, err error) {
	appID = in.GithubAppID.Value
	instID = in.GithubInst.Value
	keyPath = in.GithubKey.valueIfChanged()

	if !in.GithubAppID.Changed {
		if v := in.Env.Get("FRAMEWORKS_GITHUB_APP_ID"); v != "" {
			if parsed, perr := parseInt64(v); perr == nil {
				appID = parsed
			}
		}
	}
	if !in.GithubInst.Changed {
		if v := in.Env.Get("FRAMEWORKS_GITHUB_INSTALLATION_ID"); v != "" {
			if parsed, perr := parseInt64(v); perr == nil {
				instID = parsed
			}
		}
	}
	if keyPath == "" {
		if v := in.Env.Get("FRAMEWORKS_GITHUB_PRIVATE_KEY"); v != "" {
			keyPath = v
		}
	}

	if in.GitHubCfg != nil {
		if appID == 0 {
			appID = in.GitHubCfg.AppID
		}
		if instID == 0 {
			instID = in.GitHubCfg.InstallationID
		}
		if keyPath == "" {
			keyPath = in.GitHubCfg.PrivateKeyPath
		}
	}

	if appID == 0 || instID == 0 || keyPath == "" {
		return 0, 0, "", fmt.Errorf(`github manifest source requires GitHub App credentials. Provide via:
  --github-app-id <n> --github-installation-id <n> --github-private-key <path>
  or FRAMEWORKS_GITHUB_APP_ID / FRAMEWORKS_GITHUB_INSTALLATION_ID / FRAMEWORKS_GITHUB_PRIVATE_KEY env vars
  or 'frameworks config set github.app-id ...' / github.installation-id / github.private-key`)
	}
	return appID, instID, keyPath, nil
}

func parseInt64(s string) (int64, error) {
	s = strings.TrimSpace(s)
	var n int64
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not an integer: %q", s)
		}
		n = n*10 + int64(r-'0')
	}
	if s == "" {
		return 0, fmt.Errorf("empty int")
	}
	return n, nil
}

func fromContext(in ResolveInput, g *fwcfg.Gitops, ageKey string) (Resolved, error) {
	switch g.Source {
	case fwcfg.GitopsManifest:
		if g.ManifestPath == "" {
			return Resolved{}, fmt.Errorf("context gitops.source=manifest but manifest_path is empty")
		}
		return Resolved{Path: g.ManifestPath, AgeKey: ageKey, Source: SourceContext, Cleanup: noopCleanup}, nil
	case fwcfg.GitopsLocal:
		if g.LocalPath == "" {
			return Resolved{}, fmt.Errorf("context gitops.source=local but local_path is empty")
		}
		if g.ManifestPath != "" {
			return Resolved{Path: filepath.Join(g.LocalPath, g.ManifestPath), AgeKey: ageKey, Source: SourceContext, Cluster: g.Cluster, Cleanup: noopCleanup}, nil
		}
		path, cluster, err := fromLocalRepo(g.LocalPath, g.Cluster, in.Stdout)
		if err != nil {
			return Resolved{}, err
		}
		return Resolved{Path: path, AgeKey: ageKey, Source: SourceContext, Cluster: cluster, Cleanup: noopCleanup}, nil
	case fwcfg.GitopsGitHub:
		if g.Repo == "" {
			return Resolved{}, fmt.Errorf("context gitops.source=github but repo is empty")
		}
		return fromGithub(in, g.Repo, g.Ref, g.Cluster, ageKey, SourceContext)
	default:
		return Resolved{}, fmt.Errorf("unknown context gitops.source: %q", g.Source)
	}
}

// looksLikeGitopsRoot requires BOTH markers and does not walk parents —
// running in a subdirectory must not silently pick up an ancestor repo.
func looksLikeGitopsRoot(dir string) bool {
	if dir == "" {
		return false
	}
	st, err := os.Stat(filepath.Join(dir, "clusters"))
	if err != nil || !st.IsDir() {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, ".sops.yaml")); err != nil {
		return false
	}
	return true
}

func noManifestError() error {
	return fmt.Errorf(`no manifest source configured. Choose one:
  - pass --manifest <path>
  - pass --gitops-dir <path> --cluster <name>
  - pass --github-repo <owner/repo> --cluster <name>
  - run 'frameworks setup' to save a default`)
}
