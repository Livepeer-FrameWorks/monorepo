package cmd

import (
	"fmt"
	"os"

	internalconfig "frameworks/cli/internal/config"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"
)

// newSQLExecutor builds a SQLExecutor based on the manifest's sql_access setting.
// When sqlAccess is "ssh", an SSHExecutor is created using the host's Runner.
// For YugabyteDB the binary path is set to /opt/yugabyte/bin/ysqlsh.
// password is optional; used for TCP auth mode (YugabyteDB with ysql_enable_auth).
func newSQLExecutor(sqlAccess string, host inventory.Host, pool *ssh.Pool, isYugabyte bool, password string) (provisioner.SQLExecutor, error) {
	if sqlAccess != "ssh" {
		return &provisioner.DirectExecutor{}, nil
	}

	runner, err := getRunner(host, pool)
	if err != nil {
		return nil, fmt.Errorf("get ssh runner for sql executor: %w", err)
	}

	binaryPath := "psql"
	if isYugabyte {
		binaryPath = "/opt/yugabyte/bin/ysqlsh"
	}

	return &provisioner.SSHExecutor{
		Runner:      runner,
		BinaryPath:  binaryPath,
		UsePeerAuth: !isYugabyte,
		Password:    password,
	}, nil
}

// newCHExecutor builds a CHExecutor based on the manifest's sql_access setting.
func newCHExecutor(sqlAccess string, host inventory.Host, pool *ssh.Pool) (provisioner.CHExecutor, error) {
	if sqlAccess != "ssh" {
		return &provisioner.DirectCHExecutor{}, nil
	}

	runner, err := getRunner(host, pool)
	if err != nil {
		return nil, fmt.Errorf("get ssh runner for ch executor: %w", err)
	}

	return &provisioner.SSHCHExecutor{Runner: runner}, nil
}

// resolveYugabytePassword resolves the Yugabyte superuser password from
// manifest config, YUGABYTE_PASSWORD env var, or .env file (in that order).
// Returns "" for vanilla Postgres (uses peer auth, not passwords).
func resolveYugabytePassword(pg *inventory.PostgresConfig) string {
	if !pg.IsYugabyte() {
		return ""
	}
	if pg.Password != "" {
		return pg.Password
	}
	if v := os.Getenv("YUGABYTE_PASSWORD"); v != "" {
		return v
	}
	if envMap, err := internalconfig.LoadEnvFile(); err == nil {
		if v := envMap["YUGABYTE_PASSWORD"]; v != "" {
			return v
		}
	}
	return ""
}

// getRunner is defined in cluster_backup.go
