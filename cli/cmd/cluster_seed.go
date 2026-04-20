package cmd

import (
	"context"
	"fmt"
	"time"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

func newClusterSeedCmd() *cobra.Command {
	var (
		demo  bool
		force bool
	)

	cmd := &cobra.Command{
		Use:   "seed",
		Short: "Load seed data into databases",
		Long: `Load seed data into cluster databases.

By default, only static seeds (production reference data like billing tiers)
are applied. Use --demo to also apply demo data (sample tenant, user, stream)
for development and testing.

Seed operations are idempotent (ON CONFLICT guards).`,
		Example: `  frameworks cluster seed --demo
  frameworks cluster seed`,
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			return runSeed(cmd, rc, demo, force)
		},
	}

	cmd.Flags().BoolVar(&demo, "demo", false, "Include demo data (sample tenant, user, stream)")
	cmd.Flags().BoolVar(&force, "force", false, "Skip confirmation prompt")

	return cmd
}

func runSeed(cmd *cobra.Command, rc *resolvedCluster, demo, force bool) error {
	manifest := rc.Manifest

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

		// Only decrypt manifest env_files when Yugabyte actually needs a
		// password. Vanilla Postgres uses peer auth.
		var sharedEnv map[string]string
		if pg.IsYugabyte() && pg.Password == "" {
			env, sErr := rc.SharedEnv()
			if sErr != nil {
				return fmt.Errorf("load manifest env_files: %w", sErr)
			}
			sharedEnv = env
		}
		password, pwErr := resolveYugabytePassword(pg, sharedEnv)
		if pwErr != nil {
			return pwErr
		}
		sqlExec, execErr := newSQLExecutor(pg.SQLAccess, pgHost, sshPool, pg.IsYugabyte(), password)
		if execErr != nil {
			return fmt.Errorf("create sql executor: %w", execErr)
		}

		pgProv, provErr := provisioner.NewPostgresProvisioner(sshPool, provisioner.WithSQLExecutor(sqlExec))
		if provErr != nil {
			return fmt.Errorf("create postgres provisioner: %w", provErr)
		}
		port := pg.EffectivePort()
		dbUser := "postgres"
		if pg.IsYugabyte() {
			dbUser = "yugabyte"
		}

		var dbNames []string
		for _, d := range pg.Databases {
			dbNames = append(dbNames, d.Name)
		}

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
			if !hasPeriscope {
				fmt.Fprintln(cmd.OutOrStdout(), "  Skipping ClickHouse demo seeds (periscope not in manifest)")
			} else {
				// Only decrypt manifest env_files when we're actually going
				// to apply ClickHouse demo seeds.
				chEnv, envErr := rc.SharedEnv()
				if envErr != nil {
					return fmt.Errorf("load manifest env_files: %w", envErr)
				}
				chPassword := chEnv["CLICKHOUSE_PASSWORD"]
				if chPassword == "" {
					return fmt.Errorf("CLICKHOUSE_PASSWORD missing from manifest env_files — add it to your gitops secrets")
				}

				chExec, chExecErr := newCHExecutor(ch.SQLAccess, chHost, sshPool)
				if chExecErr != nil {
					return fmt.Errorf("create ch executor: %w", chExecErr)
				}
				chProv, chErr := provisioner.NewClickHouseProvisioner(sshPool, provisioner.WithCHExecutor(chExec))
				if chErr != nil {
					return fmt.Errorf("create clickhouse provisioner: %w", chErr)
				}
				chPort := ch.Port
				if chPort == 0 {
					chPort = 9000
				}
				config := provisioner.ServiceConfig{
					Port:     chPort,
					Metadata: map[string]any{"clickhouse_password": chPassword},
				}

				fmt.Fprintln(cmd.OutOrStdout(), "Applying ClickHouse demo seeds...")
				if err := chProv.ApplyDemoSeeds(ctx, chHost, config); err != nil {
					return fmt.Errorf("clickhouse demo seeds: %w", err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), "  ✓ ClickHouse demo seeds applied")
			}
		}
	}

	fmt.Fprintln(cmd.OutOrStdout(), "\n✓ Seed complete!")
	return nil
}
