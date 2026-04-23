package cmd

import (
	"fmt"

	"frameworks/cli/pkg/inventory"
)

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
