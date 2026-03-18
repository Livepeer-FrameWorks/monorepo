package provisioner

import (
	"context"
	"fmt"

	dbsql "frameworks/pkg/database/sql"

	"github.com/lib/pq"
)

// schemaFiles maps database names to their embedded SQL schema paths.
var schemaFiles = map[string]string{
	"quartermaster": "schema/quartermaster.sql",
	"commodore":     "schema/commodore.sql",
	"foghorn":       "schema/foghorn.sql",
	"periscope":     "schema/periscope.sql",
	"purser":        "schema/purser.sql",
	"listmonk":      "schema/listmonk.sql",
	"navigator":     "schema/navigator.sql",
	"skipper":       "schema/skipper.sql",
}

// staticSeeds maps database names to their static seed file paths.
var staticSeeds = map[string]string{
	"purser": "seeds/static/purser_tiers.sql",
}

// demoSeeds maps database names to their demo seed file paths.
var demoSeeds = map[string]string{
	"quartermaster": "seeds/demo/demo_data.sql",
}

// pgInitializeDatabase applies the embedded schema and static seeds for a database.
func pgInitializeDatabase(ctx context.Context, exec SQLExecutor, conn ConnParams, dbName string) error {
	schemaFile, ok := schemaFiles[dbName]
	if !ok {
		fmt.Printf("No dedicated schema file found for %s, skipping schema step.\n", dbName)
		return nil
	}

	fmt.Printf("Applying schema for %s...\n", dbName)
	if err := execEmbeddedSQL(ctx, exec, conn, schemaFile); err != nil {
		return fmt.Errorf("failed to apply schema for %s: %w", dbName, err)
	}

	if seedFile, ok := staticSeeds[dbName]; ok {
		fmt.Printf("Applying static seeds for %s...\n", dbName)
		if err := execEmbeddedSQL(ctx, exec, conn, seedFile); err != nil {
			return fmt.Errorf("failed to apply static seeds for %s: %w", dbName, err)
		}
	}

	fmt.Printf("✓ Database %s initialized\n", dbName)
	return nil
}

// pgCreateApplicationUser creates a Postgres/YSQL role and grants privileges.
// Idempotent: creates if missing, rotates the password on re-provision.
func pgCreateApplicationUser(ctx context.Context, exec SQLExecutor, conn ConnParams, user, password string, databases []string) error {
	var exists bool
	if err := exec.QueryRow(ctx, conn, "SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname = $1)", []any{user}, &exists); err != nil {
		return fmt.Errorf("check role existence: %w", err)
	}

	quotedUser := pq.QuoteIdentifier(user)
	if !exists {
		if err := exec.Exec(ctx, conn, fmt.Sprintf("CREATE ROLE %s WITH LOGIN", quotedUser)); err != nil {
			return fmt.Errorf("create role %s: %w", user, err)
		}
		fmt.Printf("Created role: %s\n", user)
	}

	if err := exec.Exec(ctx, conn, fmt.Sprintf("ALTER ROLE %s WITH PASSWORD %s", quotedUser, pq.QuoteLiteral(password))); err != nil {
		return fmt.Errorf("set password for %s: %w", user, err)
	}

	for _, dbName := range databases {
		quotedDB := pq.QuoteIdentifier(dbName)
		if err := exec.Exec(ctx, conn, fmt.Sprintf("GRANT ALL PRIVILEGES ON DATABASE %s TO %s", quotedDB, quotedUser)); err != nil {
			return fmt.Errorf("grant on %s: %w", dbName, err)
		}

		dbConn := ConnParams{Host: conn.Host, Port: conn.Port, User: conn.User, Database: dbName}
		schemaGrants := fmt.Sprintf(
			"GRANT USAGE ON SCHEMA public TO %[1]s;\n"+
				"GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO %[1]s;\n"+
				"GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public TO %[1]s;\n"+
				"ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL PRIVILEGES ON TABLES TO %[1]s;\n"+
				"ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL PRIVILEGES ON SEQUENCES TO %[1]s;",
			quotedUser,
		)
		if err := exec.Exec(ctx, dbConn, schemaGrants); err != nil {
			return fmt.Errorf("schema grant on %s: %w", dbName, err)
		}
	}

	fmt.Printf("✓ Role %s ready (grants on %d databases)\n", user, len(databases))
	return nil
}

// execEmbeddedSQL reads an SQL file from the embedded filesystem and executes it.
func execEmbeddedSQL(ctx context.Context, exec SQLExecutor, conn ConnParams, relativePath string) error {
	sqlContent, err := dbsql.Content.ReadFile(relativePath)
	if err != nil {
		return fmt.Errorf("failed to read embedded SQL file %s: %w", relativePath, err)
	}
	return exec.Exec(ctx, conn, string(sqlContent))
}
