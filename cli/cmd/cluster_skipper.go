package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	fwssh "frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

func newClusterSkipperCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skipper",
		Short: "Inspect Skipper operational state",
	}
	cmd.AddCommand(newClusterSkipperKnowledgeCmd())
	cmd.AddCommand(newClusterSkipperPostsCmd())
	return cmd
}

func newClusterSkipperKnowledgeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "knowledge",
		Short: "Manage Skipper knowledge crawl state",
	}
	cmd.AddCommand(newClusterSkipperKnowledgeResetCmd())
	return cmd
}

func newClusterSkipperKnowledgeResetCmd() *cobra.Command {
	var allTenants bool
	var dryRun bool
	var source string
	var tenantID string
	var yes bool
	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Clear Skipper knowledge and crawl cache so sources are re-embedded",
		RunE: func(cmd *cobra.Command, _ []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			tenantScope, err := resolveSkipperKnowledgeTenantScope(tenantID, allTenants)
			if err != nil {
				return err
			}
			sshKey := stringFlag(cmd, "ssh-key").Value
			pool := fwssh.NewPool(30*time.Second, sshKey)
			defer pool.Close()
			return runSkipperKnowledgeReset(cmd.Context(), cmd, rc, pool, skipperKnowledgeResetOptions{
				TenantID:   tenantScope,
				AllTenants: allTenants,
				Source:     source,
				DryRun:     dryRun,
				Yes:        yes,
			})
		},
	}
	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "tenant UUID to reset (defaults to active context system tenant)")
	cmd.Flags().BoolVar(&allTenants, "all-tenants", false, "reset knowledge for every tenant")
	cmd.Flags().StringVar(&source, "source", "", "limit reset to one source URL or source root")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show matching row counts without deleting data")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation prompt")
	return cmd
}

func newClusterSkipperPostsCmd() *cobra.Command {
	var limit int
	var status string
	cmd := &cobra.Command{
		Use:   "posts",
		Short: "List recent Skipper social post drafts",
		RunE: func(cmd *cobra.Command, _ []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			sshKey := stringFlag(cmd, "ssh-key").Value
			pool := fwssh.NewPool(30*time.Second, sshKey)
			defer pool.Close()
			return runSkipperPosts(cmd.Context(), cmd, rc, pool, limit, status)
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "maximum rows to show")
	cmd.Flags().StringVar(&status, "status", "", "filter by post status, e.g. draft or sent")
	return cmd
}

func runSkipperPosts(ctx context.Context, cmd *cobra.Command, rc *resolvedCluster, pool *fwssh.Pool, limit int, status string) error {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	exec, conn, err := skipperSQLExecutor(ctx, rc, pool)
	if err != nil {
		return err
	}
	query := `
		SELECT id::text,
		       tenant_id::text,
		       content_type,
		       status,
		       COALESCE(to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS'), ''),
		       COALESCE(to_char(sent_at AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS'), ''),
		       left(replace(COALESCE(context_summary, ''), E'\n', ' '), 120),
		       left(replace(tweet_text, E'\n', ' '), 180)
		FROM skipper.skipper_posts
		WHERE ($1 = '' OR status = $1)
		ORDER BY created_at DESC
		LIMIT $2`
	type row struct {
		id, tenant, contentType, status, created, sent, summary, tweet string
	}
	var rows []row
	if err := exec.QueryRows(ctx, conn, query, []any{status, limit}, func(scan func(dest ...any) error) error {
		var r row
		if err := scan(&r.id, &r.tenant, &r.contentType, &r.status, &r.created, &r.sent, &r.summary, &r.tweet); err != nil {
			return err
		}
		rows = append(rows, r)
		return nil
	}); err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if len(rows) == 0 {
		fmt.Fprintln(out, "No Skipper social posts found.")
		return nil
	}
	fmt.Fprintf(out, "%-19s  %-10s  %-9s  %-10s  %s\n", "created_utc", "type", "status", "sent_utc", "tweet")
	for _, r := range rows {
		sent := r.sent
		if sent == "" {
			sent = "-"
		}
		fmt.Fprintf(out, "%-19s  %-10s  %-9s  %-10s  %s\n", r.created, r.contentType, r.status, sent, strings.TrimSpace(r.tweet))
	}
	return nil
}

type skipperKnowledgeResetOptions struct {
	TenantID   string
	AllTenants bool
	Source     string
	DryRun     bool
	Yes        bool
}

type skipperKnowledgeResetResult struct {
	KnowledgeRows int64
	CacheRows     int64
	CrawlJobs     int64
}

func resolveSkipperKnowledgeTenantScope(tenantID string, allTenants bool) (string, error) {
	tenantID = strings.TrimSpace(tenantID)
	if allTenants {
		if tenantID != "" {
			return "", fmt.Errorf("--tenant-id and --all-tenants are mutually exclusive")
		}
		return "", nil
	}
	if tenantID != "" {
		return tenantID, nil
	}
	active, err := loadActiveContextLax()
	if err != nil || strings.TrimSpace(active.SystemTenantID) == "" {
		return "", fmt.Errorf("--tenant-id is required (no SystemTenantID in active context; pass --all-tenants to reset every tenant)")
	}
	return strings.TrimSpace(active.SystemTenantID), nil
}

func runSkipperKnowledgeReset(ctx context.Context, cmd *cobra.Command, rc *resolvedCluster, pool *fwssh.Pool, opts skipperKnowledgeResetOptions) error {
	exec, conn, err := skipperSQLExecutor(ctx, rc, pool)
	if err != nil {
		return err
	}
	if !opts.DryRun {
		scope := "tenant " + opts.TenantID
		if opts.AllTenants {
			scope = "all tenants"
		}
		if opts.Source != "" {
			scope += " source " + opts.Source
		}
		if !promptConfirm(fmt.Sprintf("Reset Skipper knowledge and crawl cache for %s?", scope), opts.Yes) {
			return fmt.Errorf("cancelled")
		}
	}
	result, err := resetSkipperKnowledge(ctx, exec, conn, opts)
	if err != nil {
		return err
	}
	action := "reset"
	if opts.DryRun {
		action = "dry-run"
	}
	scope := opts.TenantID
	if opts.AllTenants {
		scope = "all"
	}
	if scope == "" {
		scope = "unknown"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "skipper knowledge %s: tenant=%s source=%s knowledge_rows=%d page_cache_rows=%d crawl_jobs=%d\n",
		action,
		scope,
		firstNonEmpty(strings.TrimSpace(opts.Source), "all"),
		result.KnowledgeRows,
		result.CacheRows,
		result.CrawlJobs)
	if !opts.DryRun {
		fmt.Fprintln(cmd.OutOrStdout(), "Next crawl cycle will fetch and embed sources from scratch.")
	}
	return nil
}

func resetSkipperKnowledge(ctx context.Context, exec provisioner.SQLExecutor, conn provisioner.ConnParams, opts skipperKnowledgeResetOptions) (skipperKnowledgeResetResult, error) {
	source := strings.TrimSpace(opts.Source)
	tenantID := strings.TrimSpace(opts.TenantID)
	if !opts.AllTenants && tenantID == "" {
		return skipperKnowledgeResetResult{}, fmt.Errorf("tenant id is required unless --all-tenants is set")
	}
	query := skipperKnowledgeResetQuery(opts.DryRun)
	var result skipperKnowledgeResetResult
	var scanned bool
	if err := exec.QueryRows(ctx, conn, query, []any{tenantID, source}, func(scan func(dest ...any) error) error {
		scanned = true
		return scan(&result.KnowledgeRows, &result.CacheRows, &result.CrawlJobs)
	}); err != nil {
		return skipperKnowledgeResetResult{}, err
	}
	if !scanned {
		return skipperKnowledgeResetResult{}, fmt.Errorf("skipper knowledge reset returned no result")
	}
	return result, nil
}

func skipperKnowledgeResetQuery(dryRun bool) string {
	if dryRun {
		return `
			SELECT
				(SELECT COUNT(*) FROM skipper.skipper_knowledge
				 WHERE ($1 = '' OR tenant_id::text = $1)
				   AND ($2 = '' OR source_url = $2 OR source_root = $2)) AS knowledge_rows,
				(SELECT COUNT(*) FROM skipper.skipper_page_cache
				 WHERE ($1 = '' OR tenant_id::text = $1)
				   AND ($2 = '' OR page_url = $2 OR source_root = $2)) AS page_cache_rows,
				(SELECT COUNT(*) FROM skipper.skipper_crawl_jobs
				 WHERE status = 'running'
				   AND ($1 = '' OR tenant_id::text = $1)
				   AND ($2 = '' OR sitemap_url = $2)) AS crawl_jobs`
	}
	return `
		WITH knowledge_deleted AS (
			DELETE FROM skipper.skipper_knowledge
			WHERE ($1 = '' OR tenant_id::text = $1)
			  AND ($2 = '' OR source_url = $2 OR source_root = $2)
			RETURNING 1
		),
		cache_deleted AS (
			DELETE FROM skipper.skipper_page_cache
			WHERE ($1 = '' OR tenant_id::text = $1)
			  AND ($2 = '' OR page_url = $2 OR source_root = $2)
			RETURNING 1
		),
		jobs_cancelled AS (
			UPDATE skipper.skipper_crawl_jobs
			SET status = 'cancelled',
			    error = 'cancelled before Skipper knowledge reset',
			    finished_at = NOW()
			WHERE status = 'running'
			  AND ($1 = '' OR tenant_id::text = $1)
			  AND ($2 = '' OR sitemap_url = $2)
			RETURNING 1
		)
		SELECT
			(SELECT COUNT(*) FROM knowledge_deleted) AS knowledge_rows,
			(SELECT COUNT(*) FROM cache_deleted) AS page_cache_rows,
			(SELECT COUNT(*) FROM jobs_cancelled) AS crawl_jobs`
}

func skipperSQLExecutor(ctx context.Context, rc *resolvedCluster, pool *fwssh.Pool) (provisioner.SQLExecutor, provisioner.ConnParams, error) {
	pg := rc.Manifest.Infrastructure.Postgres
	if pg == nil || !pg.Enabled {
		return nil, provisioner.ConnParams{}, fmt.Errorf("postgres/yugabyte is not enabled in manifest")
	}
	host, err := skipperDBHost(rc.Manifest, pg)
	if err != nil {
		return nil, provisioner.ConnParams{}, err
	}
	runner, err := pool.Get(&fwssh.ConnectionConfig{
		Address:  host.ExternalIP,
		Port:     22,
		User:     host.User,
		HostName: host.Name,
		Timeout:  30 * time.Second,
	})
	if err != nil {
		return nil, provisioner.ConnParams{}, err
	}
	conn := provisioner.ConnParams{
		Port:     pg.EffectivePort(),
		Database: "skipper",
	}
	if pg.IsYugabyte() {
		conn.User = "yugabyte"
		return &provisioner.SSHExecutor{
			Runner:     runner,
			BinaryPath: "/opt/yugabyte/bin/ysqlsh",
		}, conn, nil
	}
	conn.User = "postgres"
	return &provisioner.SSHExecutor{Runner: runner, UsePeerAuth: true}, conn, nil
}

func skipperDBHost(manifest *inventory.Manifest, pg *inventory.PostgresConfig) (inventory.Host, error) {
	if pg.IsYugabyte() && len(pg.Nodes) > 0 {
		host, ok := manifest.GetHost(pg.Nodes[0].Host)
		if !ok {
			return inventory.Host{}, fmt.Errorf("yugabyte node host %s not found", pg.Nodes[0].Host)
		}
		return host, nil
	}
	host, ok := manifest.GetHost(pg.Host)
	if !ok {
		return inventory.Host{}, fmt.Errorf("postgres host %s not found", pg.Host)
	}
	return host, nil
}
