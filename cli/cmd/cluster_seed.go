package cmd

import (
	"context"
	"fmt"
	"time"

	"frameworks/cli/internal/ux"
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

	seedMode := "static"
	if demo {
		seedMode = "static + demo"
	}
	ux.Heading(cmd.OutOrStdout(), fmt.Sprintf("Seeding cluster (%s)", seedMode))

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

	sshKey := stringFlag(cmd, "ssh-key").Value
	sshPool := ssh.NewPool(30*time.Second, sshKey)
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

		var dbNames []string
		for _, d := range pg.Databases {
			dbNames = append(dbNames, d.Name)
		}

		serviceName := "postgres"
		itemsKey := "postgres_seed_items"
		if pg.IsYugabyte() {
			serviceName = "yugabyte"
			itemsKey = "yugabyte_seed_items"
		}

		runSeed := func(kind, label string) error {
			items, err := provisioner.BuildPostgresSeedItems(kind, dbNames)
			if err != nil {
				return fmt.Errorf("%s %s seeds: %w", serviceName, kind, err)
			}
			if len(items) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "  No %s seeds apply for this manifest's databases\n", kind)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Applying %s %s seeds...\n", serviceName, kind)
			cfg := provisioner.ServiceConfig{
				Port: pg.EffectivePort(),
				Metadata: map[string]any{
					"postgres_password": password,
					itemsKey:            items,
				},
			}
			prov, provErr := provisioner.GetProvisioner(serviceName, sshPool)
			if provErr != nil {
				return provErr
			}
			seeder, ok := prov.(provisioner.Seeder)
			if !ok {
				return fmt.Errorf("%s provisioner does not implement Seeder", serviceName)
			}
			if err := seeder.ApplySeeds(ctx, pgHost, cfg); err != nil {
				return fmt.Errorf("%s %s seeds: %w", serviceName, kind, err)
			}
			ux.Success(cmd.OutOrStdout(), label+" applied")
			return nil
		}

		if err := runSeed("static", "Static seeds"); err != nil {
			return err
		}
		if demo {
			if err := runSeed("demo", "Demo seeds"); err != nil {
				return err
			}
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

				chPort := ch.Port
				if chPort == 0 {
					chPort = 9000
				}

				items, itemsErr := provisioner.BuildClickHouseDemoSeedItems()
				if itemsErr != nil {
					return fmt.Errorf("clickhouse demo seeds: %w", itemsErr)
				}
				chCfg := provisioner.ServiceConfig{
					Port: chPort,
					Metadata: map[string]any{
						"clickhouse_password":   chPassword,
						"clickhouse_seed_items": items,
					},
				}
				chProv, chErr := provisioner.GetProvisioner("clickhouse", sshPool)
				if chErr != nil {
					return chErr
				}
				seeder, ok := chProv.(provisioner.Seeder)
				if !ok {
					return fmt.Errorf("clickhouse provisioner does not implement Seeder")
				}

				fmt.Fprintln(cmd.OutOrStdout(), "Applying ClickHouse demo seeds...")
				if err := seeder.ApplySeeds(ctx, chHost, chCfg); err != nil {
					return fmt.Errorf("clickhouse demo seeds: %w", err)
				}
				ux.Success(cmd.OutOrStdout(), "ClickHouse demo seeds applied")
			}
		}
	}

	out := cmd.OutOrStdout()
	ux.Success(out, "Seed complete")
	ux.PrintNextSteps(out, []ux.NextStep{
		{Cmd: "frameworks cluster doctor", Why: "Verify the cluster is healthy after seeding."},
	})
	return nil
}
