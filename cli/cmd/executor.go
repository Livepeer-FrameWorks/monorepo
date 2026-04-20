package cmd

import (
	"fmt"

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

// resolveYugabytePassword resolves the Yugabyte superuser password in
// priority order: manifest pg.Password (yaml) → sharedEnv["DATABASE_PASSWORD"]
// (gitops env_files, matching provision's extractInfraCredentials convention).
// Returns ("", nil) for vanilla Postgres (uses peer auth, not passwords).
// Returns a clear error for Yugabyte when neither source provides the secret
// — mirrors the GeoIP/ClickHouse fail-fast pattern so operators get the same
// "add it to your gitops secrets" guidance instead of a downstream SQL auth
// failure. Does not read process env: platform secrets live in gitops.
func resolveYugabytePassword(pg *inventory.PostgresConfig, sharedEnv map[string]string) (string, error) {
	if !pg.IsYugabyte() {
		return "", nil
	}
	if pg.Password != "" {
		return pg.Password, nil
	}
	if sharedEnv != nil {
		if pw := sharedEnv["DATABASE_PASSWORD"]; pw != "" {
			return pw, nil
		}
	}
	return "", fmt.Errorf("DATABASE_PASSWORD missing from manifest env_files — add it to your gitops secrets (or set postgres.password in the manifest)")
}

// getRunner is defined in cluster_backup.go
