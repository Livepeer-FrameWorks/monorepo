package orchestrator

import (
	"context"
	"fmt"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/servicedefs"
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
	// Add Postgres
	if p.manifest.Infrastructure.Postgres != nil && p.manifest.Infrastructure.Postgres.Enabled {
		graph.AddTask(&Task{
			Name:       "postgres",
			Type:       "postgres",
			Host:       p.manifest.Infrastructure.Postgres.Host,
			DependsOn:  []string{},
			Phase:      PhaseInfrastructure,
			Idempotent: true,
		})
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

	// Add Kafka (depends on Zookeeper)
	if p.manifest.Infrastructure.Kafka != nil && p.manifest.Infrastructure.Kafka.Enabled {
		zkDeps := []string{}
		if p.manifest.Infrastructure.Zookeeper != nil && p.manifest.Infrastructure.Zookeeper.Enabled {
			for _, node := range p.manifest.Infrastructure.Zookeeper.Ensemble {
				zkDeps = append(zkDeps, fmt.Sprintf("zookeeper-%d", node.ID))
			}
		}

		for _, broker := range p.manifest.Infrastructure.Kafka.Brokers {
			taskName := fmt.Sprintf("kafka-broker-%d", broker.ID)
			graph.AddTask(&Task{
				Name:       taskName,
				Type:       "kafka",
				Host:       broker.Host,
				DependsOn:  zkDeps,
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

	if p.manifest.Infrastructure.Postgres != nil && p.manifest.Infrastructure.Postgres.Enabled {
		infraDeps = append(infraDeps, "postgres")
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
			ClusterID:  p.manifest.ResolveCluster("quartermaster"),
			DependsOn:  infraDeps,
			Phase:      PhaseApplications,
			Idempotent: true,
		})
	}

	// 2. Privateer (System Mesh)
	// Depends on Quartermaster (for token)
	// Note: Privateer needs to be provisioned on ALL infrastructure nodes eventually.
	// For now, we assume it's listed in the services manifest or we iterate all hosts.
	// If 'privateer' is explicit in manifest services:
	if svc, ok := p.manifest.Services["privateer"]; ok && svc.Enabled {
		deploy, ok := servicedefs.DeployName("privateer", svc.Deploy)
		if !ok {
			return fmt.Errorf("unknown service id: privateer")
		}
		qmDep := append([]string{}, infraDeps...)
		if _, ok := p.manifest.Services["quartermaster"]; ok {
			qmDep = append(qmDep, "quartermaster")
		}

		graph.AddTask(&Task{
			Name:       "privateer",
			Type:       deploy,
			Host:       svc.Host,
			ClusterID:  p.manifest.ResolveCluster("privateer"),
			DependsOn:  qmDep,
			Phase:      PhaseApplications, // Technically infra/system, but needs QM up
			Idempotent: true,
		})
	}

	// 3. Other Applications
	// Depend on Quartermaster AND Privateer
	coreDeps := append([]string{}, infraDeps...)
	if _, ok := p.manifest.Services["quartermaster"]; ok {
		coreDeps = append(coreDeps, "quartermaster")
	}
	if _, ok := p.manifest.Services["privateer"]; ok {
		coreDeps = append(coreDeps, "privateer")
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

		graph.AddTask(&Task{
			Name:       name,
			Type:       deploy,
			Host:       svc.Host,
			ClusterID:  p.manifest.ResolveCluster(name),
			DependsOn:  coreDeps,
			Phase:      PhaseApplications,
			Idempotent: true,
		})
	}

	return nil
}

// addInterfaceTasks adds interface service provisioning tasks
func (p *Planner) addInterfaceTasks(graph *DependencyGraph) error {
	// Interfaces depend on application services
	appDeps := []string{}
	for name, svc := range p.manifest.Services {
		if svc.Enabled {
			appDeps = append(appDeps, name)
		}
	}

	// Add each interface service
	for name, iface := range p.manifest.Interfaces {
		if !iface.Enabled {
			continue
		}
		deploy, ok := servicedefs.DeployName(name, iface.Deploy)
		if !ok {
			return fmt.Errorf("unknown interface id: %s", name)
		}

		graph.AddTask(&Task{
			Name:       name,
			Type:       deploy,
			Host:       iface.Host,
			ClusterID:  p.manifest.ResolveCluster(name),
			DependsOn:  appDeps,
			Phase:      PhaseInterfaces,
			Idempotent: true,
		})
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

		graph.AddTask(&Task{
			Name:       name,
			Type:       deploy,
			Host:       obs.Host,
			ClusterID:  p.manifest.ResolveCluster(name),
			DependsOn:  appDeps,
			Phase:      PhaseInterfaces,
			Idempotent: true,
		})
	}

	return nil
}
