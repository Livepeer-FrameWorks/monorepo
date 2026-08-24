package cmd

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"
	pkgdatabase "github.com/Livepeer-FrameWorks/monorepo/pkg/database"

	"github.com/lib/pq"
)

func doctorPostgresCapabilities(
	ctx context.Context,
	sshPool *ssh.Pool,
	manifest *inventory.Manifest,
	host inventory.Host,
	password string,
) *health.CheckResult {
	result := &health.CheckResult{
		Name:      "postgres_capabilities",
		CheckedAt: time.Now(),
		Metadata:  map[string]string{"check_kind": "live_capabilities"},
	}
	pg := manifest.Infrastructure.Postgres
	if pg == nil || !pg.Enabled {
		result.OK, result.Status, result.Message = true, "healthy", "postgres not enabled"
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

	serviceNames := make([]string, 0, len(manifest.Services))
	for name := range manifest.Services {
		serviceNames = append(serviceNames, name)
	}
	sort.Strings(serviceNames)
	checked := 0
	for _, serviceName := range serviceNames {
		svc := manifest.Services[serviceName]
		if !svc.Enabled {
			continue
		}
		deploy := strings.TrimSpace(svc.Deploy)
		if deploy == "" {
			deploy = serviceName
		}
		capabilities := pkgdatabase.CapabilitiesFor(deploy, pkgdatabase.EnginePostgres)
		if len(capabilities) == 0 {
			continue
		}
		task := &orchestrator.Task{ServiceID: serviceName, Type: deploy, Host: svc.Host, ClusterID: svc.Cluster}
		_, db, ok := declaredPostgresDatabaseForService(task, manifest, map[string]string{})
		if !ok {
			result.Status = "unhealthy"
			result.Error = fmt.Sprintf("%s declares PostgreSQL capabilities but no physical database resolves from the manifest", serviceName)
			return result
		}
		conn := provisioner.ConnParams{Port: pg.EffectivePort(), User: user, Database: db.Name}
		runtimeRole := databaseRuntimeRole(db)
		for _, capability := range capabilities {
			probe := fmt.Sprintf("SET ROLE %s; %s; RESET ROLE;", pq.QuoteIdentifier(runtimeRole), capability.Probe)
			probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			err := executor.Exec(probeCtx, conn, probe)
			cancel()
			if err != nil {
				result.Status = "unhealthy"
				result.Error = (&pkgdatabase.CapabilityError{Service: serviceName, Capability: capability.Name, Engine: capability.Engine, Err: err}).Error()
				result.Metadata["database"] = db.Name
				result.Metadata["runtime_role"] = runtimeRole
				return result
			}
			checked++
		}
	}
	result.OK, result.Status = true, "healthy"
	result.Message = fmt.Sprintf("%d PostgreSQL/Yugabyte runtime capabilities executable", checked)
	result.Metadata["capabilities"] = fmt.Sprintf("%d", checked)
	return result
}

func doctorClickHouseCapabilities(
	ctx context.Context,
	sshPool *ssh.Pool,
	manifest *inventory.Manifest,
	sharedEnv map[string]string,
) *health.CheckResult {
	result := &health.CheckResult{
		Name:      "clickhouse_capabilities",
		CheckedAt: time.Now(),
		Metadata:  map[string]string{"check_kind": "live_capabilities"},
	}
	ch := manifest.Infrastructure.ClickHouse
	if ch == nil || !ch.Enabled {
		result.OK, result.Status, result.Message = true, "healthy", "clickhouse not enabled"
		return result
	}
	host, ok := manifest.GetHost(ch.CoordinatorHost())
	if !ok {
		result.Status, result.Error = "degraded", fmt.Sprintf("clickhouse coordinator host %q not found", ch.CoordinatorHost())
		return result
	}
	runner, err := sshPool.Get(&ssh.ConnectionConfig{
		Address: host.ExternalIP, Port: 22, User: host.User, HostName: host.Name, Timeout: 30 * time.Second,
	})
	if err != nil {
		result.Status, result.Error = "degraded", fmt.Sprintf("ssh connect: %v", err)
		return result
	}
	executor := &provisioner.SSHCHExecutor{Runner: runner}
	user := strings.TrimSpace(sharedEnv["CLICKHOUSE_USER"])
	if user == "" {
		user = "default"
	}
	password := sharedEnv["CLICKHOUSE_PASSWORD"]
	serviceNames := make([]string, 0, len(manifest.Services))
	for name := range manifest.Services {
		serviceNames = append(serviceNames, name)
	}
	sort.Strings(serviceNames)
	checked := 0
	for _, serviceName := range serviceNames {
		svc := manifest.Services[serviceName]
		if !svc.Enabled {
			continue
		}
		deploy := strings.TrimSpace(svc.Deploy)
		if deploy == "" {
			deploy = serviceName
		}
		for _, capability := range pkgdatabase.CapabilitiesFor(deploy, pkgdatabase.EngineClickHouse) {
			probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			err := executor.Exec(probeCtx, "localhost", ch.EffectivePort(), user, password, "periscope", capability.Probe)
			cancel()
			if err != nil {
				result.Status = "unhealthy"
				result.Error = (&pkgdatabase.CapabilityError{Service: serviceName, Capability: capability.Name, Engine: capability.Engine, Err: err}).Error()
				return result
			}
			checked++
		}
	}
	result.OK, result.Status = true, "healthy"
	result.Message = fmt.Sprintf("%d ClickHouse runtime capabilities executable", checked)
	result.Metadata["capabilities"] = fmt.Sprintf("%d", checked)
	return result
}
