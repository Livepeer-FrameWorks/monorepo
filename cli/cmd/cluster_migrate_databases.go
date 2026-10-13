package cmd

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

// Tests replace these seams to verify bootstrap ordering without a live cluster.
var (
	ensureServiceDatabasesFn     = ensureServiceDatabases
	readServiceDatabaseStatesFn  = provisioner.ReadServiceDatabaseStates
	initializeServiceDatabasesFn = provisioner.InitializeServiceDatabases
	bootstrapServiceDatabasesFn  = bootstrapServiceDatabases
	migrateBelowFloorGuardFn     = runBelowFloorGuardExcluding
	// ensureRuntimeRoleSessionLimitsFn attaches the runtime-role session limits over SSH.
	ensureRuntimeRoleSessionLimitsFn = provisioner.EnsureRuntimeRoleSessionLimits
	// refuseDuringYugabyteRelayoutFn reads the relayout journal through a healthy tserver.
	refuseDuringYugabyteRelayoutFn = refuseDuringYugabyteRelayout
)

// manifestServiceDatabases returns the manifest's physical service databases
// that have an embedded baseline. Yugabyte regional aliases resolve to their
// logical baseline and schema.
func manifestServiceDatabases(manifest *inventory.Manifest, databases []inventory.DatabaseConfig) ([]provisioner.SchemaDatabase, error) {
	schemaDatabases := yugabyteSchemaDatabases(databases, manifest)
	return provisioner.ServiceDatabasesWithBaseline(schemaDatabases)
}

// releasePlanServiceDatabases lists the manifest's service databases for the
// static release plan, which runs without cluster access; whether each one is
// missing is decided by the live probe in `release apply` and `cluster migrate`.
func releasePlanServiceDatabases(manifest *inventory.Manifest) []string {
	if hasUnscopedDatabaseDeployments(manifest) {
		var names []string
		for _, deployment := range manifest.SQLDeployments() {
			view, _ := manifest.WithDatabaseDeployment(deployment.Name) //nolint:errcheck // SQLDeployments enumerates enabled deployments from this same manifest.
			for _, name := range releasePlanServiceDatabases(view) {
				names = append(names, deployment.Name+"/"+name)
			}
		}
		return names
	}
	pg := manifest.Infrastructure.Postgres
	if pg == nil || !pg.Enabled {
		return nil
	}
	databases, err := manifestServiceDatabases(manifest, pg.Databases)
	if err != nil {
		return nil
	}
	return schemaDatabaseNameList(databases)
}

// postgresAdminHost selects where database administration runs: the first
// Yugabyte tserver whose local YSQL answers, or the PostgreSQL host.
func postgresAdminHost(ctx context.Context, manifest *inventory.Manifest, pg *inventory.PostgresConfig, sshPool *ssh.Pool) (inventory.Host, error) {
	hosts := postgresCandidateHosts(manifest, pg)
	if len(hosts) == 0 {
		return inventory.Host{}, fmt.Errorf("no postgres/yugabyte hosts resolvable in manifest")
	}
	if !pg.IsYugabyte() {
		return hosts[0], nil
	}
	host, ok := firstHealthyYugabyteHost(ctx, sshPool, hosts, pg)
	if !ok {
		return inventory.Host{}, fmt.Errorf("no yugabyte tserver with healthy local YSQL among %d candidate(s)", len(hosts))
	}
	return host, nil
}

// ensureServiceDatabases initializes absent or empty service databases before
// migration ledger reads. Populated databases without provenance require an
// explicit completion opt-in and are verified against a scratch baseline.
func ensureServiceDatabases(ctx context.Context, cmd *cobra.Command, rc *resolvedCluster, sshPool *ssh.Pool, dryRun, completeInterruptedBaselines bool) ([]provisioner.SchemaDatabase, error) {
	if hasUnscopedDatabaseDeployments(rc.Manifest) {
		var all []provisioner.SchemaDatabase
		err := forEachDatabaseDeployment(rc.Manifest, func(view *inventory.Manifest) error {
			items, err := ensureServiceDatabases(ctx, cmd, rc.withDatabaseManifest(view), sshPool, dryRun, completeInterruptedBaselines)
			all = append(all, items...)
			return err
		})
		return all, err
	}
	pg := rc.Manifest.Infrastructure.Postgres
	if pg == nil || !pg.Enabled {
		return nil, nil
	}
	ensured, err := ensureDeploymentServiceDatabases(ctx, cmd, rc, sshPool, dryRun, completeInterruptedBaselines)
	if err != nil {
		return nil, err
	}
	if err := convergeInstanceRuntimeRoleSessionLimits(ctx, cmd.OutOrStdout(), sshPool, rc.Manifest, pg, dryRun); err != nil {
		return nil, err
	}
	return ensured, nil
}

// ensureDeploymentServiceDatabases is ensureServiceDatabases for the databases
// on the deployment's own server; Postgres instances hold no service database.
func ensureDeploymentServiceDatabases(ctx context.Context, cmd *cobra.Command, rc *resolvedCluster, sshPool *ssh.Pool, dryRun, completeInterruptedBaselines bool) ([]provisioner.SchemaDatabase, error) {
	out := cmd.OutOrStdout()
	manifest := rc.Manifest
	pg := manifest.Infrastructure.Postgres
	candidates, err := manifestServiceDatabases(manifest, pg.Databases)
	if err != nil {
		return nil, fmt.Errorf("collect service databases: %w", err)
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	host, err := postgresAdminHost(ctx, manifest, pg, sshPool)
	if err != nil {
		return nil, err
	}
	if pg.IsYugabyte() {
		if err = refuseDuringYugabyteRelayoutFn(ctx, sshPool, host, pg); err != nil {
			return nil, err
		}
	}
	states, err := readServiceDatabaseStatesFn(ctx, sshPool, host, pg, candidates)
	if err != nil {
		return nil, fmt.Errorf("probe service databases: %w", err)
	}
	var unverified []provisioner.SchemaDatabase
	for _, database := range candidates {
		if states[database.Name].HasUnverifiedSchema() {
			unverified = append(unverified, database)
		}
	}
	if len(unverified) > 0 && !completeInterruptedBaselines {
		return nil, fmt.Errorf("service database(s) %s contain tables but have no baseline marker or migration history; inspect them, then rerun with --complete-interrupted-baselines to reapply and verify the current baseline", strings.Join(schemaDatabaseNameList(unverified), ", "))
	}
	pending, err := provisioner.PlanServiceDatabaseBootstrap(candidates, states)
	if err != nil {
		return nil, err
	}
	if len(pending) == 0 {
		fmt.Fprintf(out, "Service databases: all %d present with their baselines.\n", len(candidates))
		return nil, convergeRuntimeRoleSessionLimits(ctx, out, sshPool, host, manifest, pg, dryRun)
	}

	fmt.Fprintln(out, "Service databases missing their baseline on this cluster:")
	for _, database := range pending {
		action := "create database and roles, apply baseline"
		switch state := states[database.Name]; {
		case state.HasUnverifiedSchema():
			action = fmt.Sprintf("tables without baseline marker or migration history: ensure roles, re-apply baseline, compare schema %s with a scratch reference database %s__baseline_check_<hex> built from the same baseline (dropped afterwards), refuse on any difference", database.Schema, database.Name)
		case state.Exists:
			action = "database exists without tables: ensure roles, apply baseline"
		}
		fmt.Fprintf(out, "  - %s (owner %s, runtime role %s, baseline schema/%s.sql): %s\n",
			database.Name, database.Owner, database.RuntimeRole, database.SourceName, action)
	}
	if dryRun {
		fmt.Fprintln(out, "  [DRY-RUN] no database, role, or baseline SQL was sent; the planned databases are skipped by the ledger checks below.")
		if limitErr := convergeRuntimeRoleSessionLimits(ctx, out, sshPool, host, manifest, pg, dryRun); limitErr != nil {
			return nil, limitErr
		}
		return pending, nil
	}

	apply := func(ctx context.Context, databases []provisioner.SchemaDatabase) error {
		return bootstrapServiceDatabasesFn(ctx, cmd, rc, sshPool, host, databases)
	}
	if bootstrapErr := initializeServiceDatabasesFn(ctx, sshPool, host, pg, pending, states, apply); bootstrapErr != nil {
		return nil, fmt.Errorf("bootstrap service databases: %w", bootstrapErr)
	}
	verified, err := readServiceDatabaseStatesFn(ctx, sshPool, host, pg, pending)
	if err != nil {
		return nil, fmt.Errorf("verify bootstrapped service databases: %w", err)
	}
	stillPending, err := provisioner.PlanServiceDatabaseBootstrap(pending, verified)
	if err != nil {
		return nil, err
	}
	if len(stillPending) > 0 {
		return nil, fmt.Errorf("service database(s) %s still have no baseline after bootstrap; refusing to continue", strings.Join(schemaDatabaseNameList(stillPending), ", "))
	}
	ux.Success(out, fmt.Sprintf("Initialized service database(s): %s", strings.Join(schemaDatabaseNameList(pending), ", ")))
	if limitErr := convergeRuntimeRoleSessionLimits(ctx, out, sshPool, host, manifest, pg, dryRun); limitErr != nil {
		return nil, limitErr
	}
	return pending, nil
}

// convergeRuntimeRoleSessionLimits attaches the session limits to every
// runtime role of the deployment's databases, the roles the database role's
// init.yml creates. The role attaches them when it creates a role; this pass
// brings a role that existed before the limits up to them, so `release apply`
// and `cluster migrate --phase expand` converge a running cluster.
func convergeRuntimeRoleSessionLimits(ctx context.Context, out io.Writer, sshPool *ssh.Pool, host inventory.Host, manifest *inventory.Manifest, pg *inventory.PostgresConfig, dryRun bool) error {
	return boundRuntimeRoleSessions(ctx, out, "", sshPool, host, pg, runtimeRoleNames(expandedYugabyteDatabaseConfigs(pg.Databases, manifest)), dryRun)
}

// convergeInstanceRuntimeRoleSessionLimits bounds the runtime roles of each
// named Postgres instance's databases on that instance's own server: its host,
// over the local socket of its port.
func convergeInstanceRuntimeRoleSessionLimits(ctx context.Context, out io.Writer, sshPool *ssh.Pool, manifest *inventory.Manifest, pg *inventory.PostgresConfig, dryRun bool) error {
	for i := range pg.Instances {
		inst := &pg.Instances[i]
		roles := runtimeRoleNames(inst.Databases)
		if len(roles) == 0 {
			continue
		}
		host, ok := manifest.GetHost(inst.Host)
		if !ok {
			return fmt.Errorf("postgres instance %s: host %s not found", inst.Name, inst.Host)
		}
		server := &inventory.PostgresConfig{Enabled: true, Engine: "postgres", Host: inst.Host, Port: postgresInstancePort(inst)}
		if err := boundRuntimeRoleSessions(ctx, out, " on instance "+inst.Name, sshPool, host, server, roles, dryRun); err != nil {
			return fmt.Errorf("postgres instance %s: %w", inst.Name, err)
		}
	}
	return nil
}

func runtimeRoleNames(databases []inventory.DatabaseConfig) []string {
	seen := map[string]struct{}{}
	var roles []string
	for _, db := range databases {
		role := databaseRuntimeRole(db)
		if _, dup := seen[role]; role == "" || dup {
			continue
		}
		seen[role] = struct{}{}
		roles = append(roles, role)
	}
	sort.Strings(roles)
	return roles
}

// boundRuntimeRoleSessions attaches the session limits to roles on the server
// pg describes, reached through host; scope names that server in the output.
func boundRuntimeRoleSessions(ctx context.Context, out io.Writer, scope string, sshPool *ssh.Pool, host inventory.Host, pg *inventory.PostgresConfig, roles []string, dryRun bool) error {
	if len(roles) == 0 {
		return nil
	}
	altered, err := ensureRuntimeRoleSessionLimitsFn(ctx, sshPool, host, pg, roles, dryRun)
	if err != nil {
		return fmt.Errorf("runtime role session limits: %w", err)
	}
	limits := strings.Join(provisioner.RuntimeRoleSessionLimits, ", ")
	switch {
	case len(altered) == 0:
		fmt.Fprintf(out, "Runtime role sessions%s: all %d role(s) carry %s.\n", scope, len(roles), limits)
	case dryRun:
		fmt.Fprintf(out, "  [DRY-RUN] would bound runtime role sessions%s (%s): %s\n", scope, limits, strings.Join(altered, ", "))
	default:
		ux.Success(out, fmt.Sprintf("Bounded runtime role sessions%s (%s): %s", scope, limits, strings.Join(altered, ", ")))
	}
	return nil
}

// bootstrapServiceDatabases applies the normal database role to the selected
// manifest databases and any already-created baseline references.
func bootstrapServiceDatabases(ctx context.Context, cmd *cobra.Command, rc *resolvedCluster, sshPool *ssh.Pool, host inventory.Host, databases []provisioner.SchemaDatabase) error {
	init, err := postgresInitConfig(rc, schemaDatabaseNameSet(databases))
	if err != nil {
		return err
	}
	prov, err := provisioner.GetProvisioner(init.service, sshPool)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Initializing %s database(s) %s...\n", init.service, strings.Join(schemaDatabaseNameList(databases), ", "))
	if err := prov.Initialize(ctx, host, init.config); err != nil {
		return fmt.Errorf("initialize %s databases: %w", init.service, err)
	}
	return applyPostgresBaselineSchemas(ctx, cmd.OutOrStdout(), init.service, host, init.config, prov, databases)
}

// serviceDatabaseBootstrapRefusal is the pre-deploy gate refusal for a service
// whose owned database is missing or has no baseline on the cluster.
func serviceDatabaseBootstrapRefusal(serviceName, target string, pending []provisioner.SchemaDatabase, states map[string]provisioner.ServiceDatabaseState) error {
	recoveryFlag := ""
	for _, database := range pending {
		if states[database.Name].HasUnverifiedSchema() {
			recoveryFlag = " --complete-interrupted-baselines"
			break
		}
	}
	return fmt.Errorf("[gate] %s %s owns database(s) %s, which are missing or have no baseline on this cluster; initialize them before deploying\n\nrun: frameworks cluster release apply --version %s%s\n or: frameworks cluster migrate --phase expand --to-version %s%s",
		serviceName, target, strings.Join(schemaDatabaseNameList(pending), ", "), target, recoveryFlag, target, recoveryFlag)
}

func schemaDatabaseNameSet(databases []provisioner.SchemaDatabase) map[string]struct{} {
	if len(databases) == 0 {
		return nil
	}
	names := make(map[string]struct{}, len(databases))
	for _, database := range databases {
		names[database.Name] = struct{}{}
	}
	return names
}

func schemaDatabaseNameList(databases []provisioner.SchemaDatabase) []string {
	names := make([]string, 0, len(databases))
	for _, database := range databases {
		names = append(names, database.Name)
	}
	sort.Strings(names)
	return names
}

func excludeSchemaDatabases(databases []provisioner.SchemaDatabase, excluded map[string]struct{}) []provisioner.SchemaDatabase {
	if len(excluded) == 0 {
		return databases
	}
	out := make([]provisioner.SchemaDatabase, 0, len(databases))
	for _, database := range databases {
		if _, skip := excluded[strings.TrimSpace(database.Name)]; !skip {
			out = append(out, database)
		}
	}
	return out
}
