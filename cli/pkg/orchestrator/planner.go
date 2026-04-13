package orchestrator

import (
	"context"
	"fmt"
	"sort"

	"frameworks/cli/pkg/inventory"
	infra "frameworks/pkg/models"
	"frameworks/pkg/servicedefs"
)

// Planner creates execution plans from manifests
type Planner struct {
	manifest *inventory.Manifest
}

// NewPlanner creates a new planner
func NewPlanner(manifest *inventory.Manifest) *Planner {
	return &Planner{
		manifest: manifest,
	}
}

// Plan creates an execution plan based on options
func (p *Planner) Plan(ctx context.Context, opts ProvisionOptions) (*ExecutionPlan, error) {
	graph := NewDependencyGraph()

	// Build task list based on phase
	switch opts.Phase {
	case PhaseInfrastructure, PhaseAll:
		if err := p.addInfrastructureTasks(graph); err != nil {
			return nil, fmt.Errorf("failed to add infrastructure tasks: %w", err)
		}
	}

	if opts.Phase == PhaseApplications || opts.Phase == PhaseAll {
		if err := p.addApplicationTasks(graph); err != nil {
			return nil, fmt.Errorf("failed to add application tasks: %w", err)
		}
	}

	if opts.Phase == PhaseInterfaces || opts.Phase == PhaseAll {
		if err := p.addInterfaceTasks(graph); err != nil {
			return nil, fmt.Errorf("failed to add interface tasks: %w", err)
		}
	}

	// Validate graph
	if err := graph.Validate(); err != nil {
		return nil, fmt.Errorf("dependency graph validation failed: %w", err)
	}

	// Perform topological sort
	batches, err := graph.TopologicalSort()
	if err != nil {
		return nil, fmt.Errorf("failed to sort tasks: %w", err)
	}

	// Build execution plan
	plan := &ExecutionPlan{
		Manifest: p.manifest,
		Batches:  batches,
		AllTasks: []*Task{},
	}

	for _, batch := range batches {
		plan.AllTasks = append(plan.AllTasks, batch...)
	}

	return plan, nil
}

// addInfrastructureTasks adds infrastructure provisioning tasks
func (p *Planner) addInfrastructureTasks(graph *DependencyGraph) error {
	// Add Postgres / YugabyteDB
	if pg := p.manifest.Infrastructure.Postgres; pg != nil && pg.Enabled {
		if pg.IsYugabyte() && len(pg.Nodes) > 0 {
			for _, node := range pg.Nodes {
				graph.AddTask(&Task{
					Name:       fmt.Sprintf("yugabyte-node-%d", node.ID),
					Type:       "yugabyte",
					Host:       node.Host,
					DependsOn:  []string{},
					Phase:      PhaseInfrastructure,
					Idempotent: true,
				})
			}
		} else {
			graph.AddTask(&Task{
				Name:       "postgres",
				Type:       "postgres",
				Host:       pg.Host,
				DependsOn:  []string{},
				Phase:      PhaseInfrastructure,
				Idempotent: true,
			})
		}
	}

	// Add Redis
	if p.manifest.Infrastructure.Redis != nil && p.manifest.Infrastructure.Redis.Enabled {
		for _, instance := range p.manifest.Infrastructure.Redis.Instances {
			taskName := fmt.Sprintf("redis-%s", instance.Name)
			graph.AddTask(&Task{
				Name:       taskName,
				Type:       "redis",
				Host:       instance.Host,
				DependsOn:  []string{},
				Phase:      PhaseInfrastructure,
				Idempotent: true,
			})
		}
	}

	// Add Zookeeper
	if p.manifest.Infrastructure.Zookeeper != nil && p.manifest.Infrastructure.Zookeeper.Enabled {
		for _, node := range p.manifest.Infrastructure.Zookeeper.Ensemble {
			taskName := fmt.Sprintf("zookeeper-%d", node.ID)
			graph.AddTask(&Task{
				Name:       taskName,
				Type:       "zookeeper",
				Host:       node.Host,
				DependsOn:  []string{},
				Phase:      PhaseInfrastructure,
				Idempotent: true,
			})
		}
	}

	// Add Kafka (KRaft — no ZooKeeper dependency)
	if p.manifest.Infrastructure.Kafka != nil && p.manifest.Infrastructure.Kafka.Enabled {
		for _, broker := range p.manifest.Infrastructure.Kafka.Brokers {
			taskName := fmt.Sprintf("kafka-broker-%d", broker.ID)
			graph.AddTask(&Task{
				Name:       taskName,
				Type:       "kafka",
				Host:       broker.Host,
				DependsOn:  []string{},
				Phase:      PhaseInfrastructure,
				Idempotent: true,
			})
		}
	}

	// Add ClickHouse
	if p.manifest.Infrastructure.ClickHouse != nil && p.manifest.Infrastructure.ClickHouse.Enabled {
		graph.AddTask(&Task{
			Name:       "clickhouse",
			Type:       "clickhouse",
			Host:       p.manifest.Infrastructure.ClickHouse.Host,
			DependsOn:  []string{},
			Phase:      PhaseInfrastructure,
			Idempotent: true,
		})
	}

	return nil
}

// addApplicationTasks adds application service provisioning tasks
func (p *Planner) addApplicationTasks(graph *DependencyGraph) error {
	// Application services depend on infrastructure
	infraDeps := []string{}

	if pg := p.manifest.Infrastructure.Postgres; pg != nil && pg.Enabled {
		if pg.IsYugabyte() && len(pg.Nodes) > 0 {
			for _, node := range pg.Nodes {
				infraDeps = append(infraDeps, fmt.Sprintf("yugabyte-node-%d", node.ID))
			}
		} else {
			infraDeps = append(infraDeps, "postgres")
		}
	}

	if p.manifest.Infrastructure.Redis != nil && p.manifest.Infrastructure.Redis.Enabled {
		for _, instance := range p.manifest.Infrastructure.Redis.Instances {
			infraDeps = append(infraDeps, fmt.Sprintf("redis-%s", instance.Name))
		}
	}

	if p.manifest.Infrastructure.Kafka != nil && p.manifest.Infrastructure.Kafka.Enabled {
		for _, broker := range p.manifest.Infrastructure.Kafka.Brokers {
			infraDeps = append(infraDeps, fmt.Sprintf("kafka-broker-%d", broker.ID))
		}
	}

	// 1. Quartermaster (Core Control Plane)
	// Must run before Privateer and other apps
	if svc, ok := p.manifest.Services["quartermaster"]; ok && svc.Enabled {
		deploy, ok := servicedefs.DeployName("quartermaster", svc.Deploy)
		if !ok {
			return fmt.Errorf("unknown service id: quartermaster")
		}
		graph.AddTask(&Task{
			Name:       "quartermaster",
			Type:       deploy,
			Host:       svc.Host,
			ClusterID:  p.manifest.HostCluster(svc.Host),
			DependsOn:  infraDeps,
			Phase:      PhaseApplications,
			Idempotent: true,
		})
	}

	// 2. Privateer (System Mesh)
	// Depends on Quartermaster (for enrollment token).
	// Privateer must be provisioned on ALL hosts so the mesh covers every node
	// before application services deploy (they rely on mesh DNS for discovery).
	if svc, ok := p.manifest.Services["privateer"]; ok && svc.Enabled {
		deploy, ok := servicedefs.DeployName("privateer", svc.Deploy)
		if !ok {
			return fmt.Errorf("unknown service id: privateer")
		}
		qmDep := append([]string{}, infraDeps...)
		if _, ok := p.manifest.Services["quartermaster"]; ok {
			qmDep = append(qmDep, "quartermaster")
		}

		// Deploy to explicitly listed hosts, or all non-edge manifest hosts if none specified.
		// Privateer runs on core nodes only; edge nodes enroll through Foghorn.
		privateerHosts := EffectivePrivateerHosts(svc, p.manifest.Hosts)

		for _, hostName := range privateerHosts {
			taskName := "privateer"
			if len(privateerHosts) > 1 {
				taskName = fmt.Sprintf("privateer@%s", hostName)
			}
			graph.AddTask(&Task{
				Name:       taskName,
				Type:       deploy,
				Host:       hostName,
				ClusterID:  p.manifest.HostCluster(hostName),
				DependsOn:  qmDep,
				Phase:      PhaseApplications,
				Idempotent: true,
			})
		}
	}

	// 3. Other Applications
	// Depend on Quartermaster AND all Privateer instances (mesh must be up first)
	coreDeps := append([]string{}, infraDeps...)
	if _, ok := p.manifest.Services["quartermaster"]; ok {
		coreDeps = append(coreDeps, "quartermaster")
	}
	if svc, ok := p.manifest.Services["privateer"]; ok && svc.Enabled {
		privateerHosts := EffectivePrivateerHosts(svc, p.manifest.Hosts)
		if len(privateerHosts) == 1 {
			coreDeps = append(coreDeps, "privateer")
		} else {
			for _, h := range privateerHosts {
				coreDeps = append(coreDeps, fmt.Sprintf("privateer@%s", h))
			}
		}
	}

	for name, svc := range p.manifest.Services {
		if !svc.Enabled {
			continue
		}
		if name == "quartermaster" || name == "privateer" {
			continue
		}
		deploy, ok := servicedefs.DeployName(name, svc.Deploy)
		if !ok {
			return fmt.Errorf("unknown service id: %s", name)
		}

		hosts := resolveHosts(svc)
		for _, hostName := range hosts {
			taskName := name
			if len(hosts) > 1 {
				taskName = fmt.Sprintf("%s@%s", name, hostName)
			}
			graph.AddTask(&Task{
				Name:       taskName,
				Type:       deploy,
				Host:       hostName,
				ClusterID:  p.manifest.HostCluster(hostName),
				DependsOn:  coreDeps,
				Phase:      PhaseApplications,
				Idempotent: true,
			})
		}
	}

	return nil
}

// resolveHosts returns the host list for a service config.
// Uses Hosts (plural) if set, otherwise falls back to single Host.
func resolveHosts(svc inventory.ServiceConfig) []string {
	if len(svc.Hosts) > 0 {
		return svc.Hosts
	}
	if svc.Host != "" {
		return []string{svc.Host}
	}
	return nil
}

// EffectivePrivateerHosts returns the hosts that should run Privateer.
// Uses explicit hosts if specified, otherwise all non-edge manifest hosts.
func EffectivePrivateerHosts(svc inventory.ServiceConfig, hosts map[string]inventory.Host) []string {
	explicit := resolveHosts(svc)
	if len(explicit) > 0 {
		return explicit
	}
	var result []string
	for name, h := range hosts {
		isEdge := false
		for _, role := range h.Roles {
			if role == infra.NodeTypeEdge {
				isEdge = true
				break
			}
		}
		if !isEdge {
			result = append(result, name)
		}
	}
	return result
}

// EffectiveVMAgentHosts returns the hosts that should run vmagent.
// Uses explicit hosts if specified, otherwise all manifest hosts.
func EffectiveVMAgentHosts(svc inventory.ServiceConfig, hosts map[string]inventory.Host) []string {
	explicit := resolveHosts(svc)
	if len(explicit) > 0 {
		return explicit
	}
	var result []string
	for name := range hosts {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

// addInterfaceTasks adds interface service provisioning tasks
func (p *Planner) addInterfaceTasks(graph *DependencyGraph) error {
	// Interfaces depend on application services
	appDeps := []string{}
	for name, svc := range p.manifest.Services {
		if !svc.Enabled {
			continue
		}
		hosts := resolveHosts(svc)
		if name == "privateer" && len(hosts) == 0 {
			hosts = EffectivePrivateerHosts(svc, p.manifest.Hosts)
		}
		if len(hosts) > 1 {
			for _, h := range hosts {
				appDeps = append(appDeps, fmt.Sprintf("%s@%s", name, h))
			}
		} else {
			appDeps = append(appDeps, name)
		}
	}

	// Add each interface service (with multi-host support)
	for name, iface := range p.manifest.Interfaces {
		if !iface.Enabled {
			continue
		}
		deploy, ok := servicedefs.DeployName(name, iface.Deploy)
		if !ok {
			return fmt.Errorf("unknown interface id: %s", name)
		}

		hosts := resolveHosts(iface)
		for _, hostName := range hosts {
			taskName := name
			if len(hosts) > 1 {
				taskName = fmt.Sprintf("%s@%s", name, hostName)
			}
			graph.AddTask(&Task{
				Name:       taskName,
				Type:       deploy,
				Host:       hostName,
				ClusterID:  p.manifest.HostCluster(hostName),
				DependsOn:  appDeps,
				Phase:      PhaseInterfaces,
				Idempotent: true,
			})
		}
	}

	// Observability stack (treated as interfaces for ordering)
	for name, obs := range p.manifest.Observability {
		if !obs.Enabled {
			continue
		}
		deploy, ok := servicedefs.DeployName(name, obs.Deploy)
		if !ok {
			return fmt.Errorf("unknown observability id: %s", name)
		}

		hosts := resolveHosts(obs)
		if name == "vmagent" && len(hosts) == 0 {
			hosts = EffectiveVMAgentHosts(obs, p.manifest.Hosts)
		}
		for _, hostName := range hosts {
			taskName := name
			if len(hosts) > 1 {
				taskName = fmt.Sprintf("%s@%s", name, hostName)
			}
			graph.AddTask(&Task{
				Name:       taskName,
				Type:       deploy,
				Host:       hostName,
				ClusterID:  p.manifest.HostCluster(hostName),
				DependsOn:  appDeps,
				Phase:      PhaseInterfaces,
				Idempotent: true,
			})
		}
	}

	return nil
}
