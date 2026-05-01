package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"frameworks/cli/internal/releases"
	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/preflight"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"
	"frameworks/pkg/datamigrate"
)

// doctorPostgresMigrations validates the _migrations LEDGER (not the live
// schema) using the same access path the prod migration role uses: Unix
// socket via SSH+psql for vanilla pg, TCP+password for Yugabyte.
//
// "Ledger consistent" / "ledger missing N entries" wording is deliberate —
// this check does NOT detect schema drift.
func doctorPostgresMigrations(
	ctx context.Context,
	sshPool *ssh.Pool,
	manifest *inventory.Manifest,
	host inventory.Host,
	password, targetVersion string,
) *health.CheckResult {
	result := &health.CheckResult{
		Name:      "postgres_migrations",
		CheckedAt: time.Now(),
		Metadata:  map[string]string{"check_kind": "ledger"},
	}

	pg := manifest.Infrastructure.Postgres
	if pg == nil || !pg.Enabled {
		result.OK = true
		result.Status = "healthy"
		result.Message = "postgres not enabled"
		return result
	}

	if pg.IsYugabyte() && password == "" {
		result.OK = false
		result.Status = "degraded"
		result.Error = "Yugabyte ledger check requires DATABASE_PASSWORD; rerun with --deep or set postgres.password"
		return result
	}

	if targetVersion == "" {
		result.OK = false
		result.Status = "degraded"
		result.Error = "cannot resolve target version from cluster channel; pin a release manifest or set channel"
		return result
	}

	dbNames := postgresDatabaseNames(manifest)
	if len(dbNames) == 0 {
		result.OK = true
		result.Status = "healthy"
		result.Message = "no databases configured in manifest"
		return result
	}

	var (
		totalMissing  int
		totalMismatch int
		details       []string
	)
	for _, phase := range []string{"expand", "postdeploy"} {
		missing, err := provisioner.MissingMigrations(ctx, sshPool, host, pg, password, dbNames, phase, targetVersion)
		if err != nil {
			result.OK = false
			result.Status = "degraded"
			result.Error = fmt.Sprintf("ledger read failed (%s): %v", phase, err)
			return result
		}
		for _, m := range missing {
			if m.MismatchedChecksum != "" {
				totalMismatch++
			} else {
				totalMissing++
			}
			details = append(details, fmt.Sprintf("%s: %s", phase, m.String()))
		}
	}
	var pendingContract int
	contractMissing, err := provisioner.MissingMigrations(ctx, sshPool, host, pg, password, dbNames, "contract", targetVersion)
	if err != nil {
		result.OK = false
		result.Status = "degraded"
		result.Error = fmt.Sprintf("ledger read failed (contract): %v", err)
		return result
	}
	for _, m := range contractMissing {
		if m.MismatchedChecksum != "" {
			totalMismatch++
			details = append(details, fmt.Sprintf("contract: %s", m.String()))
			continue
		}
		pendingContract++
	}

	result.Metadata["missing"] = fmt.Sprintf("%d", totalMissing)
	result.Metadata["checksum_mismatches"] = fmt.Sprintf("%d", totalMismatch)
	result.Metadata["pending_contract"] = fmt.Sprintf("%d", pendingContract)
	result.Metadata["target_version"] = targetVersion

	if totalMismatch > 0 {
		result.OK = false
		result.Status = "unhealthy"
		result.Error = fmt.Sprintf("%d ledger checksum mismatch(es) — applied migration was edited after the fact", totalMismatch)
		result.Metadata["details"] = strings.Join(details, "; ")
		return result
	}
	if totalMissing > 0 {
		result.OK = false
		result.Status = "degraded"
		result.Error = fmt.Sprintf("%d migration(s) missing from ledger up to %s", totalMissing, targetVersion)
		result.Metadata["details"] = strings.Join(details, "; ")
		return result
	}

	result.OK = true
	result.Status = "healthy"
	if pendingContract > 0 {
		result.Message = fmt.Sprintf("required ledger consistent up to %s; %d contract migration(s) pending explicit cleanup", targetVersion, pendingContract)
	} else {
		result.Message = fmt.Sprintf("ledger consistent up to %s", targetVersion)
	}
	return result
}

// postgresDoctorUser returns the local DB user the doctor authenticates as
// when probing the live cluster. Vanilla pg uses peer auth as `postgres`;
// Yugabyte uses the `yugabyte` superuser.
func postgresDoctorUser(pg *inventory.PostgresConfig) string {
	if pg != nil && pg.IsYugabyte() {
		return "yugabyte"
	}
	return "postgres"
}

// doctorDataMigrations is the sibling check that surfaces incomplete prior
// data migrations. Same fail-closed semantics as the upgrade gate: an
// unreportable required migration is degraded, never silently passing.
func doctorDataMigrations(
	ctx context.Context,
	sshPool *ssh.Pool,
	manifest *inventory.Manifest,
	currentVersion, targetVersion string,
) *health.CheckResult {
	result := &health.CheckResult{
		Name:      "data_migration_state",
		CheckedAt: time.Now(),
		Metadata:  map[string]string{},
	}

	if targetVersion == "" {
		result.OK = false
		result.Status = "degraded"
		result.Error = "cannot resolve target version; data-migration check skipped"
		return result
	}

	catalog := releases.Catalog()
	if len(catalog) == 0 {
		result.OK = true
		result.Status = "healthy"
		result.Message = "release catalog is empty; no required data migrations declared"
		return result
	}
	reqs := preflight.CatalogRequirements(catalog, targetVersion)
	if len(reqs) == 0 {
		result.OK = true
		result.Status = "healthy"
		result.Message = fmt.Sprintf("no required data migrations declared up to %s", targetVersion)
		return result
	}

	src := preflight.SSHStateSource(sshPool, manifestHostFor(manifest), manifestRuntimeFor(manifest))
	blockers, err := datamigrate.PreDeployBlockers(ctx, src, reqs, currentVersion, targetVersion, releases.CompareSemver)
	if err != nil {
		result.OK = false
		result.Status = "degraded"
		result.Error = fmt.Sprintf("data-migration check failed: %v", err)
		return result
	}
	result.Metadata["required"] = fmt.Sprintf("%d", len(reqs))
	result.Metadata["blockers"] = fmt.Sprintf("%d", len(blockers))

	if len(blockers) > 0 {
		result.OK = false
		result.Status = "degraded"
		var lines []string
		for _, b := range blockers {
			lines = append(lines, fmt.Sprintf("%s/%s: %s", b.Requirement.Service, b.Requirement.ID, b.Reason))
		}
		result.Error = fmt.Sprintf("%d required data migration(s) blocked", len(blockers))
		result.Metadata["details"] = strings.Join(lines, "; ")
		return result
	}
	result.OK = true
	result.Status = "healthy"
	result.Message = fmt.Sprintf("%d required data migration(s) completed", len(reqs))
	return result
}
