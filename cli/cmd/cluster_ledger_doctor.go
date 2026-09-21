package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"
)

// purserLedgerCurrencyDoctorSQL counts Purser prepaid balance rows outside the
// uppercase EUR ledger, which admission, usage burn, and invoice credit never
// read, and the tenants that hold them.
const purserLedgerCurrencyDoctorSQL = `SELECT COUNT(*), COUNT(DISTINCT tenant_id) FROM purser.prepaid_balances WHERE currency <> 'EUR'`

// doctorPurserLedgerCurrency reports how many Purser prepaid balance rows sit
// outside the EUR ledger. It only reads; the
// purser_eur_ledger_conversion_v0_3_11 data migration converts them.
func doctorPurserLedgerCurrency(
	ctx context.Context,
	sshPool *ssh.Pool,
	manifest *inventory.Manifest,
	host inventory.Host,
	password string,
) *health.CheckResult {
	result := &health.CheckResult{
		Name:      "purser_ledger_currency",
		CheckedAt: time.Now(),
		Metadata:  map[string]string{"check_kind": "read_only_count"},
	}
	pg := manifest.Infrastructure.Postgres
	if pg == nil || !pg.Enabled {
		result.OK, result.Status, result.Message = true, "healthy", "postgres not enabled"
		return result
	}
	serviceName, svc, ok := purserServiceFor(manifest)
	if !ok {
		result.OK, result.Status, result.Message = true, "healthy", "purser not deployed"
		return result
	}
	task := &orchestrator.Task{ServiceID: serviceName, Type: "purser", Host: svc.Host, ClusterID: svc.Cluster}
	env := map[string]string{}
	applyCatalogPostgresDatabaseDefaults(task, env)
	_, db, ok := declaredPostgresDatabaseForService(task, manifest, env)
	if !ok {
		result.Status = "degraded"
		result.Error = "purser is deployed but no PostgreSQL database resolves from the manifest"
		return result
	}
	runner, err := sshPool.Get(&ssh.ConnectionConfig{
		Address: host.ExternalIP, Port: 22, User: host.User, HostName: host.Name, Timeout: 30 * time.Second,
	})
	if err != nil {
		result.Status, result.Error = "degraded", fmt.Sprintf("ssh connect: %v", err)
		return result
	}
	executor := &provisioner.SSHExecutor{Runner: runner, UsePeerAuth: !pg.IsYugabyte()}
	user := "postgres"
	if pg.IsYugabyte() {
		executor.BinaryPath = "/opt/yugabyte/bin/ysqlsh"
		executor.Password = password
		user = "yugabyte"
	}
	conn := provisioner.ConnParams{Port: pg.EffectivePort(), User: user, Database: db.Name}
	queryCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var rows, tenants int64
	if err := executor.QueryRow(queryCtx, conn, purserLedgerCurrencyDoctorSQL, nil, &rows, &tenants); err != nil {
		result.Status, result.Error = "degraded", fmt.Sprintf("count non-EUR prepaid balances: %v", err)
		return result
	}
	return purserLedgerCurrencyResult(result, rows, tenants)
}

func purserLedgerCurrencyResult(result *health.CheckResult, rows, tenants int64) *health.CheckResult {
	result.Metadata["non_eur_balance_rows"] = fmt.Sprintf("%d", rows)
	result.Metadata["tenants"] = fmt.Sprintf("%d", tenants)
	if rows == 0 {
		result.OK, result.Status = true, "healthy"
		result.Message = "every prepaid balance row is in the EUR ledger"
		return result
	}
	result.OK, result.Status = false, "degraded"
	result.Error = fmt.Sprintf("%d prepaid balance row(s) across %d tenant(s) are outside the EUR ledger; data migration purser_eur_ledger_conversion_v0_3_11 converts them", rows, tenants)
	return result
}

func purserServiceFor(manifest *inventory.Manifest) (string, inventory.ServiceConfig, bool) {
	for name, svc := range manifest.Services {
		deploy := strings.TrimSpace(svc.Deploy)
		if deploy == "" {
			deploy = name
		}
		if svc.Enabled && deploy == "purser" {
			return name, svc, true
		}
	}
	return "", inventory.ServiceConfig{}, false
}
