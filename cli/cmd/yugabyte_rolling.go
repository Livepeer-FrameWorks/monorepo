package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/provisioner"
	fwssh "frameworks/cli/pkg/ssh"
)

const (
	yugabyteRollRecoveryTimeout = 15 * time.Minute
	yugabyteRollPollInterval    = 10 * time.Second
)

// yugabyteMasterAPIScript fetches one JSON endpoint from the local master's web server, whose address comes from the
// master flagfile. Follower masters answer by proxying the leader.
const yugabyteMasterAPIScript = `set -eu
conf=/opt/yugabyte/conf/master.conf
host="$(sed -n 's/^--webserver_interface=//p' "$conf" | head -n 1)"
port="$(sed -n 's/^--webserver_port=//p' "$conf" | head -n 1)"
curl -fsS --max-time 10 "http://${host}:${port}%s"
`

// yugabyteAdminScript runs yb-admin against every master named in the local master flagfile.
const yugabyteAdminScript = provisioner.YugabyteBinaryResolverShell + `
set -eu
conf=/opt/yugabyte/conf/master.conf
masters="$(sed -n 's/^--master_addresses=//p' "$conf" | head -n 1)"
admin="$(fw_yb_bin yb-admin)"
"$admin" --master_addresses "$masters" %s
`

// yugabyteCatalogMigrationsScript lists the YSQL catalog migrations of the installed engine.
const yugabyteCatalogMigrationsScript = provisioner.YugabyteBinaryResolverShell + `
set -eu
admin="$(fw_yb_bin yb-admin)"
ls -1 "$(dirname "$(dirname "$admin")")/share/ysql_migrations"
`

// yugabyteIdentityScript prints every name and address a node knows itself by.
const yugabyteIdentityScript = "hostname 2>/dev/null || true\nhostname -f 2>/dev/null || true\nhostname -I 2>/dev/null || true\nhostname -i 2>/dev/null || true\n"

// yugabyteNodeStateScript reports whether a node was ever bootstrapped by this role and whether its units run now.
// The role writes the bootstrap sentinel last, once a node has joined the universe, so a half-bootstrapped node
// reports configured or data rather than bootstrapped.
const yugabyteNodeStateScript = `set -u
state=fresh
if [ -f /var/lib/yugabyte/data/.frameworks-bootstrap-complete ]; then
  state=bootstrapped
elif [ -f /opt/yugabyte/conf/master.conf ] || [ -f /opt/yugabyte/conf/tserver.conf ]; then
  state=configured
elif [ -d /var/lib/yugabyte/data/yb-data ] && [ -n "$(ls -A /var/lib/yugabyte/data/yb-data 2>/dev/null)" ]; then
  state=data
fi
running=no
for unit in yb-master yb-tserver; do
  if systemctl is-active --quiet "$unit" 2>/dev/null; then running=yes; fi
done
# A master or tserver started outside systemd still runs; only no process at all counts as stopped.
if command -v pgrep >/dev/null 2>&1; then
  if pgrep -x yb-master >/dev/null 2>&1 || pgrep -x yb-tserver >/dev/null 2>&1; then running=yes; fi
else
  running=unknown
fi
echo "state=$state running=$running"
`

// yugabyteNodeState is what one node reports before provisioning decides how to reconcile the universe.
type yugabyteNodeState struct {
	Host    string
	Serving bool
	// State is fresh, bootstrapped, configured, or data; empty when the node could not be read.
	State   string
	Running bool
	Err     error
}

// yugabyteRollMode decides whether Yugabyte nodes must be reconciled one at a time. Parallel provisioning restarts
// every replica at once, so it requires readable state, no running processes, and no master leader. Missing bootstrap
// markers alone do not prove that an interrupted deployment has no live quorum.
func yugabyteRollMode(nodes []yugabyteNodeState, masterQuorum bool) (rolling bool, reason string) {
	anyBootstrapped := false
	for _, node := range nodes {
		switch {
		case node.Err != nil:
			return true, fmt.Sprintf("the state of %s could not be read (%v)", node.Host, node.Err)
		case node.Serving:
			return true, fmt.Sprintf("%s serves YSQL", node.Host)
		}
		if node.State == "bootstrapped" {
			anyBootstrapped = true
		}
	}
	var running []string
	for _, node := range nodes {
		if node.Running {
			running = append(running, node.Host)
		}
	}
	switch {
	case len(running) > 0:
		return true, fmt.Sprintf("yb-master or yb-tserver still runs on %s, so nodes change one at a time", strings.Join(running, ", "))
	case masterQuorum:
		return true, "the masters have a leader, so nodes are repaired one at a time"
	case !anyBootstrapped:
		return false, "no node serves YSQL and none finished bootstrapping, so the universe is being bootstrapped"
	default:
		return false, "no node serves YSQL, no master leader is reachable, and no yb-master or yb-tserver runs on any node, so quorum is already lost and serial repair cannot restore it"
	}
}

func parseYugabyteNodeState(host, out string) yugabyteNodeState {
	node := yugabyteNodeState{Host: host}
	fields := map[string]string{}
	for _, field := range strings.Fields(out) {
		key, value, ok := strings.Cut(field, "=")
		if ok {
			fields[key] = value
		}
	}
	switch fields["state"] {
	case "fresh", "bootstrapped", "configured", "data":
		node.State = fields["state"]
	default:
		node.Err = fmt.Errorf("unexpected node state report %q", strings.TrimSpace(out))
		return node
	}
	switch fields["running"] {
	case "yes":
		node.Running = true
	case "no":
	case "unknown":
		node.Err = fmt.Errorf("cannot tell whether yb-master or yb-tserver runs on %s (pgrep is missing)", host)
	default:
		node.Err = fmt.Errorf("unexpected node state report %q", strings.TrimSpace(out))
	}
	return node
}

// yugabyteHealth is what the masters report about the universe: the /api/v1/health-check fields and the leaderless
// tablets from /api/v1/tablet-replication.
type yugabyteHealth struct {
	// DeadNodes holds the permanent uuids of tservers the masters consider dead.
	DeadNodes              []string
	UnderReplicatedTablets []string
	LeaderlessTablets      []string
}

// parseYugabyteHealthCheck reads /api/v1/health-check. Every field the gate relies on must be present, so a partial
// or error response fails closed.
func parseYugabyteHealthCheck(out string) (yugabyteHealth, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &raw); err != nil {
		return yugabyteHealth{}, fmt.Errorf("parse master health check %q: %w", strings.TrimSpace(out), err)
	}
	if msg, ok := raw["error"]; ok {
		return yugabyteHealth{}, fmt.Errorf("master health check reported an error: %s", msg)
	}
	var health yugabyteHealth
	for key, target := range map[string]*[]string{"dead_nodes": &health.DeadNodes, "under_replicated_tablets": &health.UnderReplicatedTablets} {
		value, ok := raw[key]
		if !ok {
			return yugabyteHealth{}, fmt.Errorf("master health check has no %s field", key)
		}
		if err := json.Unmarshal(value, target); err != nil {
			return yugabyteHealth{}, fmt.Errorf("parse master health check %s: %w", key, err)
		}
	}
	if _, ok := raw["most_recent_uptime"]; !ok {
		return yugabyteHealth{}, errors.New("master health check has no most_recent_uptime field")
	}
	return health, nil
}

// parseYugabyteLeaderlessTablets reads /api/v1/tablet-replication.
func parseYugabyteLeaderlessTablets(out string) ([]string, error) {
	var raw struct {
		Error      *string `json:"error"`
		Leaderless *[]struct {
			TabletUUID string `json:"tablet_uuid"`
		} `json:"leaderless_tablets"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &raw); err != nil {
		return nil, fmt.Errorf("parse tablet replication report %q: %w", strings.TrimSpace(out), err)
	}
	if raw.Error != nil {
		return nil, fmt.Errorf("tablet replication report returned an error: %s", *raw.Error)
	}
	if raw.Leaderless == nil {
		return nil, errors.New("tablet replication report has no leaderless_tablets field")
	}
	tablets := make([]string, 0, len(*raw.Leaderless))
	for _, tablet := range *raw.Leaderless {
		tablets = append(tablets, tablet.TabletUUID)
	}
	return tablets, nil
}

func (h yugabyteHealth) problems() []string {
	var problems []string
	if len(h.UnderReplicatedTablets) > 0 {
		problems = append(problems, fmt.Sprintf("%d under-replicated tablet(s)", len(h.UnderReplicatedTablets)))
	}
	if len(h.LeaderlessTablets) > 0 {
		problems = append(problems, fmt.Sprintf("%d leaderless tablet(s)", len(h.LeaderlessTablets)))
	}
	return problems
}

// yugabyteMaster is one row of yb-admin list_all_masters.
type yugabyteMaster struct {
	UUID  string
	Host  string
	State string
	Role  string
}

// parseYugabyteMasters reads yb-admin list_all_masters: a header line, then one row per master with its uuid, RPC
// host:port, state (ALIVE or an error code), and role.
func parseYugabyteMasters(out string) ([]yugabyteMaster, error) {
	var masters []yugabyteMaster
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[0] == "Master" {
			continue
		}
		host := fields[1]
		if h, _, ok := strings.Cut(host, ":"); ok {
			host = h
		}
		masters = append(masters, yugabyteMaster{UUID: fields[0], Host: host, State: fields[2], Role: fields[3]})
	}
	if len(masters) == 0 {
		return nil, fmt.Errorf("yb-admin list_all_masters listed no masters: %q", strings.TrimSpace(out))
	}
	return masters, nil
}

// yugabyteMasterProblems reports why the masters could not lose a member now: every master must be alive and exactly
// one must lead.
func yugabyteMasterProblems(masters []yugabyteMaster) []string {
	var problems []string
	leaders := 0
	for _, master := range masters {
		if master.State != "ALIVE" {
			problems = append(problems, fmt.Sprintf("master %s on %s is %s", master.UUID, master.Host, master.State))
		}
		if master.Role == "LEADER" {
			leaders++
		}
	}
	if leaders != 1 {
		problems = append(problems, fmt.Sprintf("%d master leader(s)", leaders))
	}
	return problems
}

// yugabyteServerUUIDs finds a node's tserver uuid in the master's tablet server registry and its master uuid in
// yb-admin list_all_masters, both by the addresses and names the node reports. A node that runs no master has an
// empty master uuid.
func yugabyteServerUUIDs(registry string, masters []yugabyteMaster, identities map[string]bool) (tserver, master string, alive bool, err error) {
	var placements map[string]map[string]struct {
		PermanentUUID string `json:"permanent_uuid"`
		Status        string `json:"status"`
	}
	if err = json.Unmarshal([]byte(strings.TrimSpace(registry)), &placements); err != nil {
		return "", "", false, fmt.Errorf("parse tablet server registry: %w", err)
	}
	var matches []string
	for _, servers := range placements {
		for address, server := range servers {
			host := address
			if h, _, ok := strings.Cut(address, ":"); ok {
				host = h
			}
			if identities[host] && server.PermanentUUID != "" {
				matches = append(matches, server.PermanentUUID)
				alive = server.Status == "ALIVE"
			}
		}
	}
	if len(matches) > 1 {
		return "", "", false, fmt.Errorf("expected at most one tablet server for this node, found %d", len(matches))
	}
	for _, candidate := range masters {
		if identities[candidate.Host] {
			if master != "" {
				return "", "", false, errors.New("more than one master matches this node")
			}
			master = candidate.UUID
		}
	}
	if len(matches) == 0 {
		// A node that never joined has no tablet server in the registry.
		return "", master, false, nil
	}
	return matches[0], master, alive, nil
}

// yugabyteUniverse is how the roll observes and questions a running universe.
type yugabyteUniverse interface {
	// ServesYSQL reports whether host answers a local YSQL query.
	ServesYSQL(ctx context.Context, host inventory.Host) bool
	// Health reads the masters' view of tablet health through observer.
	Health(ctx context.Context, observer inventory.Host) (yugabyteHealth, error)
	// Masters lists the masters and their state through observer.
	Masters(ctx context.Context, observer inventory.Host) ([]yugabyteMaster, error)
	// Servers resolves target's tserver and master uuids through observer, and whether its tserver is alive.
	Servers(ctx context.Context, observer, target inventory.Host) (tserver, master string, alive bool, err error)
	// SafeToTakeDown asks the masters, through observer, whether every tablet and the master quorum survive losing
	// the given servers.
	SafeToTakeDown(ctx context.Context, observer inventory.Host, uuids []string) error
	// FinalizeUpgrade promotes the AutoFlags and upgrades the YSQL catalog of the running binaries, through observer.
	FinalizeUpgrade(ctx context.Context, observer inventory.Host) error
	// CatalogMigrations lists the YSQL catalog migration files the installed engine carries on host.
	CatalogMigrations(ctx context.Context, host inventory.Host) ([]string, error)
	// ProcessState checks the installed binary and, for tservers, the applied configuration receipt.
	ProcessState(ctx context.Context, host inventory.Host, process string) (yugabyteProcessState, error)
}

// yugabyteProcessState reports whether a process runs its installed binary and, for tservers, desired configuration.
type yugabyteProcessState string

const (
	yugabyteProcessCurrent yugabyteProcessState = "current"
	// yugabyteProcessStale needs its installed binary or desired configuration applied.
	yugabyteProcessStale   yugabyteProcessState = "stale"
	yugabyteProcessStopped yugabyteProcessState = "stopped"
)

// yugabyteProcessStateScript compares the inode of a running process's executable with the installed binary. An
// install selects a separate artifact tree, so a process started before it still maps the old inode. The tserver receipt records
// configuration only after a start/restart, making deferred config changes detectable even when the binary is unchanged.
const yugabyteProcessStateScript = provisioner.YugabyteBinaryResolverShell + `
set -u
process=%s
pid="$(systemctl show -p MainPID --value "$process" 2>/dev/null || true)"
if [ -z "$pid" ] || [ "$pid" = 0 ]; then echo "state=stopped"; exit 0; fi
running="$(stat -L -c %%i "/proc/$pid/exe" 2>/dev/null)" || { echo "state=unknown"; exit 0; }
binary="$(fw_yb_bin "$process")" || { echo "state=unknown"; exit 0; }
installed="$(stat -L -c %%i "$binary" 2>/dev/null)" || { echo "state=unknown"; exit 0; }
if [ "$running" != "$installed" ]; then echo "state=stale"; exit 0; fi
if [ "$process" = yb-tserver ]; then
  conf="$(sha256sum /opt/yugabyte/conf/tserver.conf)" || { echo "state=unknown"; exit 0; }
  unit="$(sha256sum /etc/systemd/system/yb-tserver.service)" || { echo "state=unknown"; exit 0; }
  applied="$(cat /var/lib/yugabyte/data/.frameworks-tserver-config-applied 2>/dev/null)" || applied=
  if [ "$applied" != "$conf
$unit" ]; then echo "state=stale"; exit 0; fi
fi
echo "state=current"
`

// sshYugabyteUniverse answers through SSH on the Yugabyte nodes.
type sshYugabyteUniverse struct {
	pool *fwssh.Pool
	pg   *inventory.PostgresConfig
}

func (u *sshYugabyteUniverse) ServesYSQL(ctx context.Context, host inventory.Host) bool {
	return checkYugabyteLocalYSQL(ctx, u.pool, host, u.pg).OK
}

func (u *sshYugabyteUniverse) Health(ctx context.Context, observer inventory.Host) (yugabyteHealth, error) {
	out, err := yugabyteNodeScript(ctx, u.pool, observer, fmt.Sprintf(yugabyteMasterAPIScript, "/api/v1/health-check"))
	if err != nil {
		return yugabyteHealth{}, fmt.Errorf("master health check: %w", err)
	}
	health, err := parseYugabyteHealthCheck(out)
	if err != nil {
		return yugabyteHealth{}, err
	}
	out, err = yugabyteNodeScript(ctx, u.pool, observer, fmt.Sprintf(yugabyteMasterAPIScript, "/api/v1/tablet-replication"))
	if err != nil {
		return yugabyteHealth{}, fmt.Errorf("tablet replication report: %w", err)
	}
	if health.LeaderlessTablets, err = parseYugabyteLeaderlessTablets(out); err != nil {
		return yugabyteHealth{}, err
	}
	return health, nil
}

func (u *sshYugabyteUniverse) Masters(ctx context.Context, observer inventory.Host) ([]yugabyteMaster, error) {
	out, err := yugabyteNodeScript(ctx, u.pool, observer, fmt.Sprintf(yugabyteAdminScript, "list_all_masters"))
	if err != nil {
		return nil, fmt.Errorf("list masters: %w", err)
	}
	return parseYugabyteMasters(out)
}

func (u *sshYugabyteUniverse) Servers(ctx context.Context, observer, target inventory.Host) (string, string, bool, error) {
	identities, err := yugabyteNodeScript(ctx, u.pool, target, yugabyteIdentityScript)
	if err != nil {
		return "", "", false, fmt.Errorf("read identities of %s: %w", target.Name, err)
	}
	known := map[string]bool{target.ExternalIP: true, target.Name: true}
	for _, field := range strings.Fields(identities) {
		known[field] = true
	}
	registry, err := yugabyteNodeScript(ctx, u.pool, observer, fmt.Sprintf(yugabyteMasterAPIScript, "/api/v1/tablet-servers"))
	if err != nil {
		return "", "", false, fmt.Errorf("read tablet server registry: %w", err)
	}
	masters, err := u.Masters(ctx, observer)
	if err != nil {
		return "", "", false, err
	}
	return yugabyteServerUUIDs(registry, masters, known)
}

func (u *sshYugabyteUniverse) SafeToTakeDown(ctx context.Context, observer inventory.Host, uuids []string) error {
	_, err := yugabyteNodeScript(ctx, u.pool, observer, fmt.Sprintf(yugabyteAdminScript, "are_nodes_safe_to_take_down "+fwssh.ShellQuote(strings.Join(uuids, ","))))
	return err
}

// yugabyteFinalizeTimeout bounds finalize_upgrade, which rewrites the YSQL catalog of every database.
const yugabyteFinalizeTimeout = 10 * time.Minute

func (u *sshYugabyteUniverse) FinalizeUpgrade(ctx context.Context, observer inventory.Host) error {
	command := fmt.Sprintf("--timeout_ms %d finalize_upgrade", yugabyteFinalizeTimeout.Milliseconds())
	out, err := yugabyteNodeScriptWithin(ctx, u.pool, observer, fmt.Sprintf(yugabyteAdminScript, command), yugabyteFinalizeTimeout+time.Minute)
	if err != nil {
		return fmt.Errorf("finalize upgrade: %w", err)
	}
	if !strings.Contains(out, "Upgrade successfully finalized") {
		return fmt.Errorf("finalize upgrade on %s did not report success: %s", observer.Name, strings.TrimSpace(out))
	}
	return nil
}

func (u *sshYugabyteUniverse) CatalogMigrations(ctx context.Context, host inventory.Host) ([]string, error) {
	out, err := yugabyteNodeScript(ctx, u.pool, host, yugabyteCatalogMigrationsScript)
	if err != nil {
		return nil, fmt.Errorf("list YSQL catalog migrations on %s: %w", host.Name, err)
	}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		if name := strings.TrimSpace(line); name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("the engine installed on %s carries no YSQL catalog migrations", host.Name)
	}
	return names, nil
}

func (u *sshYugabyteUniverse) ProcessState(ctx context.Context, host inventory.Host, process string) (yugabyteProcessState, error) {
	out, err := yugabyteNodeScript(ctx, u.pool, host, fmt.Sprintf(yugabyteProcessStateScript, fwssh.ShellQuote(process)))
	if err != nil {
		return "", fmt.Errorf("read %s state on %s: %w", process, host.Name, err)
	}
	switch state := yugabyteProcessState(strings.TrimPrefix(strings.TrimSpace(out), "state=")); state {
	case yugabyteProcessCurrent, yugabyteProcessStale, yugabyteProcessStopped:
		return state, nil
	}
	return "", fmt.Errorf("cannot tell whether %s on %s runs the installed binary (%q)", process, host.Name, strings.TrimSpace(out))
}

func yugabyteNodeScript(ctx context.Context, pool *fwssh.Pool, host inventory.Host, script string) (string, error) {
	return yugabyteNodeScriptWithin(ctx, pool, host, script, time.Minute)
}

func yugabyteNodeScriptWithin(ctx context.Context, pool *fwssh.Pool, host inventory.Host, script string, timeout time.Duration) (string, error) {
	runner, err := pool.Get(&fwssh.ConnectionConfig{Address: host.ExternalIP, Port: 22, User: host.User, HostName: host.Name, Timeout: 30 * time.Second})
	if err != nil {
		return "", fmt.Errorf("ssh connect %s: %w", host.Name, err)
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := runner.RunScript(probeCtx, script)
	if err != nil {
		return "", fmt.Errorf("%s: %w", host.Name, err)
	}
	if result.ExitCode != 0 {
		return result.Stdout, fmt.Errorf("%s: exit %d: %s", host.Name, result.ExitCode, strings.TrimSpace(result.Stderr+" "+result.Stdout))
	}
	return result.Stdout, nil
}

// yugabyteRoll serialises changes to the nodes of a running Yugabyte universe. A flagfile, binary, or OS change
// restarts a node's master and tserver, so changing several nodes at once can take every replica of a tablet, or the
// master quorum, down together. On a running universe each node changes alone and in a fixed order (nodes that are
// down first, because changing them is how they are repaired), each serving node only after the masters confirm the
// universe survives losing it, and the next node only once the changed node is back. The first failure stops the
// roll, so a change that breaks one node never reaches another.
type yugabyteRoll struct {
	enabled bool
	hosts   []inventory.Host
	out     io.Writer
	u       yugabyteUniverse
	// serving records which hosts served YSQL when the roll was planned; it orders repairs first.
	serving  map[string]bool
	timeout  time.Duration
	interval time.Duration

	mu      sync.Mutex
	cond    *sync.Cond
	order   []string
	next    int
	busy    bool
	failure error
}

func (y *yugabyteRoll) init() {
	if y.cond == nil {
		y.cond = sync.NewCond(&y.mu)
	}
	if y.serving == nil {
		y.serving = map[string]bool{}
	}
}

// newYugabyteGate returns an enabled roll over every Yugabyte node, for commands that restart nodes outside provision.
// It returns nil when the manifest runs no Yugabyte universe.
func newYugabyteGate(out io.Writer, manifest *inventory.Manifest, pool *fwssh.Pool) *yugabyteRoll {
	pg := manifest.Infrastructure.Postgres
	if pg == nil || !pg.Enabled || !pg.IsYugabyte() || len(pg.Nodes) == 0 {
		return nil
	}
	roll := &yugabyteRoll{
		enabled: true, out: out, hosts: postgresCandidateHosts(manifest, pg),
		u: &sshYugabyteUniverse{pool: pool, pg: pg}, timeout: yugabyteRollRecoveryTimeout, interval: yugabyteRollPollInterval,
	}
	roll.init()
	return roll
}

func newYugabyteRoll(ctx context.Context, out io.Writer, manifest *inventory.Manifest, pool *fwssh.Pool, plan *orchestrator.ExecutionPlan) *yugabyteRoll {
	roll := &yugabyteRoll{out: out, timeout: yugabyteRollRecoveryTimeout, interval: yugabyteRollPollInterval}
	roll.init()
	pg := manifest.Infrastructure.Postgres
	if pg == nil || !pg.Enabled || !pg.IsYugabyte() || len(pg.Nodes) == 0 || !planContainsTaskType(plan, "yugabyte") {
		return roll
	}
	roll.hosts = postgresCandidateHosts(manifest, pg)
	roll.u = &sshYugabyteUniverse{pool: pool, pg: pg}

	states := make([]yugabyteNodeState, 0, len(roll.hosts))
	for _, host := range roll.hosts {
		state := yugabyteNodeState{Host: host.Name, Serving: roll.u.ServesYSQL(ctx, host)}
		roll.serving[host.Name] = state.Serving
		out, err := yugabyteNodeScript(ctx, pool, host, yugabyteNodeStateScript)
		if err != nil {
			state.Err = err
		} else {
			parsed := parseYugabyteNodeState(host.Name, out)
			state.State, state.Running, state.Err = parsed.State, parsed.Running, parsed.Err
		}
		states = append(states, state)
	}
	var reason string
	roll.enabled, reason = yugabyteRollMode(states, roll.masterQuorum(ctx))
	if roll.enabled {
		fmt.Fprintf(out, "  Yugabyte nodes will be reconciled one at a time: %s\n", reason)
	} else {
		fmt.Fprintf(out, "  Yugabyte nodes will be provisioned in parallel: %s\n", reason)
	}
	return roll
}

// servingHost returns a node that served YSQL when the roll was planned.
func (y *yugabyteRoll) servingHost() (inventory.Host, bool) {
	if y == nil {
		return inventory.Host{}, false
	}
	for _, host := range y.hosts {
		if y.serving[host.Name] {
			return host, true
		}
	}
	return inventory.Host{}, false
}

// masterQuorum reports whether any node's masters report exactly one alive leader.
func (y *yugabyteRoll) masterQuorum(ctx context.Context) bool {
	for _, host := range y.hosts {
		masters, err := y.u.Masters(ctx, host)
		if err != nil {
			continue
		}
		for _, master := range masters {
			if master.Role == "LEADER" && master.State == "ALIVE" {
				return true
			}
		}
	}
	return false
}

func planContainsTaskType(plan *orchestrator.ExecutionPlan, taskType string) bool {
	if plan == nil {
		return false
	}
	for _, batch := range plan.Batches {
		if batchContainsTaskType(batch, taskType) {
			return true
		}
	}
	return false
}

// beginBatch fixes the order in which the batch's Yugabyte nodes take their turn: nodes that were down when the roll
// was planned first, then serving nodes, each group by name.
func (y *yugabyteRoll) beginBatch(batch []*orchestrator.Task) {
	if y == nil || !y.enabled {
		return
	}
	var names []string
	for _, task := range batch {
		if task.Type == "yugabyte" {
			names = append(names, task.Host)
		}
	}
	y.setOrder(names)
}

func (y *yugabyteRoll) setOrder(names []string) {
	y.mu.Lock()
	defer y.mu.Unlock()
	y.init()
	sort.SliceStable(names, func(i, j int) bool {
		if y.serving[names[i]] != y.serving[names[j]] {
			return !y.serving[names[i]]
		}
		return names[i] < names[j]
	})
	y.order, y.next = names, 0
}

// run reconciles one node through provision, which calls change right before it alters the node. Tasks other than
// Yugabyte nodes, and every task while the roll is disabled, run without it.
func (y *yugabyteRoll) run(ctx context.Context, task *orchestrator.Task, host inventory.Host, provision func(change func() error) (*taskProvisionOutcome, error)) (*taskProvisionOutcome, error) {
	if y == nil || !y.enabled || task.Type != "yugabyte" {
		return provision(nil)
	}
	var outcome *taskProvisionOutcome
	err := y.change(ctx, host, func(gate func() error) error {
		var provisionErr error
		outcome, provisionErr = provision(gate)
		return provisionErr
	})
	return outcome, err
}

// change takes host's turn and runs apply. apply calls gate right before it alters the node; the gate refuses unless
// altering host now keeps the universe available, and once gate has passed, change waits for the node to be back
// before releasing the turn. A failure stops every later change.
func (y *yugabyteRoll) change(ctx context.Context, host inventory.Host, apply func(gate func() error) error) error {
	return y.changeWithChecks(ctx, host, apply, y.requireSafeToChange, y.awaitRecovered)
}

// Master-only phases neither stop nor recover tservers. Their admission and recovery depend on master consensus,
// not on YSQL, which may remain unavailable until the later tserver phase.
func (y *yugabyteRoll) changeMaster(ctx context.Context, host inventory.Host, apply func(gate func() error) error) error {
	return y.changeWithChecks(ctx, host, apply, y.requireSafeMasterChange, y.awaitMasterRecovered)
}

func (y *yugabyteRoll) changeWithChecks(ctx context.Context, host inventory.Host, apply func(gate func() error) error, admit, recoverNode func(context.Context, inventory.Host) error) error {
	if err := y.acquire(ctx, host); err != nil {
		return err
	}
	gated := false
	gate := func() error {
		gated = true
		return admit(ctx, host)
	}
	err := apply(gate)
	if err == nil && gated {
		err = recoverNode(ctx, host)
	}
	y.release(host, err)
	return err
}

func (y *yugabyteRoll) requireSafeMasterChange(ctx context.Context, host inventory.Host) error {
	y.mu.Lock()
	failure := y.failure
	y.mu.Unlock()
	if failure != nil {
		return failure
	}
	if len(y.hosts) == 1 {
		return nil
	}
	return y.poll(ctx, "confirming master on "+host.Name+" can change", func() []string {
		for _, observer := range append(y.otherHosts(host), host) {
			masters, err := y.u.Masters(ctx, observer)
			if err != nil {
				continue
			}
			_, master, _, err := y.u.Servers(ctx, observer, host)
			if err != nil || master == "" {
				return []string{fmt.Sprintf("cannot resolve master identity for %s: %v", host.Name, err)}
			}
			// Include the target even when reported down: the masters, not a failed local probe, decide safety.
			if len(masters) == 0 {
				continue
			}
			if err := y.u.SafeToTakeDown(ctx, observer, []string{master}); err != nil {
				return []string{err.Error()}
			}
			return nil
		}
		return []string{"no node can read master membership"}
	})
}

func (y *yugabyteRoll) awaitMasterRecovered(ctx context.Context, host inventory.Host) error {
	return y.poll(ctx, "waiting for master on "+host.Name+" to recover", func() []string {
		masters, err := y.u.Masters(ctx, host)
		if err != nil {
			return []string{err.Error()}
		}
		return yugabyteMasterProblems(masters)
	})
}

// acquire waits until host's turn comes and no other node is changing.
func (y *yugabyteRoll) acquire(ctx context.Context, host inventory.Host) error {
	stop := context.AfterFunc(ctx, func() {
		y.mu.Lock()
		defer y.mu.Unlock()
		y.cond.Broadcast()
	})
	defer stop()
	y.mu.Lock()
	defer y.mu.Unlock()
	y.init()
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("stopped before reconciling Yugabyte node %s: %w", host.Name, err)
		}
		if !y.busy && y.isTurn(host.Name) {
			y.busy = true
			return nil
		}
		y.cond.Wait()
	}
}

func (y *yugabyteRoll) isTurn(name string) bool {
	for i, ordered := range y.order {
		if ordered == name {
			return i == y.next
		}
	}
	// A node outside the planned order goes after every ordered node.
	return y.next >= len(y.order)
}

func (y *yugabyteRoll) release(host inventory.Host, err error) {
	y.mu.Lock()
	defer y.mu.Unlock()
	y.busy = false
	if y.next < len(y.order) && y.order[y.next] == host.Name {
		y.next++
	}
	if err != nil && y.failure == nil {
		y.failure = fmt.Errorf("reconciling Yugabyte node %s failed: %w", host.Name, err)
	}
	y.cond.Broadcast()
}

// requireSafeToChange refuses to alter host unless the universe stays available without it. A host that does not
// serve YSQL goes through requireSafeRepair. A serving host may only go down when every other node serves, the
// masters have an alive leader and all members, no tablet is under-replicated or leaderless, and the masters confirm
// every tablet and the master quorum survive losing host's tserver and master; that confirmation is retried while
// followers catch up after the previous node.
func (y *yugabyteRoll) requireSafeToChange(ctx context.Context, host inventory.Host) error {
	y.mu.Lock()
	failure := y.failure
	y.mu.Unlock()
	if failure != nil {
		return fmt.Errorf("refusing to reconcile Yugabyte node %s: %w; fix that node and rerun, so the same change does not reach another node", host.Name, failure)
	}
	// A single-node universe has no quorum to protect and no other node to observe it through; changing its only
	// node is accepted downtime, including when that node's master is already stopped.
	others := y.otherHosts(host)
	if len(others) == 0 {
		fmt.Fprintf(y.out, "  %s is the only Yugabyte node; reconciling it takes the universe down until it is back\n", host.Name)
		return nil
	}
	if !y.u.ServesYSQL(ctx, host) {
		return y.requireSafeRepair(ctx, host)
	}
	var down []string
	var observer *inventory.Host
	for i := range others {
		if !y.u.ServesYSQL(ctx, others[i]) {
			down = append(down, others[i].Name)
		} else if observer == nil {
			observer = &others[i]
		}
	}
	if len(down) > 0 {
		sort.Strings(down)
		return fmt.Errorf("refusing to reconcile Yugabyte node %s while %s do not serve YSQL: taking it down too could lose tablet quorum; reconcile the down node(s) first",
			host.Name, strings.Join(down, ", "))
	}
	return y.poll(ctx, fmt.Sprintf("confirming Yugabyte node %s can go down", host.Name), func() []string {
		problems := y.availabilityProblems(ctx, *observer)
		if len(problems) > 0 {
			return problems
		}
		uuids, err := y.serverUUIDs(ctx, *observer, host)
		if err != nil {
			return []string{err.Error()}
		}
		if err := y.u.SafeToTakeDown(ctx, *observer, uuids); err != nil {
			return []string{fmt.Sprintf("masters report %s is not safe to take down: %v", host.Name, err)}
		}
		return nil
	})
}

// requireSafeRepair decides whether a host that does not serve YSQL may change. YSQL can fail on its own while the
// host's tserver still holds tablet replicas and its master still votes in the quorum, so a failed query proves
// nothing. The masters are asked instead, through any node that can reach them, including host itself (yb-admin and
// the master API need no YSQL). A tserver and master they no longer count as alive, or never registered, lose the
// universe nothing and host changes as a repair. Whatever they still count as alive must pass the same
// are_nodes_safe_to_take_down check as a serving node. When no node can read the masters, host is refused.
func (y *yugabyteRoll) requireSafeRepair(ctx context.Context, host inventory.Host) error {
	var readErrs []error
	for _, observer := range append(y.otherHosts(host), host) {
		if _, err := y.u.Masters(ctx, observer); err != nil {
			readErrs = append(readErrs, fmt.Errorf("%s: %w", observer.Name, err))
			continue
		}
		alive, err := y.liveServers(ctx, observer, host)
		if err != nil {
			return fmt.Errorf("refusing to reconcile Yugabyte node %s: it does not serve YSQL and the masters cannot tell whether its tserver and master are down: %w", host.Name, err)
		}
		if len(alive) == 0 {
			fmt.Fprintf(y.out, "  %s does not serve YSQL and the masters count neither its tserver nor its master as alive; reconciling it as a repair\n", host.Name)
			return nil
		}
		fmt.Fprintf(y.out, "  %s does not serve YSQL but the masters still count %s of it as alive; changing it only once they confirm it can go down\n",
			host.Name, strings.Join(alive, " and "))
		// A server that comes back while this waits is stopped by the change as well, so every attempt asks about the
		// servers alive at that moment.
		return y.poll(ctx, fmt.Sprintf("confirming Yugabyte node %s can go down", host.Name), func() []string {
			current, err := y.liveServers(ctx, observer, host)
			if err != nil {
				return []string{err.Error()}
			}
			if len(current) == 0 {
				return nil
			}
			if err := y.u.SafeToTakeDown(ctx, observer, current); err != nil {
				return []string{fmt.Sprintf("masters report %s is not safe to take down: %v", host.Name, err)}
			}
			return nil
		})
	}
	return fmt.Errorf("refusing to reconcile Yugabyte node %s: it does not serve YSQL and no node could read the masters to prove it is down: %w", host.Name, errors.Join(readErrs...))
}

// liveServers returns the uuids of host's tserver and master that the masters, read through observer, count as alive.
func (y *yugabyteRoll) liveServers(ctx context.Context, observer, host inventory.Host) ([]string, error) {
	masters, err := y.u.Masters(ctx, observer)
	if err != nil {
		return nil, err
	}
	tserver, master, tserverAlive, err := y.u.Servers(ctx, observer, host)
	if err != nil {
		return nil, err
	}
	var alive []string
	if tserver != "" && tserverAlive {
		alive = append(alive, tserver)
	}
	for _, candidate := range masters {
		if master != "" && candidate.UUID == master && candidate.State == "ALIVE" {
			alive = append(alive, master)
		}
	}
	return alive, nil
}

// awaitRecovered waits until host is back: it serves YSQL, and its tserver and master are alive. When every node
// serves, it also waits for the universe to be whole again (all masters alive with a leader, no under-replicated or
// leaderless tablet) and for every other node to be safe to take down, which only holds once host's replicas have
// caught up. With other nodes still down, only host's own recovery is required, because they are repaired next.
func (y *yugabyteRoll) awaitRecovered(ctx context.Context, host inventory.Host) error {
	fmt.Fprintf(y.out, "  Waiting for Yugabyte node %s to recover...\n", host.Name)
	err := y.poll(ctx, fmt.Sprintf("waiting for Yugabyte node %s to recover", host.Name), func() []string {
		if !y.u.ServesYSQL(ctx, host) {
			return []string{fmt.Sprintf("%s does not serve YSQL yet", host.Name)}
		}
		others := y.otherHosts(host)
		if len(others) == 0 {
			return nil
		}
		var serving []inventory.Host
		for _, other := range others {
			if y.u.ServesYSQL(ctx, other) {
				serving = append(serving, other)
			}
		}
		if len(serving) == 0 {
			return nil
		}
		observer := serving[0]
		tserver, master, alive, err := y.u.Servers(ctx, observer, host)
		if err != nil {
			return []string{err.Error()}
		}
		if !alive {
			return []string{fmt.Sprintf("the masters do not see %s's tserver %s as alive yet", host.Name, tserver)}
		}
		if master != "" {
			masters, err := y.u.Masters(ctx, observer)
			if err != nil {
				return []string{err.Error()}
			}
			for _, m := range masters {
				if m.UUID == master && m.State != "ALIVE" {
					return []string{fmt.Sprintf("%s's master is %s", host.Name, m.State)}
				}
			}
		}
		if len(serving) < len(others) {
			return nil
		}
		if problems := y.availabilityProblems(ctx, observer); len(problems) > 0 {
			return problems
		}
		for _, other := range others {
			uuids, err := y.serverUUIDs(ctx, observer, other)
			if err != nil {
				return []string{err.Error()}
			}
			if err := y.u.SafeToTakeDown(ctx, observer, uuids); err != nil {
				return []string{fmt.Sprintf("%s's replicas have not caught up yet: masters report %s is not safe to take down: %v", host.Name, other.Name, err)}
			}
		}
		return nil
	})
	if err == nil {
		fmt.Fprintf(y.out, "  Yugabyte node %s recovered\n", host.Name)
	}
	return err
}

// finalizeUpgrade completes a binary upgrade once every node runs the new version: it waits until every node serves
// YSQL and the universe is healthy, then promotes the new AutoFlags and upgrades the YSQL catalog. Both steps are
// idempotent, so a rerun after an interrupted upgrade finishes the job. After it the old binary can no longer rejoin
// the universe, so it runs only after every node has changed and recovered.
func (y *yugabyteRoll) finalizeUpgrade(ctx context.Context) error {
	var observer inventory.Host
	err := y.poll(ctx, "waiting for every Yugabyte node to serve before finalizing the upgrade", func() []string {
		var problems []string
		for _, host := range y.hosts {
			if !y.u.ServesYSQL(ctx, host) {
				problems = append(problems, fmt.Sprintf("%s does not serve YSQL", host.Name))
			}
		}
		if len(problems) > 0 {
			return problems
		}
		observer = y.hosts[0]
		return y.availabilityProblems(ctx, observer)
	})
	if err != nil {
		return err
	}
	// Finalizing promotes the AutoFlags before it touches the catalog, and a promoted universe can no longer run the
	// previous binaries. The engine validates its own catalog migration set only once it reaches the catalog step, so
	// a build whose set it rejects would fail with rollback already spent. Read that set first and apply the same rule.
	migrations, err := y.u.CatalogMigrations(ctx, observer)
	if err != nil {
		return err
	}
	if err := validateYsqlCatalogMigrations(migrations); err != nil {
		return fmt.Errorf("refusing to finalize through %s: %w; finalizing would promote the AutoFlags irreversibly and then fail on that set, so it is not attempted and the universe can still be rolled back to the previous engine", observer.Name, err)
	}
	fmt.Fprintf(y.out, "  Finalizing Yugabyte upgrade through %s (promote AutoFlags, upgrade YSQL catalog)...\n", observer.Name)
	if err := y.u.FinalizeUpgrade(ctx, observer); err != nil {
		return err
	}
	fmt.Fprintln(y.out, "  Yugabyte upgrade finalized")
	return nil
}

// rollStale restarts processes needing their installed binary or configuration applied. Each invocation owns a
// fresh ordered work list. Master phases use master-only checks; tservers require whole-node recovery. Unreadable
// process state stops the phase before any restart.
func (y *yugabyteRoll) rollStale(ctx context.Context, process string, restart func(context.Context, inventory.Host) error) error {
	var pending []string
	byName := make(map[string]inventory.Host, len(y.hosts))
	for _, host := range y.hosts {
		state, err := y.u.ProcessState(ctx, host, process)
		if err != nil {
			return err
		}
		if state == yugabyteProcessCurrent {
			continue
		}
		pending = append(pending, host.Name)
		byName[host.Name] = host
	}
	// The previous phase consumed its cursor. Only nodes requiring this phase get a turn; skipped nodes cannot
	// strand the queue, including when an interrupted upgrade resumes with a mix of current and stale processes.
	y.setOrder(pending)
	change := y.change
	if process == "yb-master" {
		change = y.changeMaster
	}
	for _, name := range y.order {
		host := byName[name]
		fmt.Fprintf(y.out, "  Restarting %s on %s to apply its installed binary and configuration\n", process, host.Name)
		if err := change(ctx, host, func(gate func() error) error {
			if gateErr := gate(); gateErr != nil {
				return gateErr
			}
			return restart(ctx, host)
		}); err != nil {
			return err
		}
	}
	return nil
}

// availabilityProblems reports what keeps the universe from losing a member now.
func (y *yugabyteRoll) availabilityProblems(ctx context.Context, observer inventory.Host) []string {
	masters, err := y.u.Masters(ctx, observer)
	if err != nil {
		return []string{err.Error()}
	}
	if problems := yugabyteMasterProblems(masters); len(problems) > 0 {
		return problems
	}
	health, err := y.u.Health(ctx, observer)
	if err != nil {
		return []string{err.Error()}
	}
	return health.problems()
}

func (y *yugabyteRoll) serverUUIDs(ctx context.Context, observer, host inventory.Host) ([]string, error) {
	tserver, master, _, err := y.u.Servers(ctx, observer, host)
	if err != nil {
		return nil, err
	}
	if tserver == "" {
		return nil, fmt.Errorf("the masters have no tablet server registered for %s", host.Name)
	}
	uuids := []string{tserver}
	if master != "" {
		uuids = append(uuids, master)
	}
	return uuids, nil
}

func (y *yugabyteRoll) otherHosts(host inventory.Host) []inventory.Host {
	var others []inventory.Host
	for _, other := range y.hosts {
		if other.Name != host.Name {
			others = append(others, other)
		}
	}
	return others
}

// poll re-evaluates check until it reports no problems, the roll's timeout passes, or ctx ends.
func (y *yugabyteRoll) poll(ctx context.Context, what string, check func() []string) error {
	deadline := time.Now().Add(y.timeout)
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("stopped while %s: %w", what, err)
		}
		problems := check()
		if len(problems) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("gave up after %s %s: %s", y.timeout, what, strings.Join(problems, "; "))
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("stopped while %s (%s): %w", what, strings.Join(problems, "; "), ctx.Err())
		case <-time.After(y.interval):
		}
	}
}

// ysqlCatalogMigrationPattern matches a YSQL catalog migration filename, whose version is a major number and an
// optional minor number: V90__28107__yb_tablet_metadata.sql is (90, 0), V90.7__29194__… is (90, 7).
var ysqlCatalogMigrationPattern = regexp.MustCompile(`^V(\d+)(?:\.(\d+))?__\d+__[_0-9A-Za-z]+\.sql$`)

// validateYsqlCatalogMigrations applies the engine's own rule to the migration set an engine build carries: versions
// ascend from the first with no gaps, by one major or one minor, and once a minor increment has occurred every later
// version must be a minor increment. A build that mixes a release branch's backported minors with the trunk's majors
// (V90.1…V90.7 then V91) fails this, and its catalog upgrade cannot run at all.
func validateYsqlCatalogMigrations(filenames []string) error {
	type version struct{ major, minor int }
	byVersion := map[version]string{}
	versions := make([]version, 0, len(filenames))
	for _, name := range filenames {
		match := ysqlCatalogMigrationPattern.FindStringSubmatch(name)
		if match == nil {
			continue
		}
		major, err := strconv.Atoi(match[1])
		if err != nil {
			return fmt.Errorf("YSQL catalog migration %q has an unreadable major version: %w", name, err)
		}
		minor := 0
		if match[2] != "" {
			if minor, err = strconv.Atoi(match[2]); err != nil {
				return fmt.Errorf("YSQL catalog migration %q has an unreadable minor version: %w", name, err)
			}
		}
		current := version{major, minor}
		if existing, clash := byVersion[current]; clash {
			return fmt.Errorf("YSQL catalog migrations %q and %q share version %d.%d", existing, name, major, minor)
		}
		byVersion[current] = name
		versions = append(versions, current)
	}
	if len(versions) == 0 {
		return fmt.Errorf("found no YSQL catalog migrations to check")
	}
	sort.Slice(versions, func(i, j int) bool {
		if versions[i].major != versions[j].major {
			return versions[i].major < versions[j].major
		}
		return versions[i].minor < versions[j].minor
	})
	previous := version{0, 0}
	usingMinors := false
	for _, current := range versions {
		minorStep := current.major == previous.major && current.minor == previous.minor+1
		majorStep := current.major == previous.major+1 && current.minor == previous.minor
		switch {
		case usingMinors && !minorStep:
			return fmt.Errorf("the installed engine's YSQL catalog migration %q is not exactly one minor version away from %d.%d, which the engine rejects", byVersion[current], previous.major, previous.minor)
		case !usingMinors && !minorStep && !majorStep:
			return fmt.Errorf("the installed engine's YSQL catalog migration %q is not exactly one major or minor version away from %d.%d, which the engine rejects", byVersion[current], previous.major, previous.minor)
		}
		if minorStep {
			usingMinors = true
		}
		previous = current
	}
	return nil
}
