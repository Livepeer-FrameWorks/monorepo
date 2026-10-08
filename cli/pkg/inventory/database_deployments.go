package inventory

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const PrimaryDatabaseDeployment = "primary"

type DatabaseDeployment struct {
	Name   string
	Config *PostgresConfig
}

func DatabaseDeploymentName(name string) string {
	if name == "" {
		return PrimaryDatabaseDeployment
	}
	return name
}

// SQLDeployments returns the selected deployment, or all enabled deployments in stable order.
func (m *Manifest) SQLDeployments() []DatabaseDeployment {
	if m == nil {
		return nil
	}
	if m.DatabaseDeployment != "" {
		if pg := m.Infrastructure.Postgres; pg != nil && pg.Enabled {
			return []DatabaseDeployment{{m.DatabaseDeployment, pg}}
		}
		return nil
	}
	var result []DatabaseDeployment
	if pg := m.Infrastructure.Postgres; pg != nil && pg.Enabled {
		result = append(result, DatabaseDeployment{PrimaryDatabaseDeployment, pg})
	}
	names := make([]string, 0, len(m.Infrastructure.DatabaseDeployments))
	for name := range m.Infrastructure.DatabaseDeployments {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if pg := m.Infrastructure.DatabaseDeployments[name]; pg != nil && pg.Enabled {
			result = append(result, DatabaseDeployment{name, pg})
		}
	}
	return result
}

// WithDatabaseDeployment keeps the full service topology for endpoint resolution while
// directing SQL lifecycle operations to one independent cluster.
func (m *Manifest) WithDatabaseDeployment(name string) (*Manifest, error) {
	name = DatabaseDeploymentName(name)
	primary := m.Infrastructure.Postgres
	if m.DatabaseDeployment != "" {
		primary = m.DatabasePrimary
	}
	pg := primary
	if name != PrimaryDatabaseDeployment {
		pg = m.Infrastructure.DatabaseDeployments[name]
	}
	if pg == nil || !pg.Enabled {
		return nil, fmt.Errorf("database deployment %q is not enabled", name)
	}
	view := *m
	view.DatabasePrimary = primary
	view.DatabaseDeployment = name
	view.Infrastructure.Postgres = pg
	return &view, nil
}

func (m *Manifest) ServiceDatabaseDeployment(serviceID string) string {
	for _, configs := range []map[string]ServiceConfig{m.Services, m.Interfaces, m.Observability} {
		if svc, ok := configs[serviceID]; ok {
			return DatabaseDeploymentName(svc.DatabaseDeployment)
		}
	}
	return PrimaryDatabaseDeployment
}

func (m *Manifest) SQLDeploymentForHost(host string) (*Manifest, error) {
	for _, deployment := range m.SQLDeployments() {
		for _, candidate := range deployment.Config.AllHosts() {
			if host == candidate {
				return m.WithDatabaseDeployment(deployment.Name)
			}
		}
	}
	return nil, fmt.Errorf("host %q is not in an enabled database deployment", host)
}

func (m *Manifest) validateDatabaseDeployments() error {
	namePattern := regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	placementPattern := regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)
	for name, pg := range m.Infrastructure.DatabaseDeployments {
		if name == PrimaryDatabaseDeployment || !namePattern.MatchString(name) {
			return fmt.Errorf("invalid database deployment name %q (primary is reserved)", name)
		}
		if pg == nil {
			return fmt.Errorf("database deployment %q is empty", name)
		}
		if pg.Enabled && (m.Infrastructure.Postgres == nil || !m.Infrastructure.Postgres.Enabled) {
			return fmt.Errorf("named database deployments require an enabled primary infrastructure.postgres")
		}
	}
	hostOwners := map[string]string{}
	for _, deployment := range m.SQLDeployments() {
		pg := deployment.Config
		for _, value := range []string{pg.PlacementCloud, pg.PlacementRegion} {
			if value != "" && !placementPattern.MatchString(value) {
				return fmt.Errorf("database deployment %s: invalid placement label", deployment.Name)
			}
		}
		for _, node := range pg.Nodes {
			if node.PlacementZone != "" && !placementPattern.MatchString(node.PlacementZone) {
				return fmt.Errorf("database deployment %s: invalid zone for %s", deployment.Name, node.Host)
			}
		}
		if pg.Engine != "" && pg.Engine != "postgres" && pg.Engine != "yugabyte" {
			return fmt.Errorf("database deployment %s: unknown engine %q", deployment.Name, pg.Engine)
		}
		if err := validateDatabaseRuntimeRoles(deployment.Name, pg.Databases); err != nil {
			return err
		}
		for _, host := range pg.AllHosts() {
			if _, ok := m.Hosts[host]; !ok {
				return fmt.Errorf("database deployment %s: host %q not found", deployment.Name, host)
			}
			if previous, exists := hostOwners[host]; exists && previous != deployment.Name {
				return fmt.Errorf("database deployments %s and %s share host %s; their native services and data directories would collide", previous, deployment.Name, host)
			}
			hostOwners[host] = deployment.Name
		}
		if pg.IsYugabyte() {
			if len(pg.Nodes) == 0 {
				return fmt.Errorf("database deployment %s: yugabyte requires nodes", deployment.Name)
			}
			var ids []int
			var hosts []string
			for _, node := range pg.Nodes {
				ids = append(ids, node.ID)
				hosts = append(hosts, node.Host)
			}
			if err := validateClusterNodeIDs(deployment.Name, ids); err != nil {
				return err
			}
			if err := validateUniqueNodeHosts(deployment.Name, hosts); err != nil {
				return err
			}
			if pg.ReplicationFactor < 0 || pg.EffectiveReplicationFactor() > len(pg.Nodes) {
				return fmt.Errorf("database deployment %s: replication factor exceeds nodes or is negative", deployment.Name)
			}
			// This role runs a master on every node. Capacity expansion needs explicit
			// master-membership support; appending remote voters is not regional HA.
			if len(pg.Nodes) != 1 && len(pg.Nodes) != 3 {
				return fmt.Errorf("database deployment %s: supported master cohorts have 1 or 3 nodes; declare a separate deployment for another region", deployment.Name)
			}
			if deployment.Name != PrimaryDatabaseDeployment {
				if pg.ReplicationFactor != len(pg.Nodes) || pg.PlacementCloud == "" || pg.PlacementRegion == "" {
					return fmt.Errorf("database deployment %s: declare replication_factor equal to the 1 or 3 node cohort, placement_cloud and placement_region", deployment.Name)
				}
				for _, node := range pg.Nodes {
					if strings.TrimSpace(node.PlacementZone) == "" {
						return fmt.Errorf("database deployment %s: node %s requires its actual placement_zone", deployment.Name, node.Host)
					}
				}
			}
		} else if pg.Host == "" || len(pg.Nodes) != 0 {
			return fmt.Errorf("database deployment %s: postgres requires a host and does not implement multi-node HA", deployment.Name)
		}
		if deployment.Name != PrimaryDatabaseDeployment && len(pg.Instances) != 0 {
			return fmt.Errorf("database deployment %s: use separate named deployments instead of nested instances", deployment.Name)
		}
	}
	for _, configs := range []map[string]ServiceConfig{m.Services, m.Interfaces, m.Observability} {
		for id, svc := range configs {
			if !svc.Enabled || svc.DatabaseDeployment == "" {
				continue
			}
			if _, err := m.WithDatabaseDeployment(svc.DatabaseDeployment); err != nil {
				return fmt.Errorf("service %s: %w", id, err)
			}
		}
	}
	return nil
}
