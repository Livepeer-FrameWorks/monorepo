package loaders

import (
	"context"
	"sync"

	"frameworks/api_gateway/internal/clients"
	"frameworks/pkg/models"
)

// Loaders bundles per-request loaders. These provide simple de-dup and caching.
type Loaders struct {
	Node                      *NodeLoader
	Cluster                   *ClusterLoader
	NodesByCluster            *NodesByClusterLoader
	ServiceInstancesByCluster *ServiceInstancesByClusterLoader
	ServiceInstancesByNode    *ServiceInstancesByNodeLoader
}

func New(serviceClients *clients.ServiceClients) *Loaders {
	return &Loaders{
		Node:                      NewNodeLoader(serviceClients),
		Cluster:                   NewClusterLoader(serviceClients),
		NodesByCluster:            NewNodesByClusterLoader(serviceClients),
		ServiceInstancesByCluster: NewServiceInstancesByClusterLoader(serviceClients),
		ServiceInstancesByNode:    NewServiceInstancesByNodeLoader(serviceClients),
	}
}

// NodeLoader loads nodes by nodeID with request-scoped caching.
type NodeLoader struct {
	sc    *clients.ServiceClients
	mu    sync.Mutex
	cache map[string]*models.InfrastructureNode
}

func NewNodeLoader(sc *clients.ServiceClients) *NodeLoader {
	return &NodeLoader{sc: sc, cache: make(map[string]*models.InfrastructureNode)}
}

func (l *NodeLoader) Load(ctx context.Context, nodeID string) (*models.InfrastructureNode, error) {
	l.mu.Lock()
	if n, ok := l.cache[nodeID]; ok {
		l.mu.Unlock()
		return n, nil
	}
	l.mu.Unlock()
	resp, err := l.sc.Quartermaster.GetNode(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	l.cache[nodeID] = &resp.Node
	l.mu.Unlock()
	return &resp.Node, nil
}

// ClusterLoader loads clusters by clusterID with request-scoped caching.
type ClusterLoader struct {
	sc    *clients.ServiceClients
	mu    sync.Mutex
	cache map[string]*models.InfrastructureCluster
}

func NewClusterLoader(sc *clients.ServiceClients) *ClusterLoader {
	return &ClusterLoader{sc: sc, cache: make(map[string]*models.InfrastructureCluster)}
}

func (l *ClusterLoader) Load(ctx context.Context, clusterID string) (*models.InfrastructureCluster, error) {
	l.mu.Lock()
	if c, ok := l.cache[clusterID]; ok {
		l.mu.Unlock()
		return c, nil
	}
	l.mu.Unlock()
	resp, err := l.sc.Quartermaster.GetCluster(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	l.cache[clusterID] = &resp.Cluster
	l.mu.Unlock()
	return &resp.Cluster, nil
}

// NodesByClusterLoader loads all nodes for a given clusterID.
type NodesByClusterLoader struct {
	sc    *clients.ServiceClients
	mu    sync.Mutex
	cache map[string][]*models.InfrastructureNode
}

func NewNodesByClusterLoader(sc *clients.ServiceClients) *NodesByClusterLoader {
	return &NodesByClusterLoader{sc: sc, cache: make(map[string][]*models.InfrastructureNode)}
}

func (l *NodesByClusterLoader) Load(ctx context.Context, clusterID string) ([]*models.InfrastructureNode, error) {
	l.mu.Lock()
	if ns, ok := l.cache[clusterID]; ok {
		l.mu.Unlock()
		return ns, nil
	}
	l.mu.Unlock()
	resp, err := l.sc.Quartermaster.GetNodes(ctx, map[string]string{"cluster_id": clusterID})
	if err != nil {
		return nil, err
	}
	out := make([]*models.InfrastructureNode, 0, len(resp.Nodes))
	for i := range resp.Nodes {
		out = append(out, &resp.Nodes[i])
	}
	l.mu.Lock()
	l.cache[clusterID] = out
	l.mu.Unlock()
	return out, nil
}

// ServiceInstancesByClusterLoader loads service instances for a clusterID.
type ServiceInstancesByClusterLoader struct {
	sc    *clients.ServiceClients
	mu    sync.Mutex
	cache map[string][]*models.ServiceInstance
}

func NewServiceInstancesByClusterLoader(sc *clients.ServiceClients) *ServiceInstancesByClusterLoader {
	return &ServiceInstancesByClusterLoader{sc: sc, cache: make(map[string][]*models.ServiceInstance)}
}

func (l *ServiceInstancesByClusterLoader) Load(ctx context.Context, clusterID string) ([]*models.ServiceInstance, error) {
	l.mu.Lock()
	if list, ok := l.cache[clusterID]; ok {
		l.mu.Unlock()
		return list, nil
	}
	l.mu.Unlock()
	resp, err := l.sc.Quartermaster.GetServiceInstances(ctx, map[string]string{"cluster_id": clusterID})
	if err != nil {
		return nil, err
	}
	out := make([]*models.ServiceInstance, 0, len(resp.Instances))
	for i := range resp.Instances {
		out = append(out, &resp.Instances[i])
	}
	l.mu.Lock()
	l.cache[clusterID] = out
	l.mu.Unlock()
	return out, nil
}

// ServiceInstancesByNodeLoader loads service instances for a nodeID.
type ServiceInstancesByNodeLoader struct {
	sc    *clients.ServiceClients
	mu    sync.Mutex
	cache map[string][]*models.ServiceInstance
}

func NewServiceInstancesByNodeLoader(sc *clients.ServiceClients) *ServiceInstancesByNodeLoader {
	return &ServiceInstancesByNodeLoader{sc: sc, cache: make(map[string][]*models.ServiceInstance)}
}

func (l *ServiceInstancesByNodeLoader) Load(ctx context.Context, nodeID string) ([]*models.ServiceInstance, error) {
	l.mu.Lock()
	if list, ok := l.cache[nodeID]; ok {
		l.mu.Unlock()
		return list, nil
	}
	l.mu.Unlock()
	resp, err := l.sc.Quartermaster.GetServiceInstances(ctx, map[string]string{"node_id": nodeID})
	if err != nil {
		return nil, err
	}
	out := make([]*models.ServiceInstance, 0, len(resp.Instances))
	for i := range resp.Instances {
		out = append(out, &resp.Instances[i])
	}
	l.mu.Lock()
	l.cache[nodeID] = out
	l.mu.Unlock()
	return out, nil
}
