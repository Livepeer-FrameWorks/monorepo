package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"frameworks/cli/pkg/grafana"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"

	pkgdns "github.com/Livepeer-FrameWorks/monorepo/pkg/dns"
	gfdash "github.com/Livepeer-FrameWorks/monorepo/pkg/grafana"

	"github.com/spf13/cobra"
)

const grafanaClickHouseHTTPPort = 8123

func newClusterGrafanaCmd() *cobra.Command {
	var opts grafana.SyncOptions
	var filePath string

	cmd := &cobra.Command{
		Use:   "grafana",
		Short: "Manage cluster Grafana content",
	}
	syncCmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync repo-owned Grafana dashboards and their datasources",
		Long: `Sync the repo-owned Grafana dashboards (embedded in the CLI) into the
"FrameWorks" folder, ensuring the pinned datasources they reference:
uid "prometheus" (VictoriaMetrics query endpoint, derived from the manifest)
and uid "clickhouse" (readonly_user, password from the manifest env_files).

The Grafana URL is derived from the cluster manifest's root_domain and the
credentials come from GF_SECURITY_ADMIN_USER / GF_SECURITY_ADMIN_PASSWORD in
the manifest env_files; flags and environment variables override both.
Operator-made content outside those pinned folder, datasource, and dashboard
uids is never touched.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.BaseURL = firstNonEmpty(opts.BaseURL, os.Getenv("GRAFANA_URL"))
			opts.APIKey = firstNonEmpty(opts.APIKey, os.Getenv("GRAFANA_API_KEY"))
			if err := fillGrafanaAccessFromManifest(cmd, &opts); err != nil {
				return err
			}
			if filePath != "" {
				content, err := os.ReadFile(filePath)
				if err != nil {
					return err
				}
				opts.Dashboards = []grafana.DashboardSource{{Name: filePath, Content: content}}
			} else {
				dashboards, err := embeddedGrafanaDashboards()
				if err != nil {
					return err
				}
				opts.Dashboards = dashboards
			}
			opts.Out = cmd.OutOrStdout()
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			summary, err := grafana.Sync(ctx, opts)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Grafana sync complete: %d folders created, %d datasources created, %d updated, %d dashboards synced, %d skipped\n",
				summary.FoldersCreated, summary.DatasourcesCreated, summary.DatasourcesUpdated, summary.DashboardsSynced, summary.Skipped)
			return nil
		},
	}
	syncCmd.Flags().StringVar(&opts.BaseURL, "url", "", "Grafana base URL (default: $GRAFANA_URL, else derived from the manifest root_domain)")
	syncCmd.Flags().StringVar(&opts.APIKey, "api-key", "", "Grafana service-account token (default: $GRAFANA_API_KEY, else admin basic auth from the manifest env_files)")
	syncCmd.Flags().StringVar(&filePath, "file", "", "sync a single dashboard JSON file instead of the embedded dashboards")
	syncCmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "show changes without writing to Grafana")

	cmd.AddCommand(syncCmd)
	return cmd
}

func embeddedGrafanaDashboards() ([]grafana.DashboardSource, error) {
	entries, err := gfdash.Content.ReadDir("dashboards")
	if err != nil {
		return nil, fmt.Errorf("read embedded grafana dashboards: %w", err)
	}
	var sources []grafana.DashboardSource
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		content, err := gfdash.Content.ReadFile("dashboards/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read embedded grafana dashboard %s: %w", entry.Name(), err)
		}
		sources = append(sources, grafana.DashboardSource{Name: entry.Name(), Content: content})
	}
	if len(sources) == 0 {
		return nil, errors.New("no embedded grafana dashboards found")
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Name < sources[j].Name })
	return sources, nil
}

// fillGrafanaAccessFromManifest derives the Grafana URL, admin credentials,
// and the managed datasources' connection details from the cluster manifest
// and its env_files, for whatever the flags/env did not already provide.
func fillGrafanaAccessFromManifest(cmd *cobra.Command, opts *grafana.SyncOptions) error {
	rc, err := resolveClusterManifest(cmd)
	if err != nil {
		return fmt.Errorf("resolve cluster manifest for Grafana access (pass --url and --api-key to skip): %w", err)
	}
	defer rc.Cleanup()
	manifest := rc.Manifest

	if opts.BaseURL == "" {
		fqdn, ok := pkgdns.ServiceFQDN("grafana", manifest.RootDomain)
		if !ok || fqdn == "" {
			return errors.New("manifest has no root_domain to derive the Grafana URL from; pass --url or set $GRAFANA_URL")
		}
		opts.BaseURL = "https://" + fqdn
	}

	env, err := rc.SharedEnv()
	if err != nil {
		return fmt.Errorf("load manifest env_files: %w", err)
	}
	if opts.APIKey == "" {
		opts.Username = env["GF_SECURITY_ADMIN_USER"]
		opts.Password = env["GF_SECURITY_ADMIN_PASSWORD"]
		if opts.Username == "" || opts.Password == "" {
			return errors.New("no Grafana credentials: set GF_SECURITY_ADMIN_USER / GF_SECURITY_ADMIN_PASSWORD in the manifest env_files, or pass --api-key / $GRAFANA_API_KEY")
		}
	}

	// Datasource addresses use <host>.internal mesh hostnames — the same
	// names every other cross-service connection uses, and verified to
	// resolve from inside the grafana container.
	opts.VictoriaMetricsURL = grafanaVictoriaMetricsURL(manifest)
	if opts.VictoriaMetricsURL == "" {
		return errors.New("manifest defines no victoriametrics observability host; the metrics datasource cannot be derived")
	}

	ch := manifest.Infrastructure.ClickHouse
	if ch == nil || !ch.Enabled {
		return errors.New("manifest defines no ClickHouse; the clickhouse datasource cannot be derived")
	}
	chHost := manifestMeshHostname(manifest, ch.CoordinatorHost())
	if chHost == "" {
		return fmt.Errorf("ClickHouse host %q not found in manifest hosts", ch.CoordinatorHost())
	}
	// The dedicated frameworks_analytics user is the one analytics read
	// identity (provisioned via users.d, reachable cross-host). The role's
	// rendered `readonly` user is localhost-only and the repo users.xml
	// `readonly_user` exists only in the dev docker image.
	analyticsPassword := env["CLICKHOUSE_ANALYTICS_PASSWORD"]
	if analyticsPassword == "" {
		return errors.New("CLICKHOUSE_ANALYTICS_PASSWORD missing from manifest env_files — the clickhouse datasource authenticates as frameworks_analytics; set it with `scripts/sops-env.sh set secrets/<cluster>.env CLICKHOUSE_ANALYTICS_PASSWORD <value>` in your gitops repo")
	}
	opts.ClickHouse = &grafana.ClickHouseDatasource{
		Server:   chHost,
		Port:     grafanaClickHouseHTTPPort,
		Username: "frameworks_analytics",
		Password: analyticsPassword,
	}
	return nil
}

// grafanaVictoriaMetricsURL returns the VictoriaMetrics base URL for the
// grafana datasource, addressed by mesh hostname. The VM datasource plugin
// appends its own API paths, so this is the bare http://<host>:<port>.
func grafanaVictoriaMetricsURL(manifest *inventory.Manifest) string {
	if manifest == nil {
		return ""
	}
	obs, ok := manifest.Observability["victoriametrics"]
	if !ok || !obs.Enabled {
		return ""
	}
	hostName := obs.Host
	if hostName == "" && len(obs.Hosts) > 0 {
		hostName = obs.Hosts[0]
	}
	meshHost := manifestMeshHostname(manifest, hostName)
	if meshHost == "" {
		return ""
	}
	port, err := resolvePort("victoriametrics", obs)
	if err != nil || port == 0 {
		port = provisioner.ServicePorts["victoriametrics"]
	}
	if port == 0 {
		return ""
	}
	return fmt.Sprintf("http://%s:%d", meshHost, port)
}
