package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	internalconfig "frameworks/cli/internal/config"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

func newClusterSeedCmd() *cobra.Command {
	var (
		manifestPath string
		demo         bool
		force        bool
	)

	cmd := &cobra.Command{
		Use:   "seed",
		Short: "Load seed data into databases",
		Long: `Load seed data into cluster databases.

By default, only static seeds (production reference data like billing tiers)
are applied. Use --demo to also apply demo data (sample tenant, user, stream)
for development and testing.

Seed operations are idempotent (ON CONFLICT guards).`,
		Example: `  # Apply demo data for development
  frameworks cluster seed --demo --manifest cluster.yaml

  # Re-apply static seeds after schema changes
  frameworks cluster seed --manifest cluster.yaml`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSeed(cmd, manifestPath, demo, force)
		},
	}

	cmd.Flags().StringVar(&manifestPath, "manifest", "cluster.yaml", "Path to cluster manifest file")
	cmd.Flags().BoolVar(&demo, "demo", false, "Include demo data (sample tenant, user, stream)")
	cmd.Flags().BoolVar(&force, "force", false, "Skip confirmation prompt")

	return cmd
}

func runSeed(cmd *cobra.Command, manifestPath string, demo, force bool) error {
	manifest, err := inventory.Load(manifestPath)
	if err != nil {
		return fmt.Errorf("failed to load manifest: %w", err)
	}

	if demo && !force {
		fmt.Fprint(cmd.OutOrStdout(), "This will insert demo data (sample tenant, user, stream). Continue? [y/N] ")
		var answer string
		if _, err := fmt.Scanln(&answer); err != nil {
			return fmt.Errorf("reading input: %w", err)
		}
		if answer != "y" && answer != "Y" {
			fmt.Fprintln(cmd.OutOrStdout(), "Aborted.")
			return nil
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	sshPool := ssh.NewPool(30 * time.Second)
	defer sshPool.Close()

	// Postgres seeds
	pg := manifest.Infrastructure.Postgres
	if pg != nil && pg.Enabled {
		var pgHost inventory.Host
		var ok bool
		if pg.IsYugabyte() && len(pg.Nodes) > 0 {
			pgHost, ok = manifest.GetHost(pg.Nodes[0].Host)
		} else {
			pgHost, ok = manifest.GetHost(pg.Host)
		}
		if !ok {
			return fmt.Errorf("postgres host not found in manifest")
		}

		pgProv, provErr := provisioner.NewPostgresProvisioner(sshPool)
		if provErr != nil {
			return fmt.Errorf("create postgres provisioner: %w", provErr)
		}
		port := pg.EffectivePort()
		dbUser := "postgres"
		if pg.IsYugabyte() {
			dbUser = "yugabyte"
		}

		// Build database name list from manifest
		var dbNames []string
		for _, d := range pg.Databases {
			dbNames = append(dbNames, d.Name)
		}

		// Static seeds are always applied
		fmt.Fprintln(cmd.OutOrStdout(), "Applying Postgres static seeds...")
		if err := pgProv.ApplyStaticSeeds(ctx, pgHost, port, dbUser, dbNames); err != nil {
			return fmt.Errorf("postgres static seeds: %w", err)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "  ✓ Static seeds applied")

		if demo {
			fmt.Fprintln(cmd.OutOrStdout(), "Applying Postgres demo seeds...")
			if err := pgProv.ApplyDemoSeeds(ctx, pgHost, port, dbUser, dbNames); err != nil {
				return fmt.Errorf("postgres demo seeds: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "  ✓ Demo seeds applied")
		}
	}

	// ClickHouse demo seeds
	if demo {
		ch := manifest.Infrastructure.ClickHouse
		if ch != nil && ch.Enabled {
			chHost, ok := manifest.GetHost(ch.Host)
			if !ok {
				return fmt.Errorf("clickhouse host not found in manifest")
			}

			chProv, chErr := provisioner.NewClickHouseProvisioner(sshPool)
			if chErr != nil {
				return fmt.Errorf("create clickhouse provisioner: %w", chErr)
			}
			chPort := ch.Port
			if chPort == 0 {
				chPort = 9000
			}
			// Resolve ClickHouse password: env file (same as provisioning), then os env.
			chPassword := os.Getenv("CLICKHOUSE_PASSWORD")
			if chPassword == "" {
				if envMap, envErr := internalconfig.LoadEnvFile(); envErr == nil {
					chPassword = envMap["CLICKHOUSE_PASSWORD"]
				}
			}
			config := provisioner.ServiceConfig{
				Port:     chPort,
				Metadata: map[string]interface{}{"clickhouse_password": chPassword},
			}

			// Only seed if periscope is in the manifest's database list
			chDBs := ch.Databases
			if len(chDBs) == 0 {
				chDBs = []string{"periscope"}
			}
			hasPeriscope := false
			for _, d := range chDBs {
				if d == "periscope" {
					hasPeriscope = true
					break
				}
			}
			if hasPeriscope {
				fmt.Fprintln(cmd.OutOrStdout(), "Applying ClickHouse demo seeds...")
				if err := chProv.ApplyDemoSeeds(ctx, chHost, config); err != nil {
					return fmt.Errorf("clickhouse demo seeds: %w", err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), "  ✓ ClickHouse demo seeds applied")
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "  Skipping ClickHouse demo seeds (periscope not in manifest)")
			}
		}
	}

	fmt.Fprintln(cmd.OutOrStdout(), "\n✓ Seed complete!")
	return nil
}
