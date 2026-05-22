package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"frameworks/cli/pkg/metabase"

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
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.BaseURL = firstNonEmpty(opts.BaseURL, os.Getenv("METABASE_URL"))
			opts.SessionID = firstNonEmpty(opts.SessionID, os.Getenv("METABASE_SESSION_ID"))
			opts.APIKey = firstNonEmpty(opts.APIKey, os.Getenv("METABASE_API_KEY"))
			if opts.SpecPath == "" {
				opts.SpecPath = defaultMetabaseSpecPath()
			}
			opts.Out = cmd.OutOrStdout()
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()
			summary, err := metabase.Sync(ctx, opts)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Metabase sync complete: %d created, %d updated, %d adopted, %d unchanged, %d added to dashboard\n",
				summary.Created, summary.Updated, summary.Adopted, summary.Unchanged, summary.AddedToDashboard)
			return nil
		},
	}
	syncCmd.Flags().StringVar(&opts.BaseURL, "url", "", "Metabase base URL (default: $METABASE_URL)")
	syncCmd.Flags().StringVar(&opts.SessionID, "session-id", "", "Metabase session id (default: $METABASE_SESSION_ID)")
	syncCmd.Flags().StringVar(&opts.APIKey, "api-key", "", "Metabase API key (default: $METABASE_API_KEY)")
	syncCmd.Flags().StringVar(&opts.SpecPath, "file", "", "managed Metabase card spec (default: infrastructure/metabase/periscope_cards.yaml)")
	syncCmd.Flags().StringVar(&opts.Database, "database", "FrameWorks ClickHouse", "Metabase database name")
	syncCmd.Flags().IntVar(&opts.DatabaseID, "database-id", 0, "Metabase database id (overrides --database)")
	syncCmd.Flags().StringVar(&opts.Collection, "collection", "FrameWorks", "Metabase collection name")
	syncCmd.Flags().IntVar(&opts.CollectionID, "collection-id", 0, "Metabase collection id (overrides --collection)")
	syncCmd.Flags().StringVar(&opts.Dashboard, "dashboard", "FrameWorks Periscope", "Metabase dashboard name")
	syncCmd.Flags().IntVar(&opts.DashboardID, "dashboard-id", 0, "Metabase dashboard id (overrides --dashboard)")
	syncCmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "show changes without writing to Metabase")
	syncCmd.Flags().BoolVar(&opts.Adopt, "adopt", false, "mark matching unmanaged cards as Frameworks-managed")
	syncCmd.Flags().BoolVar(&opts.Force, "force", false, "replace existing unmanaged cards")

	cmd.AddCommand(syncCmd)
	return cmd
}

func defaultMetabaseSpecPath() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "infrastructure/metabase/periscope_cards.yaml"
	}
	for dir := cwd; ; dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, "infrastructure/metabase/periscope_cards.yaml")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		if dir == filepath.Dir(dir) {
			return "infrastructure/metabase/periscope_cards.yaml"
		}
	}
}
