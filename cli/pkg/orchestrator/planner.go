package orchestrator

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"frameworks/cli/pkg/inventory"
	infra "github.com/Livepeer-FrameWorks/monorepo/pkg/models"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
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

// kafkaPlannerCluster is the minimal projection of a Kafka cluster the planner
// needs — identity (region_id), the controllers, and the brokers. Topics
// don't affect task generation so they're omitted here. Both the primary
// KafkaConfig and each RegionalKafkaCluster project into one entry.
type kafkaPlannerCluster struct {
	RegionID    string
	Controllers []inventory.KafkaController
	Brokers     []inventory.KafkaBroker
}

// kafkaClusters returns one kafkaPlannerCluster per declared Kafka cluster.
// Primary (RegionID="") is first, then each Regional. Returns nil when Kafka
// is disabled or unconfigured.
func (p *Planner) kafkaClusters() []kafkaPlannerCluster {
	if p.manifest == nil || p.manifest.Infrastructure.Kafka == nil || !p.manifest.Infrastructure.Kafka.Enabled {
		return nil
	}
	k := p.manifest.Infrastructure.Kafka
	out := make([]kafkaPlannerCluster, 0, 1+len(k.Regional))
	if len(k.Brokers) > 0 || len(k.Controllers) > 0 {
		out = append(out, kafkaPlannerCluster{
			Controllers: k.Controllers,
			Brokers:     k.Brokers,
		})
	}
	for _, rc := range k.Regional {
		out = append(out, kafkaPlannerCluster{
			RegionID:    rc.RegionID,
			Controllers: rc.Controllers,
			Brokers:     rc.Brokers,
		})
	}
	return out
}

// kafkaBrokerTaskName returns the planner-generated task name for a broker in
// the given cluster (matching the planner's emission rules above). Used by
// downstream consumers (application-task dep wiring) so the naming convention
// stays in one place.
func kafkaBrokerTaskName(regionID string, brokerID int) string {
	if regionID == "" {
		return "kafka-broker-" + strconv.Itoa(brokerID)
	}
	return "kafka-broker-" + regionID + "-" + strconv.Itoa(brokerID)
}

// effectiveServiceCluster picks the cluster a service task should run
// against. Service-scope (svc.Cluster / svc.Clusters[0]) wins over
// host-scope (the cluster the box belongs to) because cluster-scoped
// services like Foghorn / Chandler / livepeer-gateway live on a control-
// plane host but serve a media cluster — their env (S3 creds, etc.) has
// to be looked up against the media cluster, not the control cluster.
// Multi-cluster M:N declarations are not supported for credential-bearing
// services: declare one service entry per cluster instead.
func effectiveServiceCluster(svc inventory.ServiceConfig, hostName string, manifest *inventory.Manifest) string {
	if svc.Cluster != "" {
		return svc.Cluster
	}
	if len(svc.Clusters) > 0 {
		return svc.Clusters[0]
	}
	return manifest.HostCluster(hostName)
}

// Plan creates an execution plan based on options
func (p *Planner) Plan(ctx context.Context, opts ProvisionOptions) (*ExecutionPlan, error) {
	graph := NewDependencyGraph()

	// PhaseMesh is an implicit prerequisite of every other phase — infra,
	// apps, and interfaces can all end up addressing peers over the mesh.
	if err := p.addMeshTasks(graph); err != nil {
		return nil, fmt.Errorf("failed to add mesh tasks: %w", err)
	}

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

// addMeshTasks emits one privateer task per privateer-enabled host into
// PhaseMesh. These run first, with no dependencies, so the mesh substrate is
// up on every node before any infra task starts.
func (p *Planner) addMeshTasks(graph *DependencyGraph) error {
	svc, ok := p.manifest.Services["privateer"]
	if !ok || !svc.Enabled {
		return nil
	}
	deploy, ok := servicedefs.DeployName("privateer", svc.Deploy)
	if !ok {
		return fmt.Errorf("unknown service id: privateer")
	}
	privateerHosts := EffectivePrivateerHosts(svc, p.manifest.Hosts)
	for _, hostName := range privateerHosts {
		task := NewServiceTask(deploy, "privateer", hostName, hostName, PhaseMesh)
		task.Name = "privateer-mesh-" + hostName
		task.ClusterID = p.manifest.HostCluster(hostName)
		graph.AddTask(task)
	}
	return nil
}

// meshBarrierDeps returns the names of every privateer-mesh-* task. Used as
// a global barrier: every mesh-addressed infra task depends on *all* of
// them, mirroring the existing "Kafka brokers depend on all controllers"
// pattern. Empty when Privateer is not enabled.
func (p *Planner) meshBarrierDeps() []string {
	svc, ok := p.manifest.Services["privateer"]
	if !ok || !svc.Enabled {
		return nil
	}
	privateerHosts := EffectivePrivateerHosts(svc, p.manifest.Hosts)
	deps := make([]string, 0, len(privateerHosts))
	for _, h := range privateerHosts {
		deps = append(deps, "privateer-mesh-"+h)
	}
	return deps
}

// addInfrastructureTasks adds infrastructure provisioning tasks
func (p *Planner) addInfrastructureTasks(graph *DependencyGraph) error {
	hostDatabaseDeps := map[string][]string{}
	meshDeps := p.meshBarrierDeps()

	withMesh := func(deps []string) []string {
		if len(meshDeps) == 0 {
			return deps
		}
		out := make([]string, 0, len(deps)+len(meshDeps))
		out = append(out, deps...)
		out = append(out, meshDeps...)
		return out
	}

	// Add Postgres / YugabyteDB
	if pg := p.manifest.Infrastructure.Postgres; pg != nil && pg.Enabled {
		if pg.IsYugabyte() && len(pg.Nodes) > 0 {
			for _, node := range pg.Nodes {
				task := NewTask("yugabyte", "postgres", strconv.Itoa(node.ID), node.Host, PhaseInfrastructure)
				task.Name = "yugabyte-node-" + strconv.Itoa(node.ID)
				task.DependsOn = withMesh(task.DependsOn)
				graph.AddTask(task)
				hostDatabaseDeps[node.Host] = append(hostDatabaseDeps[node.Host], task.Name)
			}
		} else {
			task := NewTask("postgres", "postgres", "", pg.Host, PhaseInfrastructure)
			task.DependsOn = withMesh(task.DependsOn)
			graph.AddTask(task)
			hostDatabaseDeps[pg.Host] = append(hostDatabaseDeps[pg.Host], task.Name)
		}
		for _, inst := range pg.Instances {
			task := NewTask("postgres", "postgres", inst.Name, inst.Host, PhaseInfrastructure)
			task.DependsOn = withMesh(task.DependsOn)
			graph.AddTask(task)
			hostDatabaseDeps[inst.Host] = append(hostDatabaseDeps[inst.Host], task.Name)
		}
	}

	// Add Redis. Sentinel-mode instances fan out into N+1 server tasks
	// (primary + each replica) plus M Sentinel tasks watching the master.
	// Replica + sentinel tasks carry metadata pointing at the primary so
	// the role-aware install can render replicaof / sentinel monitor.
	if p.manifest.Infrastructure.Redis != nil && p.manifest.Infrastructure.Redis.Enabled {
		for _, instance := range p.manifest.Infrastructure.Redis.Instances {
			task := NewTask("redis", "redis", instance.Name, instance.Host, PhaseInfrastructure)
			task.DependsOn = withMesh(task.DependsOn)
			task.ClusterID = instance.Cluster
			task.Metadata = map[string]any{"redis_role": "primary"}
			graph.AddTask(task)
			if !strings.EqualFold(instance.Mode, "sentinel") {
				continue
			}
			for _, replicaHost := range instance.ReplicaHosts {
				replica := NewTask("redis", "redis", instance.Name+"-replica-"+replicaHost, replicaHost, PhaseInfrastructure)
				replica.DependsOn = withMesh([]string{task.Name})
				replica.ClusterID = instance.Cluster
				replica.Metadata = map[string]any{
					"redis_role":     "replica",
					"primary_host":   instance.Host,
					"primary_task":   task.Name,
					"instance_label": instance.Name,
				}
				graph.AddTask(replica)
			}
			for _, sn := range instance.Sentinels {
				sentinel := NewTask("redis", "redis", instance.Name+"-sentinel-"+sn.Host, sn.Host, PhaseInfrastructure)
				sentinel.DependsOn = withMesh([]string{task.Name})
				sentinel.ClusterID = instance.Cluster
				sentinel.Metadata = map[string]any{
					"redis_role":     "sentinel",
					"primary_host":   instance.Host,
					"sentinel_port":  sn.Port,
					"instance_label": instance.Name,
				}
				graph.AddTask(sentinel)
			}
		}
	}

	// Add Zookeeper
	if p.manifest.Infrastructure.Zookeeper != nil && p.manifest.Infrastructure.Zookeeper.Enabled {
		for _, node := range p.manifest.Infrastructure.Zookeeper.Ensemble {
			task := NewTask("zookeeper", "zookeeper", strconv.Itoa(node.ID), node.Host, PhaseInfrastructure)
			task.DependsOn = withMesh(task.DependsOn)
			graph.AddTask(task)
		}
	}

	// Add Kafka (KRaft). Each declared cluster (primary + each Regional) gets
	// its own controller + broker task set. Tasks are tagged via ClusterID
	// with the region_id (empty for primary) so the task builder can look up
	// the right cluster view. Task names get a region suffix when non-empty
	// to avoid collisions if two clusters declare overlapping IDs.
	for _, kc := range p.kafkaClusters() {
		controllerDeps := []string{}
		nameSuffix := ""
		if kc.RegionID != "" {
			nameSuffix = "-" + kc.RegionID
		}

		for _, ctrl := range kc.Controllers {
			task := NewTask("kafka-controller", "kafka", strconv.Itoa(ctrl.ID), ctrl.Host, PhaseInfrastructure)
			task.Name = "kafka-controller" + nameSuffix + "-" + strconv.Itoa(ctrl.ID)
			task.ClusterID = kc.RegionID
			task.DependsOn = withMesh(task.DependsOn)
			graph.AddTask(task)
			controllerDeps = append(controllerDeps, task.Name)
		}

		// Brokers depend on the controllers of their own cluster plus mesh.
		for _, broker := range kc.Brokers {
			task := NewTask("kafka", "kafka", strconv.Itoa(broker.ID), broker.Host, PhaseInfrastructure)
			task.Name = "kafka-broker" + nameSuffix + "-" + strconv.Itoa(broker.ID)
			task.ClusterID = kc.RegionID
			task.DependsOn = withMesh(controllerDeps)
			graph.AddTask(task)
		}
	}

	// MirrorMaker2 worker. Depends on every broker across every declared Kafka
	// cluster so the worker only starts once source + aggregator clusters are
	// live. One task on the configured host; no per-region split — connect-
	// mirror-maker.sh handles multiple source clusters from a single process.
	if mm := p.manifest.Infrastructure.Kafka; mm != nil && mm.Enabled && mm.MirrorMaker != nil && mm.MirrorMaker.Enabled {
		task := NewTask("kafka-mirrormaker", "kafka-mirrormaker", "", mm.MirrorMaker.Host, PhaseInfrastructure)
		task.DependsOn = withMesh(task.DependsOn)
		for _, kc := range p.kafkaClusters() {
			suffix := ""
			if kc.RegionID != "" {
				suffix = "-" + kc.RegionID
			}
			for _, broker := range kc.Brokers {
				task.DependsOn = append(task.DependsOn, "kafka-broker"+suffix+"-"+strconv.Itoa(broker.ID))
			}
		}
		graph.AddTask(task)
	}

	// Add ClickHouse
	if p.manifest.Infrastructure.ClickHouse != nil && p.manifest.Infrastructure.ClickHouse.Enabled {
		task := NewTask("clickhouse", "clickhouse", "", p.manifest.Infrastructure.ClickHouse.Host, PhaseInfrastructure)
		task.DependsOn = append(task.DependsOn, hostDatabaseDeps[task.Host]...)
		task.DependsOn = withMesh(task.DependsOn)
		graph.AddTask(task)
	}

	return nil
}

// addApplicationTasks adds application service provisioning tasks
func (p *Planner) addApplicationTasks(graph *DependencyGraph) error {
	// `--only applications` runs against a cluster where infrastructure is
	// already live: only emit infra DependsOn entries for tasks that are
	// actually in this graph.
	appendIfInGraph := func(deps []string, names ...string) []string {
		for _, name := range names {
			if graph.HasTask(name) {
				deps = append(deps, name)
			}
		}
		return deps
	}

	infraDeps := []string{}

	if pg := p.manifest.Infrastructure.Postgres; pg != nil && pg.Enabled {
		if pg.IsYugabyte() && len(pg.Nodes) > 0 {
			for _, node := range pg.Nodes {
				infraDeps = appendIfInGraph(infraDeps, "yugabyte-node-"+strconv.Itoa(node.ID))
			}
		} else {
			infraDeps = appendIfInGraph(infraDeps, "postgres")
		}
	}

	if p.manifest.Infrastructure.Redis != nil && p.manifest.Infrastructure.Redis.Enabled {
		for _, instance := range p.manifest.Infrastructure.Redis.Instances {
			infraDeps = appendIfInGraph(infraDeps, NewTask("redis", "redis", instance.Name, "", PhaseInfrastructure).Name)
		}
	}

	if p.manifest.Infrastructure.Kafka != nil && p.manifest.Infrastructure.Kafka.Enabled {
		// App tasks depend on every kafka broker across every declared
		// cluster (primary + each Regional). Naming follows kafkaBrokerTaskName.
		for _, kc := range p.kafkaClusters() {
			for _, broker := range kc.Brokers {
				infraDeps = appendIfInGraph(infraDeps, kafkaBrokerTaskName(kc.RegionID, broker.ID))
			}
		}
	}

	// 1. Quartermaster (Core Control Plane)
	// Must run before Privateer and other apps
	if svc, ok := p.manifest.Services["quartermaster"]; ok && svc.Enabled {
		deploy, ok := servicedefs.DeployName("quartermaster", svc.Deploy)
		if !ok {
			return fmt.Errorf("unknown service id: quartermaster")
		}
		task := NewServiceTask(deploy, "quartermaster", "", svc.Host, PhaseApplications)
		task.ClusterID = effectiveServiceCluster(svc, svc.Host, p.manifest)
		task.DependsOn = infraDeps
		graph.AddTask(task)
	}

	// Other Applications depend on Quartermaster AND every privateer-mesh
	// instance (global mesh barrier), but only when those tasks are in the
	// current graph.
	coreDeps := append([]string{}, infraDeps...)
	coreDeps = appendIfInGraph(coreDeps, "quartermaster")
	if svc, ok := p.manifest.Services["privateer"]; ok && svc.Enabled {
		privateerHosts := EffectivePrivateerHosts(svc, p.manifest.Hosts)
		for _, h := range privateerHosts {
			coreDeps = appendIfInGraph(coreDeps, "privateer-mesh-"+h)
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
			instanceID := ""
			if len(hosts) > 1 {
				instanceID = hostName
			}
			task := NewServiceTask(deploy, name, instanceID, hostName, PhaseApplications)
			task.ClusterID = effectiveServiceCluster(svc, hostName, p.manifest)
			task.DependsOn = coreDeps
			if name == "skipper" {
				if bridge, ok := p.manifest.Services["bridge"]; ok && bridge.Enabled {
					bridgeHosts := resolveHosts(bridge)
					if len(bridgeHosts) > 1 {
						for _, bridgeHost := range bridgeHosts {
							task.DependsOn = append(task.DependsOn, "bridge@"+bridgeHost)
						}
					} else {
						task.DependsOn = append(task.DependsOn, "bridge")
					}
				}
			}
			graph.AddTask(task)
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
		if !slices.Contains(h.Roles, infra.NodeTypeEdge) {
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
	// Interfaces depend on application services, but only those actually
	// emitted into the current graph — `--only interfaces` runs against an
	// assumed-live applications phase.
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
				candidate := NewServiceTask("", name, h, "", PhaseApplications).Name
				if graph.HasTask(candidate) {
					appDeps = append(appDeps, candidate)
				}
			}
		} else if graph.HasTask(name) {
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
			instanceID := ""
			if len(hosts) > 1 {
				instanceID = hostName
			}
			task := NewServiceTask(deploy, name, instanceID, hostName, PhaseInterfaces)
			task.ClusterID = effectiveServiceCluster(iface, hostName, p.manifest)
			task.DependsOn = appDeps
			graph.AddTask(task)
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
			instanceID := ""
			if len(hosts) > 1 {
				instanceID = hostName
			}
			task := NewServiceTask(deploy, name, instanceID, hostName, PhaseInterfaces)
			task.ClusterID = effectiveServiceCluster(obs, hostName, p.manifest)
			task.DependsOn = appDeps
			graph.AddTask(task)
		}
	}

	return nil
}
