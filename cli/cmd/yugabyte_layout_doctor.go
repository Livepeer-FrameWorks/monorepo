package cmd

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	fwssh "frameworks/cli/pkg/ssh"
)

type yugabyteLayoutReport struct {
	Database         string
	Declared         string
	Observed         string
	Tablets          int
	Peers            int
	Drift            []string
	UnreachableNodes []string
	// Missing marks a manifest database that does not exist yet; release apply creates it.
	Missing bool
	// Err is why the database could not be inspected.
	Err string
}

// yugabyteLayoutWarning is the status of a check that found layout drift. Drift is expected on every database created
// before its layout existed, so it is reported but counts as passed.
const yugabyteLayoutWarning = "warning"

// yugabyteHostRunner opens the SSH runner for one tserver.
type yugabyteHostRunner func(inventory.Host) (fwssh.Runner, error)

func yugabytePoolRunner(sshPool *fwssh.Pool) yugabyteHostRunner {
	return func(host inventory.Host) (fwssh.Runner, error) {
		if sshPool == nil {
			return nil, fmt.Errorf("ssh pool is nil")
		}
		runner, err := sshPool.Get(&fwssh.ConnectionConfig{Address: host.ExternalIP, Port: 22, User: host.User, HostName: host.Name, Timeout: 30 * time.Second})
		if err != nil {
			return nil, fmt.Errorf("ssh connect %s: %w", host.Name, err)
		}
		return runner, nil
	}
}

// yugabyteLayoutTarget is one existing database whose placement is compared with its declared layout.
type yugabyteLayoutTarget struct {
	database provisioner.SchemaDatabase
	layout   *provisioner.DatabaseLayout
}

// doctorYugabyteLayout compares every platform database's observed YugabyteDB placement with its repository layout
// and reports distinct tablets and tablet peers per database. Drift is a warning: an existing database keeps its
// creation-time placement until it is relaid out.
func doctorYugabyteLayout(ctx context.Context, sshPool *fwssh.Pool, manifest *inventory.Manifest, pg *inventory.PostgresConfig) *health.CheckResult {
	return inspectYugabyteLayouts(ctx, yugabytePoolRunner(sshPool), postgresCandidateHosts(manifest, pg), pg, yugabyteSchemaDatabases(pg.Databases, manifest))
}

// inspectYugabyteLayouts spends one SSH command on the first tserver that lists its databases, one on that tserver for
// every database's placement, and one per tserver for the tablets of all databases, whatever the number of databases.
func inspectYugabyteLayouts(ctx context.Context, runnerFor yugabyteHostRunner, hosts []inventory.Host, pg *inventory.PostgresConfig, databases []provisioner.SchemaDatabase) *health.CheckResult {
	port := pg.EffectivePort()
	primary, existing, err := yugabyteLayoutPrimary(ctx, runnerFor, hosts, port)
	if err != nil {
		return &health.CheckResult{Name: "yugabyte_layout", CheckedAt: time.Now(), Status: "unhealthy", Error: err.Error()}
	}
	var reports []yugabyteLayoutReport
	var targets []yugabyteLayoutTarget
	for _, database := range databases {
		if _, ok := existing[database.Name]; !ok {
			if layoutSource(database) != "" {
				reports = append(reports, yugabyteLayoutReport{Database: database.Name, Missing: true})
			}
			continue
		}
		layout, layoutErr := provisioner.YugabyteLayoutForDatabase(databaseLayoutSource(database))
		if layoutErr != nil {
			reports = append(reports, yugabyteLayoutReport{Database: database.Name, Err: layoutErr.Error()})
			continue
		}
		if layout != nil {
			targets = append(targets, yugabyteLayoutTarget{database: database, layout: layout})
		}
	}
	reports = append(reports, readYugabyteLayouts(ctx, runnerFor, hosts, primary, port, targets)...)
	return summarizeYugabyteLayout(reports)
}

func databaseLayoutSource(database provisioner.SchemaDatabase) string {
	if database.SourceName != "" {
		return database.SourceName
	}
	return database.Name
}

// layoutSource returns the logical database whose layout a physical database follows, or "" when it declares none.
func layoutSource(database provisioner.SchemaDatabase) string {
	source := databaseLayoutSource(database)
	if layout, err := provisioner.YugabyteLayoutForDatabase(source); err != nil || layout == nil {
		return ""
	}
	return source
}

// yugabyteLayoutPrimary returns the first tserver whose local YSQL lists the cluster's databases, with that list.
func yugabyteLayoutPrimary(ctx context.Context, runnerFor yugabyteHostRunner, hosts []inventory.Host, port int) (inventory.Host, map[string]struct{}, error) {
	var lastErr error
	for _, host := range hosts {
		names, err := yugabyteDatabaseNames(ctx, runnerFor, host, port)
		if err == nil {
			return host, names, nil
		}
		lastErr = fmt.Errorf("%s: %w", host.Name, err)
	}
	if lastErr == nil {
		return inventory.Host{}, nil, fmt.Errorf("no yugabyte tserver with healthy local YSQL")
	}
	return inventory.Host{}, nil, fmt.Errorf("no yugabyte tserver with healthy local YSQL (last: %w)", lastErr)
}

func yugabyteDatabaseNames(ctx context.Context, runnerFor yugabyteHostRunner, host inventory.Host, port int) (map[string]struct{}, error) {
	queryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	results, err := runYugabyteLocalQueries(queryCtx, runnerFor, host, port, []provisioner.YugabyteLocalQuery{{Database: "yugabyte", SQL: "SELECT datname FROM pg_database"}})
	if err != nil {
		return nil, fmt.Errorf("list databases: %w", err)
	}
	names := map[string]struct{}{}
	if err := results[0].Scan(func(scan func(dest ...any) error) error {
		var name string
		if scanErr := scan(&name); scanErr != nil {
			return scanErr
		}
		names[name] = struct{}{}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("list databases: %w", err)
	}
	return names, nil
}

// inspectYugabyteLayout reads one database's placement and tablets for a relayout plan.
func inspectYugabyteLayout(ctx context.Context, runnerFor yugabyteHostRunner, hosts []inventory.Host, primary inventory.Host, pg *inventory.PostgresConfig, database provisioner.SchemaDatabase) (*yugabyteLayoutReport, error) {
	layout, err := provisioner.YugabyteLayoutForDatabase(databaseLayoutSource(database))
	if err != nil {
		return nil, err
	}
	if layout == nil {
		return nil, nil
	}
	reports := readYugabyteLayouts(ctx, runnerFor, hosts, primary, pg.EffectivePort(), []yugabyteLayoutTarget{{database: database, layout: layout}})
	if reports[0].Err != "" {
		return nil, fmt.Errorf("%s", reports[0].Err)
	}
	return &reports[0], nil
}

// readYugabyteLayouts reads every target's colocation and relation placement from primary and every tserver's serving
// tablets, all concurrently and each host in one SSH command. A tserver that cannot be read is listed as unreachable
// on every report instead of failing the check.
func readYugabyteLayouts(ctx context.Context, runnerFor yugabyteHostRunner, hosts []inventory.Host, primary inventory.Host, port int, targets []yugabyteLayoutTarget) []yugabyteLayoutReport {
	if len(targets) == 0 {
		return nil
	}
	queries := make([]provisioner.YugabyteLocalQuery, 0, 2*len(targets))
	for _, target := range targets {
		queries = append(queries,
			provisioner.YugabyteLocalQuery{Database: target.database.Name, SQL: "SELECT yb_is_database_colocated()"},
			provisioner.YugabyteLocalQuery{Database: target.database.Name, SQL: provisioner.YugabyteRelationPlacementQuery})
	}
	var placement []provisioner.YugabyteLocalRows
	var placementErr error
	tablets := make([]map[string][]string, len(hosts))
	tabletErrs := make([]error, len(hosts))

	var wg sync.WaitGroup
	wg.Go(func() {
		queryCtx, cancel := context.WithTimeout(ctx, 30*time.Second+time.Duration(len(queries))*5*time.Second)
		defer cancel()
		placement, placementErr = runYugabyteLocalQueries(queryCtx, runnerFor, primary, port, queries)
	})
	for i, host := range hosts {
		wg.Go(func() {
			tablets[i], tabletErrs[i] = yugabyteServingTablets(ctx, runnerFor, host, port)
		})
	}
	wg.Wait()

	reports := make([]yugabyteLayoutReport, 0, len(targets))
	for t, target := range targets {
		name := target.database.Name
		if placementErr != nil {
			reports = append(reports, yugabyteLayoutReport{Database: name, Err: fmt.Sprintf("read placement on %s: %v", primary.Name, placementErr)})
			continue
		}
		var colocated bool
		if err := placement[2*t].Scan(func(scan func(dest ...any) error) error { return scan(&colocated) }); err != nil {
			reports = append(reports, yugabyteLayoutReport{Database: name, Err: fmt.Sprintf("read database colocation: %v", err)})
			continue
		}
		var relations []provisioner.YugabyteRelationPlacement
		if err := placement[2*t+1].Scan(func(scan func(dest ...any) error) error {
			var relation provisioner.YugabyteRelationPlacement
			if scanErr := scan(&relation.Relation, &relation.Kind, &relation.Table, &relation.Colocated, &relation.NumTablets); scanErr != nil {
				return scanErr
			}
			relations = append(relations, relation)
			return nil
		}); err != nil {
			reports = append(reports, yugabyteLayoutReport{Database: name, Err: fmt.Sprintf("read relation placement: %v", err)})
			continue
		}
		report := yugabyteLayoutReport{
			Database: name,
			Declared: string(target.layout.Layout),
			Observed: string(provisioner.DatabaseLayoutDistributed),
			Drift:    provisioner.YugabytePlacementDrift(target.layout, colocated, relations),
		}
		if colocated {
			report.Observed = string(provisioner.DatabaseLayoutColocated)
		}
		distinct := map[string]struct{}{}
		for i, host := range hosts {
			if tabletErrs[i] != nil {
				report.UnreachableNodes = append(report.UnreachableNodes, host.Name)
				continue
			}
			report.Peers += len(tablets[i][name])
			for _, id := range tablets[i][name] {
				distinct[id] = struct{}{}
			}
		}
		report.Tablets = len(distinct)
		reports = append(reports, report)
	}
	return reports
}

// yugabyteServingTablets returns the serving tablet ids the tserver hosts, keyed by database.
func yugabyteServingTablets(ctx context.Context, runnerFor yugabyteHostRunner, host inventory.Host, port int) (map[string][]string, error) {
	queryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	results, err := runYugabyteLocalQueries(queryCtx, runnerFor, host, port, []provisioner.YugabyteLocalQuery{{Database: "yugabyte", SQL: provisioner.YugabyteServingTabletsQuery}})
	if err != nil {
		return nil, err
	}
	byDatabase := map[string][]string{}
	err = results[0].Scan(func(scan func(dest ...any) error) error {
		var database, id string
		if scanErr := scan(&database, &id); scanErr != nil {
			return scanErr
		}
		byDatabase[database] = append(byDatabase[database], id)
		return nil
	})
	return byDatabase, err
}

func runYugabyteLocalQueries(ctx context.Context, runnerFor yugabyteHostRunner, host inventory.Host, port int, queries []provisioner.YugabyteLocalQuery) ([]provisioner.YugabyteLocalRows, error) {
	runner, err := runnerFor(host)
	if err != nil {
		return nil, err
	}
	return provisioner.RunYugabyteLocalQueries(ctx, runner, port, "yugabyte", queries)
}

func summarizeYugabyteLayout(reports []yugabyteLayoutReport) *health.CheckResult {
	result := &health.CheckResult{Name: "yugabyte_layout", CheckedAt: time.Now(), Metadata: map[string]string{"check_kind": "physical_layout"}}
	sort.Slice(reports, func(i, j int) bool { return reports[i].Database < reports[j].Database })
	var drifted, incomplete, missing, failed []string
	for _, report := range reports {
		switch {
		case report.Missing:
			missing = append(missing, report.Database)
			result.Metadata[report.Database] = "not created yet"
			continue
		case report.Err != "":
			failed = append(failed, fmt.Sprintf("%s: %s", report.Database, report.Err))
			result.Metadata[report.Database] = "error: " + report.Err
			continue
		}
		line := fmt.Sprintf("layout=%s observed=%s tablets=%d peers=%d drift=%d", report.Declared, report.Observed, report.Tablets, report.Peers, len(report.Drift))
		if len(report.UnreachableNodes) > 0 {
			line += fmt.Sprintf(" tablet_counts_exclude=%s", strings.Join(report.UnreachableNodes, ","))
			incomplete = append(incomplete, fmt.Sprintf("%s: tablets not read from %s", report.Database, strings.Join(report.UnreachableNodes, ", ")))
		}
		result.Metadata[report.Database] = line
		if len(report.Drift) == 0 {
			continue
		}
		shown := report.Drift
		if len(shown) > 3 {
			shown = append(append([]string{}, shown[:3]...), fmt.Sprintf("%d more", len(report.Drift)-3))
		}
		drifted = append(drifted, fmt.Sprintf("%s: %s", report.Database, strings.Join(shown, "; ")))
	}
	var problems []string
	if len(failed) > 0 {
		problems = append(problems, fmt.Sprintf("could not inspect %s", strings.Join(failed, " | ")))
	}
	if len(incomplete) > 0 {
		problems = append(problems, fmt.Sprintf("tablet evidence is incomplete (%s); rerun once every tserver answers local YSQL", strings.Join(incomplete, " | ")))
	}
	var notes []string
	if len(drifted) > 0 {
		notes = append(notes, fmt.Sprintf("%d of %d database(s) keep a placement other than their declared layout until they are relaid out (%s)",
			len(drifted), len(reports)-len(missing)-len(failed), strings.Join(drifted, " | ")))
	}
	if len(missing) > 0 {
		notes = append(notes, fmt.Sprintf("not created yet: %s", strings.Join(missing, ", ")))
	}
	switch {
	case len(problems) > 0:
		result.Status = "degraded"
		result.Error = strings.Join(append(problems, notes...), "; ")
	case len(drifted) > 0:
		result.Status = yugabyteLayoutWarning
		result.Error = strings.Join(notes, "; ")
	default:
		result.OK = true
		result.Status = "healthy"
		result.Message = fmt.Sprintf("%d database(s) match their declared layout", len(reports)-len(missing))
		if len(missing) > 0 {
			result.Message += "; " + strings.Join(notes, "; ")
		}
	}
	return result
}
