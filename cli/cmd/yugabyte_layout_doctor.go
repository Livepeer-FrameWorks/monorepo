package cmd

import (
	"context"
	"fmt"
	"sort"
	"strings"
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

// doctorYugabyteLayout compares every platform database's observed YugabyteDB placement with its repository layout
// and reports distinct tablets and tablet peers per database. Drift is a warning: an existing database keeps its
// creation-time placement until it is relaid out.
func doctorYugabyteLayout(ctx context.Context, sshPool *fwssh.Pool, manifest *inventory.Manifest, pg *inventory.PostgresConfig) *health.CheckResult {
	hosts := postgresCandidateHosts(manifest, pg)
	primary, ok := firstHealthyYugabyteHost(ctx, sshPool, hosts, pg)
	if !ok {
		return &health.CheckResult{Name: "yugabyte_layout", CheckedAt: time.Now(), Status: "unhealthy", Error: "no yugabyte tserver with healthy local YSQL"}
	}
	existing, err := yugabyteDatabaseNames(ctx, sshPool, primary, pg)
	if err != nil {
		return &health.CheckResult{Name: "yugabyte_layout", CheckedAt: time.Now(), Status: "unhealthy", Error: err.Error()}
	}
	// A tserver that cannot be read once is skipped for every later database instead of timing out again.
	unreachable := map[string]error{}
	var reports []yugabyteLayoutReport
	for _, database := range yugabyteSchemaDatabases(pg.Databases, manifest) {
		if _, ok := existing[database.Name]; !ok {
			if layoutSource(database) != "" {
				reports = append(reports, yugabyteLayoutReport{Database: database.Name, Missing: true})
			}
			continue
		}
		report, inspectErr := inspectYugabyteLayout(ctx, sshPool, hosts, primary, pg, database, unreachable)
		if inspectErr != nil {
			reports = append(reports, yugabyteLayoutReport{Database: database.Name, Err: inspectErr.Error()})
			continue
		}
		if report != nil {
			reports = append(reports, *report)
		}
	}
	return summarizeYugabyteLayout(reports)
}

// layoutSource returns the logical database whose layout a physical database follows, or "" when it declares none.
func layoutSource(database provisioner.SchemaDatabase) string {
	source := database.SourceName
	if source == "" {
		source = database.Name
	}
	if layout, err := provisioner.YugabyteLayoutForDatabase(source); err != nil || layout == nil {
		return ""
	}
	return source
}

func yugabyteDatabaseNames(ctx context.Context, sshPool *fwssh.Pool, host inventory.Host, pg *inventory.PostgresConfig) (map[string]struct{}, error) {
	executor, err := yugabyteLocalExecutor(sshPool, host)
	if err != nil {
		return nil, err
	}
	queryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	names := map[string]struct{}{}
	conn := provisioner.ConnParams{Port: pg.EffectivePort(), User: "yugabyte", Database: "yugabyte"}
	if err = executor.QueryRows(queryCtx, conn, "SELECT datname FROM pg_database", nil, func(scan func(dest ...any) error) error {
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

func inspectYugabyteLayout(ctx context.Context, sshPool *fwssh.Pool, hosts []inventory.Host, primary inventory.Host, pg *inventory.PostgresConfig, database provisioner.SchemaDatabase, unreachable map[string]error) (*yugabyteLayoutReport, error) {
	source := database.SourceName
	if source == "" {
		source = database.Name
	}
	layout, err := provisioner.YugabyteLayoutForDatabase(source)
	if err != nil {
		return nil, err
	}
	if layout == nil {
		return nil, nil
	}
	conn := provisioner.ConnParams{Port: pg.EffectivePort(), User: "yugabyte", Database: database.Name}

	executor, err := yugabyteLocalExecutor(sshPool, primary)
	if err != nil {
		return nil, err
	}
	queryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var colocated bool
	if err := executor.QueryRow(queryCtx, conn, "SELECT yb_is_database_colocated()", nil, &colocated); err != nil {
		return nil, fmt.Errorf("read database colocation: %w", err)
	}
	var relations []provisioner.YugabyteRelationPlacement
	if err := executor.QueryRows(queryCtx, conn, provisioner.YugabyteRelationPlacementQuery, nil, func(scan func(dest ...any) error) error {
		var relation provisioner.YugabyteRelationPlacement
		if scanErr := scan(&relation.Relation, &relation.Kind, &relation.Table, &relation.Colocated, &relation.NumTablets); scanErr != nil {
			return scanErr
		}
		relations = append(relations, relation)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("read relation placement: %w", err)
	}

	report := &yugabyteLayoutReport{
		Database: database.Name,
		Declared: string(layout.Layout),
		Observed: string(provisioner.DatabaseLayoutDistributed),
		Drift:    provisioner.YugabytePlacementDrift(layout, colocated, relations),
	}
	if colocated {
		report.Observed = string(provisioner.DatabaseLayoutColocated)
	}
	tablets := map[string]struct{}{}
	for _, host := range hosts {
		if _, down := unreachable[host.Name]; down {
			report.UnreachableNodes = append(report.UnreachableNodes, host.Name)
			continue
		}
		peers, err := yugabyteLocalTabletIDs(ctx, sshPool, host, conn)
		if err != nil {
			unreachable[host.Name] = err
			report.UnreachableNodes = append(report.UnreachableNodes, host.Name)
			continue
		}
		report.Peers += len(peers)
		for _, id := range peers {
			tablets[id] = struct{}{}
		}
	}
	report.Tablets = len(tablets)
	return report, nil
}

func yugabyteLocalTabletIDs(ctx context.Context, sshPool *fwssh.Pool, host inventory.Host, conn provisioner.ConnParams) ([]string, error) {
	executor, err := yugabyteLocalExecutor(sshPool, host)
	if err != nil {
		return nil, err
	}
	queryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var ids []string
	err = executor.QueryRows(queryCtx, conn, provisioner.YugabyteServingTabletsQuery, nil, func(scan func(dest ...any) error) error {
		var id string
		if scanErr := scan(&id); scanErr != nil {
			return scanErr
		}
		ids = append(ids, id)
		return nil
	})
	return ids, err
}

func yugabyteLocalExecutor(sshPool *fwssh.Pool, host inventory.Host) (*provisioner.SSHExecutor, error) {
	if sshPool == nil {
		return nil, fmt.Errorf("ssh pool is nil")
	}
	runner, err := sshPool.Get(&fwssh.ConnectionConfig{
		Address:  host.ExternalIP,
		Port:     22,
		User:     host.User,
		HostName: host.Name,
		Timeout:  30 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("ssh connect %s: %w", host.Name, err)
	}
	return &provisioner.SSHExecutor{Runner: runner, UseYugabyteTools: true}, nil
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
