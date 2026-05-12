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
	cmd.AddCommand(newClusterSkipperPostsCmd())
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
