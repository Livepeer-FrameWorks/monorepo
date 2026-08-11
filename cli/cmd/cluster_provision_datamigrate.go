package cmd

import (
	"context"
	"fmt"

	"frameworks/cli/internal/releases"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/preflight"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
	"github.com/spf13/cobra"
)

// dbMigrationState is the per-service durable evidence the provision data-migration gate reads once: the
// `_schema_baseline` floor each owning physical database was BORN at, and the `_data_migrations` completion ledger.
// hasDB is false when the service owns no catalog database (fail closed — its state cannot be verified).
type dbMigrationState struct {
	floors  map[string]string
	ledgers map[string]provisioner.DataMigrationLedger
	hasDB   bool
}

// runProvisionDataMigrationPreflight is the release-level data-migration gate for `cluster provision`. It runs AFTER
// the in-cell database has been initialized (its baseline schema applied) and BEFORE applications deploy, so it can read
// durable evidence from the database itself rather than infer freshness from engine/database absence. Fresh-vs-preserved
// is decided by `_schema_baseline` provenance (mirroring the schema below-floor guard): a data migration introduced
// strictly below the owning database's baseline floor was FOLDED into the baseline the database was born from, so a
// baseline-born (fresh) database is exempt from it; every other required migration must be completed in `_data_migrations`
// on the (preserved) database. datamigrate.PreDeployBlockers still applies the release-window selection to the survivors.
// A catalog with no declared data migrations (the case today) is a no-op.
//
// dbInfraPending reports that a database-infrastructure task is scheduled in the same or a later batch than the point
// this gate runs (PhaseAll). Because the planner only orders a DB-backed application behind its OWN database task, a
// manifest could co-schedule a DB-less application with unfinished database infrastructure. This fails closed LAZILY:
// the refusal lives inside the per-service state read, which PreDeployBlockers invokes only for an IN-WINDOW migration
// that genuinely needs database I/O — so a deploy whose in-window set is empty (all requirements target-line or
// not-yet-due, excluded by the window BEFORE any read) is never refused merely because the catalog is non-empty. NOTE:
// an in-window migration that WOULD be folded is NOT exempt from this refusal — folding is decided from the baseline
// floor, which is precisely the database read dbInfraPending precludes, so the gate fails closed rather than guess.
func runProvisionDataMigrationPreflight(ctx context.Context, cmd *cobra.Command, rc *resolvedCluster, sshPool *ssh.Pool, manifest *inventory.Manifest, targetVersion string, dbInfraPending bool) error {
	out := cmd.OutOrStdout()
	catalog, err := loadReleaseCatalog()
	if err != nil {
		return fmt.Errorf("[provision] embedded release catalog failed to load: %w; refusing (cannot verify prior data migrations)", err)
	}
	reqs := preflight.CatalogRequirements(catalog, targetVersion)
	if len(reqs) == 0 {
		fmt.Fprintf(out, "[provision] no required data migrations declared up to %s.\n", targetVersion)
		return nil
	}

	// Map (service, id) -> IntroducedIn so the state source can fold by baseline floor. The pre-deploy release WINDOW is
	// applied BEFORE any database I/O: datamigrate.PreDeployBlockers excludes target-line and not-yet-due requirements
	// and only invokes the state source (below) for those that can actually block this deployment, so a migration
	// irrelevant to this deploy never triggers a baseline/ledger read that could abort provisioning if its DB is down.
	introduced := make(map[string]string, len(reqs))
	for _, r := range reqs {
		introduced[dataMigrationKey(r.Service, r.ID)] = r.IntroducedIn
	}

	// One read per owning service, cached: baseline floors + completion ledgers from the initialized databases. Only
	// executed when the state source is invoked for an in-window requirement — so the dbInfraPending refusal below fires
	// ONLY when an in-window migration actually needs database I/O, never merely because catalog requirements exist.
	cache := map[string]dbMigrationState{}
	stateFor := func(service string) (dbMigrationState, error) {
		if s, ok := cache[service]; ok {
			return s, nil
		}
		if dbInfraPending {
			return dbMigrationState{}, fmt.Errorf("database infrastructure is scheduled in the same or a later batch as the first application, so database initialization is not guaranteed complete before this gate; order the database tasks ahead of applications (the planner should model DB init as an explicit barrier)")
		}
		dbName, ok := releases.ServiceDatabaseLookup(service)
		if !ok {
			s := dbMigrationState{hasDB: false}
			cache[service] = s
			return s, nil
		}
		floors, fErr := readProvisionBaselineFloors(ctx, rc, sshPool, manifest, dbName)
		if fErr != nil {
			return dbMigrationState{}, fErr
		}
		ledgers, lErr := readProvisionDataMigrationLedgers(ctx, rc, sshPool, manifest, dbName)
		if lErr != nil {
			return dbMigrationState{}, lErr
		}
		s := dbMigrationState{floors: floors, ledgers: ledgers, hasDB: true}
		cache[service] = s
		return s, nil
	}

	src := provisionDataMigrationStateSource(introduced, stateFor)
	blockers, err := datamigrate.PreDeployBlockers(ctx, src, reqs, targetVersion, releases.CompareSemver, releases.BaseVersion)
	if err != nil {
		return fmt.Errorf("[provision] check prior data migrations: %w", err)
	}
	if len(blockers) > 0 {
		return fmt.Errorf("[provision] required data migrations are incomplete on the preserved database(s) — refusing to deploy applications ahead of them:\n%s\n\nrun: frameworks cluster data-migrate run <id>", formatBlockers(blockers))
	}
	fmt.Fprintf(out, "[provision] required data migrations verified for %s (folded/out-of-window migrations skipped).\n", targetVersion)
	return nil
}

// dataMigrationKey is the stable lookup key for a (service, id) requirement (NUL-separated so neither field can spoof a
// boundary).
func dataMigrationKey(service, id string) string { return service + "\x00" + id }

// foldedBelowAllFloors reports whether introducedIn is STRICTLY BELOW the baseline floor of every owning physical
// database (all non-empty) — the canonical fold rule: a migration strictly below the floor a database was born at is
// folded into that baseline. It is NOT universally folded (and must still be checked against the ledger) when there is
// no owning database, any owning database has no marker ("" — an existing pre-baseline cluster), or introducedIn is not
// strictly below one of the owning floors.
func foldedBelowAllFloors(introducedIn string, floors map[string]string) bool {
	if len(floors) == 0 {
		return false
	}
	for _, floor := range floors {
		if floor == "" || releases.CompareSemver(introducedIn, floor) >= 0 {
			return false
		}
	}
	return true
}

// provisionDataMigrationStateSource builds a datamigrate.StateSource that reads per-service database state LAZILY (via
// stateFor) and derives a LiveStatus with dataMigrationLiveStatus. PreDeployBlockers invokes it only for in-window
// requirements, so stateFor — and its database I/O — never runs for a migration excluded by the release window.
func provisionDataMigrationStateSource(introduced map[string]string, stateFor func(service string) (dbMigrationState, error)) datamigrate.StateSource {
	return func(_ context.Context, service, id string) datamigrate.LiveStatus {
		s, err := stateFor(service)
		if err != nil {
			return datamigrate.LiveStatus{ID: id, Service: service, FetchError: err}
		}
		return dataMigrationLiveStatus(service, id, introduced[dataMigrationKey(service, id)], s)
	}
}

// dataMigrationLiveStatus classifies one in-window requirement against durable evidence: an unresolvable owning database
// is a FetchError (fail closed); a migration STRICTLY BELOW the owning databases' baseline floor(s) is folded into the
// baseline they were born from and reported StatusCompleted; otherwise it must be StatusCompleted in the
// `_data_migrations` ledger of every owning database, else its live status (which classify() blocks on) is returned.
func dataMigrationLiveStatus(service, id, introducedIn string, s dbMigrationState) datamigrate.LiveStatus {
	live := datamigrate.LiveStatus{ID: id, Service: service}
	if !s.hasDB {
		live.FetchError = fmt.Errorf("service %q owns no database in the catalog; cannot verify data-migration %q", service, id)
		return live
	}
	if foldedBelowAllFloors(introducedIn, s.floors) {
		live.Status = datamigrate.StatusCompleted // folded into the baseline the database was born from
		return live
	}
	for _, led := range s.ledgers {
		if led.Statuses[id] != string(datamigrate.StatusCompleted) {
			live.Status = datamigrate.Status(led.Statuses[id]) // "" or a non-completed status → classify() blocks
			return live
		}
	}
	live.Status = datamigrate.StatusCompleted
	return live
}

// readProvisionDataMigrationLedgers reads the _data_migrations completion state for a service's owning database over
// SSH, resolving the physical database(s) the same way the upgrade schema gate does (schemaDatabasesFromConfigs /
// yugabyteSchemaDatabases), so Yugabyte regional aliases map to the same ledger the gate checks.
func readProvisionDataMigrationLedgers(ctx context.Context, rc *resolvedCluster, sshPool *ssh.Pool, manifest *inventory.Manifest, dbName string) (map[string]provisioner.DataMigrationLedger, error) {
	pg := manifest.Infrastructure.Postgres
	if pg == nil || !pg.Enabled {
		return nil, fmt.Errorf("a data migration is declared but postgres is not enabled in the manifest; cannot verify its state")
	}
	dbHost, ok := resolvePGHost(manifest, pg)
	if !ok {
		return nil, fmt.Errorf("cannot resolve postgres host from manifest")
	}
	password, _ := resolveYugabytePassword(pg, sharedEnvForGate(rc)) //nolint:errcheck // a missing yugabyte password surfaces as a read error below
	return provisioner.ReadDataMigrationLedgers(ctx, sshPool, dbHost, pg, password, provisionOwningDatabases(manifest, pg, dbName))
}

// readProvisionBaselineFloors reads the `_schema_baseline` floor of a service's owning database(s), resolving the
// physical database(s) exactly as the ledger read does so floors and ledgers key off the same names.
func readProvisionBaselineFloors(ctx context.Context, rc *resolvedCluster, sshPool *ssh.Pool, manifest *inventory.Manifest, dbName string) (map[string]string, error) {
	pg := manifest.Infrastructure.Postgres
	if pg == nil || !pg.Enabled {
		return nil, fmt.Errorf("a data migration is declared but postgres is not enabled in the manifest; cannot read its baseline provenance")
	}
	dbHost, ok := resolvePGHost(manifest, pg)
	if !ok {
		return nil, fmt.Errorf("cannot resolve postgres host from manifest")
	}
	return provisioner.ReadPostgresBaselineFloors(ctx, sshPool, dbHost, pg, provisionOwningDatabases(manifest, pg, dbName))
}

// provisionOwningDatabases resolves the physical schema database(s) for a logical database name, matching the upgrade
// gate's mapping (Yugabyte regional aliases included).
func provisionOwningDatabases(manifest *inventory.Manifest, pg *inventory.PostgresConfig, dbName string) []provisioner.SchemaDatabase {
	if pg.IsYugabyte() {
		return yugabyteSchemaDatabases([]inventory.DatabaseConfig{{Name: dbName}}, manifest)
	}
	return schemaDatabasesFromConfigs([]inventory.DatabaseConfig{{Name: dbName}})
}
