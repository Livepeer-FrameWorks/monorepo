package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"frameworks/cli/pkg/bootstrap"
	"frameworks/cli/pkg/inventory"
	pkgdns "github.com/Livepeer-FrameWorks/monorepo/pkg/dns"

	"github.com/spf13/cobra"
)

// edgeNativeStorageDir is where a native edge keeps its media (Helmsman's
// HELMSMAN_STORAGE_LOCAL_PATH, the edge role's edge_storage_local_path).
const edgeNativeStorageDir = "/var/lib/frameworks/edge-storage"

// nativeEdgeUnits are the systemd units of a native edge bundle.
var nativeEdgeUnits = []string{"frameworks-helmsman.service", "frameworks-mistserver.service", "frameworks-caddy.service"}

// stackTargetDatabases maps the database names the stack scenarios query
// (lib.sh pg <name>) to the manifest service whose own role reads them.
var stackTargetDatabases = []struct{ name, service string }{
	{"commodore", "commodore"},
	{"purser", "purser"},
	{"quartermaster", "quartermaster"},
	{"network_observer", "network-observer"},
}

func newClusterStackTargetCmd() *cobra.Command {
	var outDir string
	cmd := &cobra.Command{
		Use:   "stack-target",
		Short: "Describe this cluster for the stack scenario runner",
		Long: `Write the files scripts/stack needs to run its scenarios against this
deployed cluster (STACK_TARGET=staging make verify-stack):

  target.env       public endpoints, cells, edges and database names
  service-map.txt  scenario-service|ssh-target|systemd-unit, one row per replica
  access-map.txt   how each database and ClickHouse is read over SSH
  operator.env     the platform operator's login (mode 0600)

Everything comes from the cluster manifest, its edge manifest, the bootstrap
overlay and their SOPS files. No secret is written outside operator.env, and
database credentials stay on the hosts.`,
		Example: `  frameworks cluster stack-target --gitops-dir ../gitops --cluster staging --out-dir .stack/staging/target`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(outDir) == "" {
				return fmt.Errorf("--out-dir is required")
			}
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			return runClusterStackTarget(cmd, rc, outDir)
		},
	}
	cmd.Flags().StringVar(&outDir, "out-dir", "", "directory to write the target files into (created mode 0700)")
	return cmd
}

func runClusterStackTarget(cmd *cobra.Command, rc *resolvedCluster, outDir string) error {
	manifestDir := filepath.Dir(rc.ManifestPath)
	var edges *inventory.EdgeManifest
	if path := defaultEdgeManifestPath(rc.ManifestPath); path != "" {
		loaded, err := inventory.LoadEdgeWithHosts(path, rc.AgeKey)
		if err != nil {
			return fmt.Errorf("load edge manifest: %w", err)
		}
		edges = loaded
	}
	manifestPath, err := filepath.Abs(rc.ManifestPath)
	if err != nil {
		return err
	}
	target, err := buildStackTarget(rc.Manifest, edges)
	if err != nil {
		return err
	}
	email, password, err := stackTargetOperator(rc.Manifest, manifestDir, rc.AgeKey)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(outDir, 0o700); err != nil {
		return err
	}
	absOut, err := filepath.Abs(outDir)
	if err != nil {
		return err
	}
	target.env = append(target.env,
		[2]string{"STACK_CLI_MANIFEST", manifestPath},
		[2]string{"STACK_CLI_CONTEXT", rc.ContextName},
		[2]string{"STAGING_SERVICE_MAP_FILE", filepath.Join(absOut, "service-map.txt")},
		[2]string{"STACK_ACCESS_MAP_FILE", filepath.Join(absOut, "access-map.txt")},
	)
	files := map[string]string{
		"target.env":      renderStackTargetEnv(target.env),
		"service-map.txt": strings.Join(target.services, "\n") + "\n",
		"access-map.txt":  strings.Join(target.access, "\n") + "\n",
		"operator.env":    renderStackTargetEnv([][2]string{{"STACK_OPERATOR_EMAIL", email}, {"STACK_OPERATOR_PASSWORD", password}}),
	}
	for name, content := range files {
		mode := os.FileMode(0o644)
		if name == "operator.env" {
			mode = 0o600
		}
		if err = os.WriteFile(filepath.Join(absOut, name), []byte(content), mode); err != nil {
			return err
		}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "stack target for %s written to %s\n", firstNonEmpty(rc.Cluster, rc.Manifest.Profile), absOut)
	return nil
}

type stackTarget struct {
	env      [][2]string
	services []string
	access   []string
}

// buildStackTarget derives the scenario runner's view of a deployed cluster.
// Cell A is the default media cluster and cell B the next media cluster by id;
// each cell's edge is the first edge-manifest node of that cluster.
func buildStackTarget(m *inventory.Manifest, edges *inventory.EdgeManifest) (*stackTarget, error) {
	if m == nil {
		return nil, fmt.Errorf("manifest is required")
	}
	root := strings.Trim(strings.TrimSpace(m.RootDomain), ".")
	if root == "" {
		return nil, fmt.Errorf("manifest has no root_domain")
	}
	cells := stackTargetMediaCells(m)
	if len(cells) < 2 {
		return nil, fmt.Errorf("the scenarios need two media clusters, manifest has %d", len(cells))
	}
	bridge, ok := pkgdns.ServiceFQDN("bridge", root)
	if !ok {
		return nil, fmt.Errorf("no public name for bridge")
	}
	t := &stackTarget{}
	add := func(k, v string) { t.env = append(t.env, [2]string{k, v}) }
	add("BRIDGE_URL", "https://"+bridge)
	add("BRIDGE_GRAPHQL_URL", "https://"+bridge+"/graphql")
	add("BRIDGE_WS_URL", "wss://"+bridge+"/graphql/ws")
	add("STACK_EDGE_STORAGE_DIR", edgeNativeStorageDir)

	edgeServices := []string{"edge", "edge-b"}
	for i, cell := range cells[:2] {
		letter := string(rune('A' + i))
		foghorn, ok := pkgdns.ServiceFQDN("foghorn", pkgdns.SanitizeLabel(cell)+"."+root)
		if !ok {
			return nil, fmt.Errorf("no public name for foghorn in %s", cell)
		}
		add("CELL_"+letter+"_CLUSTER", cell)
		add("FOGHORN_"+letter+"_CLUSTER", cell)
		// One public entry per cell: replicas sit behind it and are not
		// individually reachable from outside the cluster network.
		add("FOGHORN_"+letter+"_URL", "https://"+foghorn)
		add("FOGHORN_"+letter+"_URLS", "https://"+foghorn)
		add("FOGHORN_"+letter+"_DB", "foghorn_"+strings.ToLower(letter))

		node, ok := stackTargetCellEdge(edges, cell)
		if !ok {
			return nil, fmt.Errorf("edge manifest has no node in media cluster %s", cell)
		}
		domain := edgeManifestNodeDomain(edges.RootDomain, cell, node.Subdomain)
		if domain == "" {
			return nil, fmt.Errorf("edge node %s has no public domain", node.Name)
		}
		add("EDGE_"+letter+"_HOST", domain)
		add("EDGE_"+letter+"_RTMP", "rtmp://"+domain+":1935/live")
		add("STACK_EDGE_"+letter+"_SERVICE", edgeServices[i])
		if letter == "A" {
			add("STACK_EDGE_A_LOCATION_PREFIX", "https://"+domain+"/")
			// The scenarios' artifacts originate in cell A, whose Chandler serves their thumbnails.
			if chandler, ok := pkgdns.ServiceFQDN("chandler", pkgdns.SanitizeLabel(cell)+"."+root); ok {
				add("CHANDLER_URL", "https://"+chandler)
			}
		}
		user, addr, err := splitSSHTarget(node.SSH)
		if err != nil {
			return nil, fmt.Errorf("edge node %s: %w", node.Name, err)
		}
		for _, unit := range nativeEdgeUnits {
			t.services = append(t.services, fmt.Sprintf("%s|%s@%s|%s", edgeServices[i], user, firstNonEmpty(node.ExternalIP, addr), unit))
		}

		foghornService, host, err := stackTargetCellService(m, "foghorn", cell)
		if err != nil {
			return nil, err
		}
		t.access = append(t.access, stackTargetPGRow(m, "foghorn_"+strings.ToLower(letter), foghornService, host))
		// The service-map name covering every replica of the cell's Foghorn.
		add("STACK_FOGHORN_"+letter+"_SERVICES", foghornService)
	}

	services, err := stackTargetServiceRows(m)
	if err != nil {
		return nil, err
	}
	t.services = append(services, t.services...)

	for _, db := range stackTargetDatabases {
		cfg, ok := m.Services[db.service]
		if !ok || !cfg.Enabled {
			continue
		}
		hosts := serviceHostNames(cfg)
		if len(hosts) == 0 {
			continue
		}
		t.access = append(t.access, stackTargetPGRow(m, db.name, db.service, hosts[0]))
	}
	if cfg, ok := m.Services["periscope-query"]; ok && cfg.Enabled {
		if hosts := serviceHostNames(cfg); len(hosts) > 0 {
			ssh, err := stackTargetSSH(m, hosts[0])
			if err != nil {
				return nil, err
			}
			t.access = append(t.access, fmt.Sprintf("ch|clickhouse|%s|/etc/frameworks/periscope-query.env|%d", ssh, grafanaClickHouseHTTPPort))
		}
	}
	return t, nil
}

func stackTargetMediaCells(m *inventory.Manifest) []string {
	var cells []string
	for id, c := range m.Clusters {
		if c.Type != "edge" {
			continue
		}
		media := len(c.Roles) == 0
		for _, role := range c.Roles {
			media = media || role == "media"
		}
		if media {
			cells = append(cells, id)
		}
	}
	sort.SliceStable(cells, func(i, j int) bool {
		di, dj := m.Clusters[cells[i]].Default, m.Clusters[cells[j]].Default
		if di != dj {
			return di
		}
		return cells[i] < cells[j]
	})
	return cells
}

func stackTargetCellEdge(edges *inventory.EdgeManifest, cell string) (inventory.EdgeNode, bool) {
	if edges == nil {
		return inventory.EdgeNode{}, false
	}
	for _, node := range edges.Nodes {
		if node.ResolvedCluster(edges.ClusterID) == cell {
			return node, true
		}
	}
	return inventory.EdgeNode{}, false
}

// stackTargetCellService is the manifest service of a deploy type that serves
// one media cluster, and its first host.
func stackTargetCellService(m *inventory.Manifest, deploy, cell string) (string, string, error) {
	names := make([]string, 0, len(m.Services))
	for name := range m.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		cfg := m.Services[name]
		if !cfg.Enabled || (cfg.Cluster != cell && !slices.Contains(cfg.Clusters, cell)) {
			continue
		}
		got, err := resolveDeployName(name, cfg)
		if err != nil || got != deploy {
			continue
		}
		if hosts := serviceHostNames(cfg); len(hosts) > 0 {
			return name, hosts[0], nil
		}
	}
	return "", "", fmt.Errorf("no enabled %s service in media cluster %s", deploy, cell)
}

// stackTargetServiceRows lists every enabled service replica with its unit, so
// the runner reads the same journals the CLI's logs command reads.
func stackTargetServiceRows(m *inventory.Manifest) ([]string, error) {
	var rows []string
	for _, configs := range []map[string]inventory.ServiceConfig{m.Services, m.Interfaces} {
		names := make([]string, 0, len(configs))
		for name := range configs {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			cfg := configs[name]
			if !cfg.Enabled {
				continue
			}
			deploy, err := resolveDeployName(name, cfg)
			if err != nil {
				continue
			}
			for _, host := range serviceHostNames(cfg) {
				ssh, err := stackTargetSSH(m, host)
				if err != nil {
					return nil, err
				}
				rows = append(rows, fmt.Sprintf("%s|%s|frameworks-%s.service", name, ssh, deploy))
			}
		}
	}
	return rows, nil
}

// stackTargetPGRow: pg|<db>|<host holding the service's env>|<env file>|<host
// that runs psql>. The client is the native Postgres instance's host when the
// manifest has one (it carries the client tools), else the service host.
func stackTargetPGRow(m *inventory.Manifest, db, service, host string) string {
	deploy, err := resolveDeployName(service, m.Services[service])
	if err != nil {
		deploy = service
	}
	ssh, err := stackTargetSSH(m, host)
	if err != nil {
		ssh = host
	}
	client := ssh
	if pg := m.Infrastructure.Postgres; pg != nil && pg.Enabled && len(pg.Instances) > 0 {
		if c, err := stackTargetSSH(m, pg.Instances[0].Host); err == nil {
			client = c
		}
	}
	return fmt.Sprintf("pg|%s|%s|/etc/frameworks/%s.env|%s", db, ssh, deploy, client)
}

func stackTargetSSH(m *inventory.Manifest, hostName string) (string, error) {
	host, ok := m.GetHost(hostName)
	if !ok {
		return "", fmt.Errorf("unknown host %s", hostName)
	}
	addr := strings.TrimSpace(host.ExternalIP)
	if addr == "" {
		return "", fmt.Errorf("host %s has no external_ip", hostName)
	}
	if user := strings.TrimSpace(host.User); user != "" {
		return user + "@" + addr, nil
	}
	return addr, nil
}

// stackTargetOperator is the bootstrap overlay's platform operator: the login
// the runner uses to mint its run's API token.
func stackTargetOperator(m *inventory.Manifest, manifestDir, ageKey string) (string, string, error) {
	path := m.BootstrapOverlay
	if path != "" && !filepath.IsAbs(path) {
		path = filepath.Join(manifestDir, path)
	}
	overlay, err := bootstrap.LoadOverlay(path)
	if err != nil {
		return "", "", err
	}
	if overlay == nil {
		return "", "", fmt.Errorf("manifest has no bootstrap_overlay with a platform operator")
	}
	resolver := &bootstrap.DefaultResolver{BaseDir: manifestDir, AgeKeyFile: firstNonEmpty(ageKey, os.Getenv("SOPS_AGE_KEY_FILE"))}
	for _, account := range overlay.Accounts {
		if account.Kind != bootstrap.AccountSystemOperator {
			continue
		}
		for _, user := range account.Users {
			if !user.PlatformOperator || user.Email == "" {
				continue
			}
			password, err := resolver.Resolve(user.PasswordRef)
			if err != nil {
				return "", "", fmt.Errorf("resolve platform operator password: %w", err)
			}
			return user.Email, password, nil
		}
	}
	return "", "", fmt.Errorf("bootstrap overlay has no system_operator user with platform_operator")
}

func renderStackTargetEnv(pairs [][2]string) string {
	var b strings.Builder
	for _, kv := range pairs {
		fmt.Fprintf(&b, "%s='%s'\n", kv[0], strings.ReplaceAll(kv[1], "'", `'\''`))
	}
	return b.String()
}
