package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

// engine is the Yugabyte engine scripts/run-yugabyte-contract-fixture.sh started, with the baselines it rendered
// through the release code path.
type engine struct {
	adminDSN  string
	container string
	baselines string
}

func engineFromEnv() (*engine, error) {
	e := &engine{
		adminDSN:  strings.TrimSpace(os.Getenv("FRAMEWORKS_YUGABYTE_TEST_DSN")),
		container: strings.TrimSpace(os.Getenv("FRAMEWORKS_YUGABYTE_TEST_CONTAINER")),
		baselines: strings.TrimSpace(os.Getenv("FRAMEWORKS_YUGABYTE_TEST_BASELINES")),
	}
	if e.adminDSN == "" || e.container == "" || e.baselines == "" {
		return nil, fmt.Errorf("run through scripts/run-yugabyte-contract-fixture.sh (make verify-yugabyte-explain-audit): FRAMEWORKS_YUGABYTE_TEST_DSN, _CONTAINER and _BASELINES are required")
	}
	return e, nil
}

// declaredColocated reports the placement the release creates database with.
func (e *engine) declaredColocated(database string) (bool, error) {
	layout, err := os.ReadFile(filepath.Join(e.baselines, database+".layout"))
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(layout)) == "colocated", nil
}

func (e *engine) admin(ctx context.Context, statement string) error {
	db, err := sql.Open("postgres", e.adminDSN)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, statement)
	return err
}

// createDatabase creates name, colocated or not, applies the rendered baseline one statement at a time through
// ysqlsh exactly as the shared contract fixture does, and returns a connection pool to it.
func (e *engine) createDatabase(database, name string, colocated bool) (*sql.DB, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := e.dropDatabase(name); err != nil {
		return nil, err
	}
	statement := "CREATE DATABASE " + name
	if colocated {
		statement += " WITH COLOCATION = true"
	}
	if err := e.admin(ctx, statement); err != nil {
		return nil, fmt.Errorf("create database %s: %w", name, err)
	}
	baseline, err := os.ReadFile(filepath.Join(e.baselines, database+".sql"))
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", e.container, "ysqlsh", "-X", "-h", e.container, "-U", "yugabyte",
		"-d", name, "-v", "ON_ERROR_STOP=1", "-q")
	cmd.Stdin = bytes.NewReader(baseline)
	if out, runErr := cmd.CombinedOutput(); runErr != nil {
		return nil, fmt.Errorf("apply %s baseline: %w\n%s", database, runErr, out)
	}
	parsed, err := url.Parse(e.adminDSN)
	if err != nil {
		return nil, err
	}
	parsed.Path = "/" + name
	db, err := sql.Open("postgres", parsed.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	return db, db.PingContext(ctx)
}

func (e *engine) dropDatabase(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := e.admin(ctx, fmt.Sprintf("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%s' AND pid <> pg_backend_pid()", name)); err != nil {
		return err
	}
	return e.admin(ctx, "DROP DATABASE IF EXISTS "+name)
}
