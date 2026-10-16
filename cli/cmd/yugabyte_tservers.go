package cmd

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"
)

const yugabyteTServerRPCPort = "9100"

var yugabyteTServerUUID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// yugabyteTServerListCommand asks the masters for every registered tserver.
func yugabyteTServerListCommand(masterAddresses string) string {
	return provisioner.YugabyteBinaryResolverShell + fmt.Sprintf(`
set -eu
admin="$(fw_yb_bin yb-admin)"
"$admin" --master_addresses %s list_all_tablet_servers
`, ssh.ShellQuote(masterAddresses))
}

// parseYugabyteTServerList maps each tserver's RPC host:port to its status
// (ALIVE or DEAD) from `yb-admin list_all_tablet_servers`, whose rows start
// with the tserver UUID followed by the RPC address.
func parseYugabyteTServerList(output string) map[string]string {
	statuses := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || !yugabyteTServerUUID.MatchString(fields[0]) {
			continue
		}
		status := ""
		for _, f := range fields[2:] {
			if f == "ALIVE" || f == "DEAD" {
				status = f
				break
			}
		}
		if status != "" {
			statuses[fields[1]] = status
		}
	}
	return statuses
}

// deadYugabyteTServers compares the masters' tserver list with the manifest
// nodes, addressed by their mesh address on the tserver RPC port.
func deadYugabyteTServers(statuses map[string]string, expected map[string]string) []string {
	var dead []string
	for addr, node := range expected {
		switch statuses[addr] {
		case "ALIVE":
		case "":
			dead = append(dead, fmt.Sprintf("%s: tserver %s not registered with the masters", node, addr))
		default:
			dead = append(dead, fmt.Sprintf("%s: tserver %s is %s", node, addr, statuses[addr]))
		}
	}
	sort.Strings(dead)
	return dead
}

// yugabyteDeadTServers asks the first master that answers for the tserver
// list and returns the manifest nodes it does not report ALIVE.
func yugabyteDeadTServers(ctx context.Context, pool *ssh.Pool, manifest *inventory.Manifest, pg *inventory.PostgresConfig) ([]string, error) {
	if pg == nil || !pg.IsYugabyte() || len(pg.Nodes) == 0 {
		return nil, nil
	}
	expected := make(map[string]string, len(pg.Nodes))
	for _, node := range pg.Nodes {
		expected[net.JoinHostPort(manifest.MeshAddress(node.Host), yugabyteTServerRPCPort)] = node.Host
	}
	masterAddresses := pg.MasterAddresses(manifest.MeshAddress)
	base := provisioner.NewBaseProvisioner("yugabyte-tservers", pool)
	var errs []string
	for _, node := range pg.Nodes {
		host, ok := manifest.GetHost(node.Host)
		if !ok {
			errs = append(errs, fmt.Sprintf("%s: host not in manifest", node.Host))
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		result, err := base.RunCommand(probeCtx, host, yugabyteTServerListCommand(masterAddresses))
		cancel()
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", node.Host, err))
			continue
		}
		statuses := parseYugabyteTServerList(result.Stdout)
		if len(statuses) == 0 {
			errs = append(errs, fmt.Sprintf("%s: no tservers in yb-admin output", node.Host))
			continue
		}
		return deadYugabyteTServers(statuses, expected), nil
	}
	return nil, fmt.Errorf("no master listed the tservers: %s", strings.Join(errs, "; "))
}
