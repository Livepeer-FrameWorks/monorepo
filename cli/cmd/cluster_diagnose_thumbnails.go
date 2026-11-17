package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/pkg/ssh"
	"github.com/spf13/cobra"
)

func newClusterDiagnoseThumbnailsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "thumbnails",
		Short: "Inspect thumbnail recovery query plans and database waits in every cell",
		Long:  "Run read-only, five-second-bounded SQL probes against each Foghorn database. Reports recovery indexes, database waits and the actual publishing, expiry and superseded-version query plans. No media objects or tenant rows are returned.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()
			targets, err := postgresSnapshotTargets(rc)
			if err != nil {
				return err
			}
			key := stringFlag(cmd, "ssh-key").Value
			pool := ssh.NewPool(30*time.Second, key)
			defer pool.Close()
			report := mediaAuthorityDiagnosticReport{Probes: []mediaAuthorityDiagnosticProbe{}}
			failures := 0
			for _, target := range targets {
				probes := thumbnailRecoveryDatabaseProbes(target.Databases)
				if len(probes) == 0 {
					continue
				}
				probe := mediaAuthorityDiagnosticProbe{Kind: "database", Name: target.Name, Host: target.HostName, ExitCode: -1}
				runner, runErr := snapshotRunner(pool, key, target.Host, 90*time.Second)
				if runErr == nil {
					result, err := runner.RunScript(ctx, mediaAuthorityDatabaseScript(target, probes))
					runErr = err
					if result != nil {
						probe.Stdout, probe.Stderr, probe.ExitCode = strings.TrimSpace(result.Stdout), strings.TrimSpace(result.Stderr), result.ExitCode
					}
				}
				if runErr != nil {
					probe.Error = "thumbnail database probe failed: " + runErr.Error()
				} else if probe.ExitCode != 0 {
					probe.Error = fmt.Sprintf("thumbnail database probe exited %d", probe.ExitCode)
				}
				if probe.Error != "" {
					failures++
				}
				report.Probes = append(report.Probes, probe)
			}
			if len(report.Probes) == 0 {
				return fmt.Errorf("no Foghorn databases found in manifest")
			}
			if fwcfg.GetRuntimeOverrides().OutputJSON {
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(report); err != nil {
					return err
				}
			} else {
				for _, probe := range report.Probes {
					fmt.Fprintf(cmd.OutOrStdout(), "\n%s on %s\n%s\n", probe.Name, probe.Host, probe.Stdout)
					if probe.Stderr != "" {
						fmt.Fprintln(cmd.ErrOrStderr(), probe.Stderr)
					}
					if probe.Error != "" {
						fmt.Fprintln(cmd.ErrOrStderr(), probe.Error)
					}
				}
			}
			if failures > 0 {
				return fmt.Errorf("thumbnail recovery diagnostics failed on %d target(s)", failures)
			}
			return nil
		},
	}
}

func thumbnailRecoveryDatabaseProbes(databases []string) []mediaAuthorityDatabaseProbe {
	var probes []mediaAuthorityDatabaseProbe
	for _, database := range databases {
		if database != "foghorn" && !strings.HasPrefix(database, "foghorn_") {
			continue
		}
		for _, sql := range []string{thumbnailRecoveryMetadataSQL, thumbnailPublishingPlanSQL, thumbnailExpiryPlanSQL, thumbnailSupersededPlanSQL} {
			probes = append(probes, mediaAuthorityDatabaseProbe{Database: database, SQL: "SET default_transaction_read_only = on;\nSET statement_timeout = '5s';\n\\timing on\n" + sql})
		}
	}
	return probes
}

const thumbnailRecoveryMetadataSQL = `\echo == thumbnail recovery indexes and database waits ==
SELECT current_database() AS database, NOW() AS observed_at;
SELECT indexname, indexdef FROM pg_indexes
WHERE schemaname = 'foghorn' AND tablename = 'thumbnail_task_assignment' ORDER BY indexname;
SELECT usename, state, wait_event_type, wait_event, count(*) AS sessions,
       max(NOW() - query_start) AS longest_running
FROM pg_stat_activity WHERE datname = current_database()
GROUP BY 1, 2, 3, 4 ORDER BY sessions DESC LIMIT 20;
`

const thumbnailPublishingPlanSQL = `\echo == publishing recovery plan ==
EXPLAIN (ANALYZE, COSTS OFF)
SELECT attempt_id, COALESCE(publish_lease_token, '') AS token
FROM foghorn.thumbnail_task_assignment
WHERE status = 'publishing' AND expiry > NOW() LIMIT 18;
`

const thumbnailExpiryPlanSQL = `\echo == expiry recovery plan ==
EXPLAIN (ANALYZE, COSTS OFF)
SELECT attempt_id FROM foghorn.thumbnail_task_assignment
WHERE status IN ('assigned', 'uploading', 'verifying', 'publishing') AND expiry < NOW() LIMIT 18;
`

const thumbnailSupersededPlanSQL = `\echo == superseded-version recovery plan ==
EXPLAIN (ANALYZE, COSTS OFF)
SELECT a.attempt_id FROM foghorn.thumbnail_task_assignment a
JOIN foghorn.thumbnail_active_pointer p ON p.asset_key = a.asset_key
WHERE a.status = 'published' AND a.version <> p.active_version
AND a.superseded_at IS NOT NULL AND a.superseded_at < NOW() - INTERVAL '5 minutes'
ORDER BY a.superseded_at LIMIT 18;
`
