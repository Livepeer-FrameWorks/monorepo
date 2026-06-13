package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"frameworks/cli/pkg/metabase"

	pkgdns "github.com/Livepeer-FrameWorks/monorepo/pkg/dns"
	mbspecs "github.com/Livepeer-FrameWorks/monorepo/pkg/metabase"

	"github.com/spf13/cobra"
)

func newClusterMetabaseCmd() *cobra.Command {
	var opts metabase.SyncOptions

	cmd := &cobra.Command{
		Use:   "metabase",
		Short: "Manage cluster Metabase content",
	}
	syncCmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync managed Metabase cards without overwriting manual edits",
		Long: `Sync the FrameWorks-managed Metabase card specs.

Without --file, every embedded spec is synced to the dashboard it declares
(collections and dashboards are created when missing; the database connection
must already exist in Metabase). The Metabase URL is derived from the cluster
manifest's root_domain and the API key is read from the manifest env_files
(METABASE_API_KEY); flags and environment variables override both.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.BaseURL = firstNonEmpty(opts.BaseURL, os.Getenv("METABASE_URL"))
			opts.SessionID = firstNonEmpty(opts.SessionID, os.Getenv("METABASE_SESSION_ID"))
			opts.APIKey = firstNonEmpty(opts.APIKey, os.Getenv("METABASE_API_KEY"))
			if opts.BaseURL == "" || (opts.APIKey == "" && opts.SessionID == "") {
				if err := fillMetabaseAccessFromManifest(cmd, &opts); err != nil {
					return err
				}
			}
			if opts.Dashboard != "" && opts.SpecPath == "" {
				return errors.New("--dashboard overrides a single spec's target; combine it with --file")
			}
			opts.Out = cmd.OutOrStdout()
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()

			specs, err := metabaseSpecSources(opts.SpecPath)
			if err != nil {
				return err
			}
			var total metabase.SyncSummary
			for _, src := range specs {
				runOpts := opts
				runOpts.SpecPath = src.path
				runOpts.SpecContent = src.content
				fmt.Fprintf(cmd.OutOrStdout(), "Syncing %s...\n", src.name)
				summary, err := metabase.Sync(ctx, runOpts)
				if err != nil {
					return fmt.Errorf("sync %s: %w", src.name, err)
				}
				total.Created += summary.Created
				total.Updated += summary.Updated
				total.Adopted += summary.Adopted
				total.Unchanged += summary.Unchanged
				total.AddedToDashboard += summary.AddedToDashboard
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Metabase sync complete: %d created, %d updated, %d adopted, %d unchanged, %d added to dashboard\n",
				total.Created, total.Updated, total.Adopted, total.Unchanged, total.AddedToDashboard)
			return nil
		},
	}
	syncCmd.Flags().StringVar(&opts.BaseURL, "url", "", "Metabase base URL (default: $METABASE_URL, else derived from the manifest root_domain)")
	syncCmd.Flags().StringVar(&opts.SessionID, "session-id", "", "Metabase session id (default: $METABASE_SESSION_ID)")
	syncCmd.Flags().StringVar(&opts.APIKey, "api-key", "", "Metabase API key (default: $METABASE_API_KEY, else METABASE_API_KEY from the manifest env_files)")
	syncCmd.Flags().StringVar(&opts.SpecPath, "file", "", "sync a single card spec file instead of the embedded specs")
	syncCmd.Flags().StringVar(&opts.Database, "database", "FrameWorks ClickHouse", "Metabase database name")
	syncCmd.Flags().IntVar(&opts.DatabaseID, "database-id", 0, "Metabase database id (overrides --database)")
	syncCmd.Flags().StringVar(&opts.Collection, "collection", "FrameWorks", "Metabase collection name")
	syncCmd.Flags().IntVar(&opts.CollectionID, "collection-id", 0, "Metabase collection id (overrides --collection)")
	syncCmd.Flags().StringVar(&opts.Dashboard, "dashboard", "", "Metabase dashboard name (overrides the spec's dashboard; requires --file)")
	syncCmd.Flags().IntVar(&opts.DashboardID, "dashboard-id", 0, "Metabase dashboard id (overrides --dashboard)")
	syncCmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "show changes without writing to Metabase")
	syncCmd.Flags().BoolVar(&opts.Adopt, "adopt", false, "mark matching unmanaged cards as Frameworks-managed")
	syncCmd.Flags().BoolVar(&opts.Force, "force", false, "replace existing unmanaged cards")

	cmd.AddCommand(syncCmd)
	return cmd
}

type metabaseSpecSource struct {
	name    string
	path    string
	content []byte
}

// metabaseSpecSources returns the card specs to sync: the single --file spec
// when given, otherwise every spec embedded in the binary.
func metabaseSpecSources(specPath string) ([]metabaseSpecSource, error) {
	if specPath != "" {
		return []metabaseSpecSource{{name: specPath, path: specPath}}, nil
	}
	entries, err := mbspecs.Content.ReadDir("specs")
	if err != nil {
		return nil, fmt.Errorf("read embedded metabase specs: %w", err)
	}
	var sources []metabaseSpecSource
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		content, err := mbspecs.Content.ReadFile("specs/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read embedded metabase spec %s: %w", entry.Name(), err)
		}
		sources = append(sources, metabaseSpecSource{name: entry.Name(), content: content})
	}
	if len(sources) == 0 {
		return nil, errors.New("no embedded metabase specs found")
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].name < sources[j].name })
	return sources, nil
}

// fillMetabaseAccessFromManifest derives the Metabase URL from the cluster
// manifest's root_domain and reads METABASE_API_KEY from the manifest
// env_files, for whichever of the two flags/env did not already provide.
func fillMetabaseAccessFromManifest(cmd *cobra.Command, opts *metabase.SyncOptions) error {
	rc, err := resolveClusterManifest(cmd)
	if err != nil {
		return fmt.Errorf("resolve cluster manifest for Metabase access (pass --url and --api-key to skip): %w", err)
	}
	defer rc.Cleanup()

	if opts.BaseURL == "" {
		fqdn, ok := pkgdns.ServiceFQDN("metabase", rc.Manifest.RootDomain)
		if !ok || fqdn == "" {
			return errors.New("manifest has no root_domain to derive the Metabase URL from; pass --url or set $METABASE_URL")
		}
		opts.BaseURL = "https://" + fqdn
	}
	if opts.APIKey == "" && opts.SessionID == "" {
		env, err := rc.SharedEnv()
		if err != nil {
			return fmt.Errorf("load manifest env_files: %w", err)
		}
		opts.APIKey = env["METABASE_API_KEY"]
		if opts.APIKey == "" {
			return errors.New("no Metabase credentials: create an API key (Metabase → Admin settings → Authentication → API keys), store it with `scripts/sops-env.sh set secrets/<cluster>.env METABASE_API_KEY <key>` in your gitops repo, or pass --api-key / $METABASE_API_KEY")
		}
	}
	return nil
}
