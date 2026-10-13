package provisioner

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// RuntimeRoleSessionLimits are the session settings every service runtime role
// carries, written as pg_db_role_setting stores them. The postgres and yugabyte
// roles' init.yml attach the same values from their vars/main.yml.
var RuntimeRoleSessionLimits = []string{"statement_timeout=60s", "idle_in_transaction_session_timeout=60s"}

// EnsureRuntimeRoleSessionLimits attaches RuntimeRoleSessionLimits to every
// existing role in roles that does not already carry them, connecting through
// the administrative path of the service database probe on host. It returns the
// roles it altered, or with dryRun, which only reads, the roles it would alter.
func EnsureRuntimeRoleSessionLimits(ctx context.Context, sshPool *ssh.Pool, host inventory.Host, pg *inventory.PostgresConfig, roles []string, dryRun bool) ([]string, error) {
	if len(roles) == 0 {
		return nil, nil
	}
	probe, err := newServiceDatabaseProbe(sshPool, host, pg)
	if err != nil {
		return nil, err
	}
	return ensureRuntimeRoleSessionLimits(ctx, probe, roles, dryRun)
}

// A role is altered only when its settings differ: on YugabyteDB, ALTER ROLE
// changes the shared catalog and so invalidates every backend's catalog cache
// on the universe. A role's settings apply at login, so a running service takes
// them up as its pool opens connections.
func ensureRuntimeRoleSessionLimits(ctx context.Context, probe serviceDatabaseProbe, roles []string, dryRun bool) ([]string, error) {
	unique := map[string]struct{}{}
	for _, role := range roles {
		role = strings.TrimSpace(role)
		if role == "" {
			continue
		}
		if !simpleDBIdentifier.MatchString(role) {
			return nil, fmt.Errorf("invalid runtime role name %q", role)
		}
		unique[role] = struct{}{}
	}
	if len(unique) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(unique))
	for role := range unique {
		names = append(names, role)
	}
	sort.Strings(names)
	unbounded, err := probe.textRows(ctx, probe.maintenanceDatabase(), runtimeRolesWithoutSessionLimitsQuery(names))
	if err != nil {
		return nil, fmt.Errorf("read runtime role settings: %w", err)
	}
	if dryRun {
		return unbounded, nil
	}
	for _, role := range unbounded {
		if !simpleDBIdentifier.MatchString(role) {
			return nil, fmt.Errorf("unexpected role name %q in role settings", role)
		}
		for _, limit := range RuntimeRoleSessionLimits {
			setting, value, _ := strings.Cut(limit, "=")
			statement := fmt.Sprintf("ALTER ROLE %q SET %s = '%s'", role, setting, value)
			if err := probe.exec(ctx, probe.maintenanceDatabase(), statement); err != nil {
				return nil, fmt.Errorf("set %s on %s: %w", setting, role, err)
			}
		}
	}
	return unbounded, nil
}

// runtimeRolesWithoutSessionLimitsQuery lists the existing roles among names
// whose cluster-wide settings lack any of RuntimeRoleSessionLimits. names are
// validated identifiers.
func runtimeRolesWithoutSessionLimitsQuery(names []string) string {
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		quoted = append(quoted, "'"+name+"'")
	}
	limits := make([]string, 0, len(RuntimeRoleSessionLimits))
	for _, limit := range RuntimeRoleSessionLimits {
		limits = append(limits, "'"+limit+"'")
	}
	return fmt.Sprintf(`SELECT r.rolname FROM pg_roles r
WHERE r.rolname IN (%s)
  AND NOT EXISTS (
    SELECT 1 FROM pg_db_role_setting s
    WHERE s.setrole = r.oid AND s.setdatabase = 0 AND s.setconfig @> ARRAY[%s]::text[])
ORDER BY r.rolname`, strings.Join(quoted, ", "), strings.Join(limits, ", "))
}
