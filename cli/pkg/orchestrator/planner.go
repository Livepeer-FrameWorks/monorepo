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
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
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
	Role        string
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
			RegionID:    k.RegionID,
			Role:        k.Role,
			Controllers: k.Controllers,
			Brokers:     k.Brokers,
		})
	}
	for _, rc := range k.Regional {
		out = append(out, kafkaPlannerCluster{
			RegionID:    rc.RegionID,
			Role:        rc.Role,
			Controllers: rc.Controllers,
			Brokers:     rc.Brokers,
		})
	}
	return out
}

func (p *Planner) kafkaAggregatorRegion() string {
	if p.manifest == nil || p.manifest.Infrastructure.Kafka == nil {
		return ""
	}
	k := p.manifest.Infrastructure.Kafka
	if k.Role == "aggregator" || k.Role == "" {
		if k.RegionID != "" {
			return k.RegionID
		}
		if region := p.kafkaNodeRegion(k.Controllers, k.Brokers); region != "" {
			return region
		}
	}
	for _, rc := range k.Regional {
		if rc.Role != "aggregator" {
			continue
		}
		if rc.RegionID != "" {
			return rc.RegionID
		}
		if region := p.kafkaNodeRegion(rc.Controllers, rc.Brokers); region != "" {
			return region
		}
	}
	return ""
}

func (p *Planner) kafkaNodeRegion(controllers []inventory.KafkaController, brokers []inventory.KafkaBroker) string {
	region := ""
	record := func(hostName string) {
		if region != "" || p.manifest == nil {
			return
		}
		region = p.hostRegion(hostName)
	}
	for _, controller := range controllers {
		record(controller.Host)
	}
	for _, broker := range brokers {
		record(broker.Host)
	}
	return region
}

func (p *Planner) hostRegion(hostName string) string {
	if p.manifest == nil {
		return ""
	}
	host, ok := p.manifest.Hosts[hostName]
	if !ok {
		return ""
	}
	if region := strings.TrimSpace(host.Labels["region"]); region != "" {
		return region
	}
	if host.Cluster != "" {
		if cluster, ok := p.manifest.Clusters[host.Cluster]; ok {
			return strings.TrimSpace(cluster.Region)
		}
	}
	for _, kc := range p.kafkaClusters() {
		for _, controller := range kc.Controllers {
			if controller.Host == hostName && kc.RegionID != "" {
				return kc.RegionID
			}
		}
		for _, broker := range kc.Brokers {
			if broker.Host == hostName && kc.RegionID != "" {
				return kc.RegionID
			}
		}
	}
	return ""
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

func redisPrimaryTaskName(instance inventory.RedisInstance) string {
	if instance.Cluster == "" {
		return "redis-" + instance.Name
	}
	return "redis-" + instance.Name + "-" + instance.Cluster
}

func redisReplicaTaskName(instance inventory.RedisInstance, host string) string {
	if instance.Cluster == "" {
		return "redis-" + instance.Name + "-replica-" + host
	}
	return "redis-" + instance.Name + "-" + instance.Cluster + "-replica-" + host
}

func redisSentinelTaskName(instance inventory.RedisInstance, host string) string {
	if instance.Cluster == "" {
		return "redis-" + instance.Name + "-sentinel-" + host
	}
	return "redis-" + instance.Name + "-" + instance.Cluster + "-sentinel-" + host
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

func (p *Planner) topologyInfraTaskDeps(serviceID string, svc inventory.ServiceConfig, clusterID string, appendIfInGraph func([]string, ...string) []string) []string {
	var deps []string
	for _, dep := range topology.InfraDependencies(serviceID) {
		switch dep.Kind {
		case topology.InfraDatabase:
			deps = appendIfInGraph(deps, p.databaseTaskNames(dep)...)
		case topology.InfraClickHouse:
			deps = appendIfInGraph(deps, p.clickhouseTaskNames()...)
		case topology.InfraKafka:
			deps = appendIfInGraph(deps, p.kafkaTaskNames(dep, clusterID)...)
		case topology.InfraRedis:
			deps = appendIfInGraph(deps, p.redisTaskNames(dep, svc, clusterID)...)
		}
	}
	return deps
}

func (p *Planner) databaseTaskNames(dep topology.InfraDependency) []string {
	pg := p.manifest.Infrastructure.Postgres
	if pg == nil || !pg.Enabled {
		return nil
	}
	if dep.Provider == topology.InfraProviderNamed {
		var out []string
		for _, inst := range pg.Instances {
			if inst.Name == dep.Name {
				out = append(out, "postgres-"+inst.Name)
			}
		}
		return out
	}
	if pg.IsYugabyte() && len(pg.Nodes) > 0 {
		out := make([]string, 0, len(pg.Nodes))
		for _, node := range pg.Nodes {
			out = append(out, "yugabyte-node-"+strconv.Itoa(node.ID))
		}
		return out
	}
	return []string{"postgres"}
}

// clickhouseNodeTaskName is the task name for a Replicated ClickHouse node.
func clickhouseNodeTaskName(id int) string {
	return "clickhouse-node-" + strconv.Itoa(id)
}

// clickhouseTaskNames returns the per-node provisioning task names ClickHouse
// consumers depend on. ClickHouse is always a Replicated cluster (N>=1 nodes).
func (p *Planner) clickhouseTaskNames() []string {
	ch := p.manifest.Infrastructure.ClickHouse
	if ch == nil || !ch.Enabled {
		return nil
	}
	out := make([]string, 0, len(ch.Nodes))
	for _, node := range ch.Nodes {
		out = append(out, clickhouseNodeTaskName(node.ID))
	}
	return out
}

func (p *Planner) kafkaTaskNames(dep topology.InfraDependency, clusterID string) []string {
	if p.manifest.Infrastructure.Kafka == nil || !p.manifest.Infrastructure.Kafka.Enabled {
		return nil
	}
	var selected *kafkaPlannerCluster
	if dep.Provider == topology.InfraProviderAggregator {
		selected = p.aggregatorKafkaCluster()
	} else {
		selected = p.kafkaClusterForServiceCluster(clusterID)
	}
	if selected == nil {
		return nil
	}
	out := make([]string, 0, len(selected.Brokers))
	for _, broker := range selected.Brokers {
		out = append(out, kafkaBrokerTaskName(selected.RegionID, broker.ID))
	}
	return out
}

func (p *Planner) aggregatorKafkaCluster() *kafkaPlannerCluster {
	clusters := p.kafkaClusters()
	for i := range clusters {
		if strings.EqualFold(clusters[i].Role, "aggregator") {
			return &clusters[i]
		}
	}
	if len(clusters) == 0 {
		return nil
	}
	return &clusters[0]
}

func (p *Planner) kafkaClusterForServiceCluster(clusterID string) *kafkaPlannerCluster {
	clusters := p.kafkaClusters()
	if len(clusters) == 0 {
		return nil
	}
	region := ""
	if clusterID != "" {
		if cluster, ok := p.manifest.Clusters[clusterID]; ok {
			region = strings.TrimSpace(cluster.Region)
		}
	}
	if region != "" {
		for i := range clusters {
			if clusters[i].RegionID == region {
				return &clusters[i]
			}
		}
	}
	return &clusters[0]
}

func (p *Planner) redisTaskNames(dep topology.InfraDependency, _ inventory.ServiceConfig, clusterID string) []string {
	if p.manifest.Infrastructure.Redis == nil || !p.manifest.Infrastructure.Redis.Enabled {
		return nil
	}
	var out []string
	for _, inst := range p.manifest.Infrastructure.Redis.Instances {
		if dep.Provider == topology.InfraProviderNamed && inst.Name != dep.Name {
			continue
		}
		if inst.Cluster != "" && inst.Cluster != clusterID {
			continue
		}
		out = append(out, redisPrimaryTaskName(inst))
	}
	return out
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
	privateerHosts := EffectivePrivateerHostsForManifest(svc, p.manifest)
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
	privateerHosts := EffectivePrivateerHostsForManifest(svc, p.manifest)
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
			task.Name = redisPrimaryTaskName(instance)
			task.DependsOn = withMesh(task.DependsOn)
			task.ClusterID = instance.Cluster
			task.Metadata = map[string]any{"redis_role": "primary"}
			graph.AddTask(task)
			if !strings.EqualFold(instance.Mode, "sentinel") {
				continue
			}
			for _, replicaHost := range instance.ReplicaHosts {
				replica := NewTask("redis", "redis", instance.Name+"-replica-"+replicaHost, replicaHost, PhaseInfrastructure)
				replica.Name = redisReplicaTaskName(instance, replicaHost)
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
				sentinel.Name = redisSentinelTaskName(instance, sn.Host)
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

	// MirrorMaker2 workers. They depend on every broker across every declared
	// Kafka cluster so the worker cluster only starts once source + aggregator
	// clusters are live. Multiple hosts run the same dedicated MM2 config and
	// coordinate task ownership through Kafka Connect/MM2 internals.
	if mm := p.manifest.Infrastructure.Kafka; mm != nil && mm.Enabled && mm.MirrorMaker != nil && mm.MirrorMaker.Enabled {
		aggregatorRegion := p.kafkaAggregatorRegion()
		hosts := mm.MirrorMaker.Hosts
		if len(hosts) == 0 && mm.MirrorMaker.Host != "" {
			hosts = []string{mm.MirrorMaker.Host}
		}
		for _, host := range hosts {
			if strings.TrimSpace(host) == "" {
				continue
			}
			if aggregatorRegion != "" {
				hostRegion := p.hostRegion(host)
				if hostRegion == "" {
					return fmt.Errorf("kafka mirrormaker host %q has no region; MM2 workers must run in aggregator region %q", host, aggregatorRegion)
				}
				if hostRegion != aggregatorRegion {
					return fmt.Errorf("kafka mirrormaker host %q is in region %q, want aggregator region %q", host, hostRegion, aggregatorRegion)
				}
			}
			task := NewTask("kafka-mirrormaker", "kafka-mirrormaker", host, host, PhaseInfrastructure)
			task.DependsOn = withMesh(task.DependsOn)
			if len(hosts) == 1 {
				task.Name = "kafka-mirrormaker"
			}
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
	}

	// Add ClickHouse. Always a Replicated cluster (N>=1): one task per node,
	// mirroring the YugabyteDB pattern.
	if ch := p.manifest.Infrastructure.ClickHouse; ch != nil && ch.Enabled {
		// Centralized ClickHouse size guard. Planner-backed flows share this path, so
		// manifests larger than the supported single-node bootstrap cannot partially
		// apply.
		if ch.IsMultiNode() {
			return fmt.Errorf("multi-node ClickHouse (%d nodes) bootstrap is unsupported; declare exactly one node", len(ch.Nodes))
		}
		for _, node := range ch.Nodes {
			task := NewTask("clickhouse", "clickhouse", strconv.Itoa(node.ID), node.Host, PhaseInfrastructure)
			task.Name = clickhouseNodeTaskName(node.ID)
			task.DependsOn = append(task.DependsOn, hostDatabaseDeps[node.Host]...)
			task.DependsOn = withMesh(task.DependsOn)
			graph.AddTask(task)
		}
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

	// 1. Quartermaster (Core Control Plane)
	// Must run before Privateer and other apps
	if svc, ok := p.manifest.Services["quartermaster"]; ok && svc.Enabled {
		deploy, ok := servicedefs.DeployName("quartermaster", svc.Deploy)
		if !ok {
			return fmt.Errorf("unknown service id: quartermaster")
		}
		task := NewServiceTask(deploy, "quartermaster", "", svc.Host, PhaseApplications)
		task.ClusterID = effectiveServiceCluster(svc, svc.Host, p.manifest)
		task.DependsOn = p.topologyInfraTaskDeps("quartermaster", svc, task.ClusterID, appendIfInGraph)
		graph.AddTask(task)
	}

	// Other Applications depend on Quartermaster AND every privateer-mesh
	// instance (global mesh barrier), but only when those tasks are in the
	// current graph.
	coreDeps := appendIfInGraph(nil, "quartermaster")
	if svc, ok := p.manifest.Services["privateer"]; ok && svc.Enabled {
		privateerHosts := EffectivePrivateerHostsForManifest(svc, p.manifest)
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
			task.DependsOn = append(p.topologyInfraTaskDeps(deploy, svc, task.ClusterID, appendIfInGraph), coreDeps...)
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

func EffectivePrivateerHostsForManifest(svc inventory.ServiceConfig, manifest *inventory.Manifest) []string {
	if manifest == nil {
		return nil
	}
	seen := map[string]struct{}{}
	for _, hostName := range EffectivePrivateerHosts(svc, manifest.Hosts) {
		if hostName != "" {
			seen[hostName] = struct{}{}
		}
	}
	if vmagent, ok := manifest.Observability["vmagent"]; ok && vmagent.Enabled {
		for _, hostName := range EffectiveVMAgentHosts(vmagent, manifest) {
			if hostName != "" {
				seen[hostName] = struct{}{}
			}
		}
	}
	if vmauth, ok := manifest.Observability["vmauth"]; ok && vmauth.Enabled {
		for _, hostName := range resolveHosts(vmauth) {
			if hostName != "" {
				seen[hostName] = struct{}{}
			}
		}
	}
	if vm, ok := manifest.Observability["victoriametrics"]; ok && vm.Enabled {
		for _, hostName := range resolveHosts(vm) {
			if hostName != "" {
				seen[hostName] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(seen))
	for hostName := range seen {
		result = append(result, hostName)
	}
	sort.Strings(result)
	return result
}

// EffectiveVMAgentHosts returns the hosts that should run vmagent.
// Uses explicit hosts if specified, otherwise all manifest hosts. Native
// metrics infrastructure is additive so operators don't have to manually keep
// vmagent placement in sync with Yugabyte/ClickHouse placement.
func EffectiveVMAgentHosts(svc inventory.ServiceConfig, manifest *inventory.Manifest) []string {
	if manifest == nil {
		return nil
	}
	seen := map[string]struct{}{}
	explicit := resolveHosts(svc)
	if len(explicit) > 0 {
		for _, name := range explicit {
			name = strings.TrimSpace(name)
			if name != "" {
				seen[name] = struct{}{}
			}
		}
	} else {
		for name := range manifest.Hosts {
			seen[name] = struct{}{}
		}
	}
	if pg := manifest.Infrastructure.Postgres; pg != nil && pg.Enabled && pg.IsYugabyte() {
		for _, node := range pg.Nodes {
			if node.Host != "" {
				seen[node.Host] = struct{}{}
			}
		}
	}
	if ch := manifest.Infrastructure.ClickHouse; ch != nil && ch.Enabled {
		for _, host := range ch.AllHosts() {
			seen[host] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for name := range seen {
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
			hosts = EffectivePrivateerHostsForManifest(svc, p.manifest)
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
		if name == "vmagent" {
			hosts = EffectiveVMAgentHosts(obs, p.manifest)
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
