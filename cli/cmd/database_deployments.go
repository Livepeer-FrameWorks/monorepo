package cmd

import (
	"fmt"
	"net/url"
	"strings"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	"github.com/spf13/cobra"
)

func (rc *resolvedCluster) withDatabaseManifest(manifest *inventory.Manifest) *resolvedCluster {
	return &resolvedCluster{
		Manifest: manifest, ManifestPath: rc.ManifestPath, AgeKey: rc.AgeKey,
		Source: rc.Source, Cluster: rc.Cluster, ReleaseRepos: rc.ReleaseRepos,
		Persona: rc.Persona, ContextName: rc.ContextName, ContextSystemTenantID: rc.ContextSystemTenantID,
		ClusterOverridesContext: rc.ClusterOverridesContext, SourcePersistsManifest: rc.SourcePersistsManifest,
		sourceFloorVerifiedFor: rc.sourceFloorVerifiedFor, envSource: rc,
	}
}

func hasUnscopedDatabaseDeployments(manifest *inventory.Manifest) bool {
	if manifest == nil {
		return false
	}
	if manifest.DatabaseDeployment != "" {
		return false
	}
	for _, pg := range manifest.Infrastructure.DatabaseDeployments {
		if pg != nil && pg.Enabled {
			return true
		}
	}
	return false
}

func validateDatabaseDeploymentCommand(cmd *cobra.Command, manifest *inventory.Manifest, selection string) error {
	if selection != "" {
		for c := cmd; c != nil && c.Name() != "cluster"; c = c.Parent() {
			switch c.Name() {
			case "provision":
				if stringFlag(cmd, "only").Value != "infrastructure" {
					return fmt.Errorf("--database-deployment scopes SQL infrastructure; provision applications without this flag so their service bindings select the database")
				}
			case "apply", "release", "os", "set-channel", "secrets", "control-plane", "restore-fence":
				return fmt.Errorf("--database-deployment is not supported for %s; this command operates on the fleet", c.Name())
			}
		}
	}
	if selection == "" && hasUnscopedDatabaseDeployments(manifest) {
		for c := cmd; c != nil; c = c.Parent() {
			if c.Name() == "yugabyte" || c.Name() == "seed" {
				return fmt.Errorf("select --database-deployment primary or a named deployment for %s", c.Name())
			}
		}
	}
	return nil
}

func forEachDatabaseDeployment(manifest *inventory.Manifest, run func(*inventory.Manifest) error) error {
	for _, deployment := range manifest.SQLDeployments() {
		view, err := manifest.WithDatabaseDeployment(deployment.Name)
		if err != nil {
			return err
		}
		if err := run(view); err != nil {
			return fmt.Errorf("database deployment %s: %w", deployment.Name, err)
		}
	}
	return nil
}

func databaseTemplateOwnedElsewhere(name string, manifest *inventory.Manifest) bool {
	for id, svc := range manifest.Services {
		deploy := svc.Deploy
		if deploy == "" {
			deploy = id
		}
		if svc.Enabled && svc.Cluster != "" && deploy == name && strings.ReplaceAll(id, "-", "_") != name &&
			manifest.ServiceDatabaseDeployment(id) != inventory.DatabaseDeploymentName(manifest.DatabaseDeployment) {
			return true
		}
	}
	return false
}

func deploymentOwnsLogicalDatabase(manifest *inventory.Manifest, name string) bool {
	pg := manifest.Infrastructure.Postgres
	if pg == nil || !pg.Enabled {
		return false
	}
	for _, db := range expandedYugabyteDatabaseConfigs(pg.Databases, manifest) {
		if yugabyteLogicalDatabaseName(db.Name, manifest) == name {
			return true
		}
	}
	return false
}

// Physical names remain stable during moves and identify backups independently of location.
func validateSQLDatabaseOwnership(manifest *inventory.Manifest) error {
	if len(manifest.Infrastructure.DatabaseDeployments) == 0 {
		return nil
	}
	seen := map[string]string{}
	return forEachDatabaseDeployment(manifest, func(view *inventory.Manifest) error {
		databases := expandedYugabyteDatabaseConfigs(view.Infrastructure.Postgres.Databases, view)
		for _, instance := range view.Infrastructure.Postgres.Instances {
			databases = append(databases, instance.Databases...)
		}
		for _, db := range databases {
			if owner, exists := seen[db.Name]; exists {
				return fmt.Errorf("physical database %s is declared in both %s and %s; assign it to exactly one deployment", db.Name, owner, view.DatabaseDeployment)
			}
			seen[db.Name] = view.DatabaseDeployment
		}
		return nil
	})
}

// Explicit deployment bindings cannot be overridden by an old EU DSN in a shared env file.
func validateServiceDatabaseDeploymentEnv(task *orchestrator.Task, manifest *inventory.Manifest, env map[string]string) error {
	if len(manifest.Infrastructure.DatabaseDeployments) == 0 || !serviceIsDatabaseBacked(task.Type) {
		return nil
	}
	expected := map[string]string{}
	applyCatalogPostgresDatabaseDefaults(task, expected)
	inst, db, ok := declaredPostgresDatabaseForService(task, manifest, expected)
	if !ok {
		return fmt.Errorf("service %s has no declared database in deployment %s", task.ServiceID, manifest.DatabaseDeployment)
	}
	parsed, err := url.Parse(env["DATABASE_URL"])
	if err != nil || parsed.Host == "" || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		return fmt.Errorf("service %s: database deployment requires a postgres URL", task.ServiceID)
	}
	if strings.TrimPrefix(parsed.Path, "/") != db.Name {
		return fmt.Errorf("service %s: DATABASE_URL does not select declared database %s", task.ServiceID, db.Name)
	}
	pg := manifest.Infrastructure.Postgres
	if inst != nil {
		pg = &inventory.PostgresConfig{Host: inst.Host, Port: postgresInstancePort(inst)}
	}
	var hosts []string
	if pg.IsYugabyte() {
		for _, n := range pg.Nodes {
			hosts = append(hosts, n.Host)
		}
	} else {
		hosts = append(hosts, pg.Host)
	}
	allowed := map[string]bool{}
	for _, host := range hosts {
		allowed[fmt.Sprintf("%s:%d", manifestMeshHostname(manifest, host), pg.EffectivePort())] = true
		if inst != nil && host == task.Host {
			allowed[fmt.Sprintf("127.0.0.1:%d", pg.EffectivePort())] = true
		}
	}
	seen := map[string]bool{}
	for _, host := range strings.Split(parsed.Host, ",") {
		if !allowed[host] {
			return fmt.Errorf("service %s: DATABASE_URL endpoint conflicts with database deployment %s; remove stale DATABASE_URL/HOST/PORT overrides", task.ServiceID, manifest.DatabaseDeployment)
		}
		if seen[host] {
			return fmt.Errorf("service %s: DATABASE_URL repeats a database endpoint", task.ServiceID)
		}
		seen[host] = true
	}
	if pg.IsYugabyte() && len(strings.Split(parsed.Host, ",")) != len(hosts) {
		return fmt.Errorf("service %s: DATABASE_URL must include every node of deployment %s", task.ServiceID, manifest.DatabaseDeployment)
	}
	return nil
}
