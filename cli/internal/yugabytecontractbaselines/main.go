// Renders every platform service baseline as the release applies it to YugabyteDB, for the shared Yugabyte contract
// fixture (scripts/run-yugabyte-contract-fixture.sh). For each service database it writes <database>.sql, the baseline
// with the database's layout applied by the same code path the yugabyte role receives its schema items from, and
// <database>.layout, "colocated" or "distributed", the placement the release creates the database with. Not a
// user-facing CLI entrypoint.
//
// Run:
//
//	go run ./internal/yugabytecontractbaselines -out <directory>
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"frameworks/cli/internal/releases"
	"frameworks/cli/pkg/provisioner"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("yugabytecontractbaselines: %v", err)
	}
}

func run() error {
	out := flag.String("out", "", "directory to write <database>.sql and <database>.layout into (required)")
	flag.Parse()
	if *out == "" {
		return errors.New("-out is required")
	}
	if err := releases.LoadError(); err != nil {
		return fmt.Errorf("release catalog: %w", err)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	databases := make([]provisioner.SchemaDatabase, 0)
	for _, name := range releases.ServiceDatabaseNames() {
		databases = append(databases, provisioner.SchemaDatabase{Name: name})
	}
	items, cleanup, err := provisioner.BuildSchemaItemsForEngine(databases, provisioner.SQLEngineYugabyte)
	defer cleanup()
	if err != nil {
		return err
	}
	for _, item := range items {
		database, databaseOK := item["db"].(string)
		source, sourceOK := item["src"].(string)
		if !databaseOK || !sourceOK || database == "" || source == "" {
			return fmt.Errorf("schema item %v names no database or source", item)
		}
		baseline, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		colocated, err := provisioner.YugabyteDatabaseColocated(database)
		if err != nil {
			return err
		}
		placement := "distributed"
		if colocated {
			placement = "colocated"
		}
		if err := os.WriteFile(filepath.Join(*out, database+".sql"), baseline, 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(*out, database+".layout"), []byte(placement+"\n"), 0o644); err != nil {
			return err
		}
	}
	return nil
}
