package loaders

import (
	"context"
	"sync"

	"frameworks/api_gateway/internal/clients"
	"frameworks/pkg/ctxkeys"
	pb "frameworks/pkg/proto"
)

// Loaders bundles per-request loaders. These provide simple de-dup and caching.
type Loaders struct {
	Node                      *NodeLoader
	Cluster                   *ClusterLoader
	NodesByCluster            *NodesByClusterLoader
	ServiceInstancesByCluster *ServiceInstancesByClusterLoader
	ServiceInstancesByNode    *ServiceInstancesByNodeLoader
	StreamMetrics             *StreamMetricsLoader
	ArtifactLifecycle         *ArtifactLifecycleLoader
	Memo                      *Memoizer
}

func New(serviceClients *clients.ServiceClients) *Loaders {
	return &Loaders{
		Node:                      NewNodeLoader(serviceClients),
		Cluster:                   NewClusterLoader(serviceClients),
		NodesByCluster:            NewNodesByClusterLoader(serviceClients),
		ServiceInstancesByCluster: NewServiceInstancesByClusterLoader(serviceClients),
		ServiceInstancesByNode:    NewServiceInstancesByNodeLoader(serviceClients),
		StreamMetrics:             NewStreamMetricsLoader(serviceClients.Periscope),
		ArtifactLifecycle:         NewArtifactLifecycleLoader(serviceClients.Periscope),
		Memo:                      NewMemoizer(),
	}
}

// ContextWithLoaders stores loaders in the context
func ContextWithLoaders(ctx context.Context, l *Loaders) context.Context {
	return context.WithValue(ctx, ctxkeys.KeyLoaders, l)
}

// FromContext retrieves loaders from the context
func FromContext(ctx context.Context) *Loaders {
	if ctx == nil {
		return nil
	}
	if l, ok := ctx.Value(ctxkeys.KeyLoaders).(*Loaders); ok {
		return l
	}
	return nil
}

// Memoizer provides request-scoped memoization for arbitrary keys
type Memoizer struct {
	mu   sync.Mutex
	data map[string]*memoEntry
}

type memoEntry struct {
	value interface{}
	err   error
	ready chan struct{}
}

// NewMemoizer creates a new memoizer instance
func NewMemoizer() *Memoizer {
	return &Memoizer{data: make(map[string]*memoEntry)}
}

// GetOrLoad returns a cached value for key or invokes loader once per key
func (m *Memoizer) GetOrLoad(key string, loader func() (interface{}, error)) (interface{}, error) {
	if m == nil {
		return loader()
	}
	m.mu.Lock()
	if entry, ok := m.data[key]; ok {
		m.mu.Unlock()
		<-entry.ready
		return entry.value, entry.err
	}
	entry := &memoEntry{ready: make(chan struct{})}
	m.data[key] = entry
	m.mu.Unlock()

	entry.value, entry.err = loader()
	close(entry.ready)
	return entry.value, entry.err
}

// NodeLoader loads nodes by nodeID with request-scoped caching.
type NodeLoader struct {
	sc    *clients.ServiceClients
	mu    sync.Mutex
	cache map[string]*pb.InfrastructureNode
}

func NewNodeLoader(sc *clients.ServiceClients) *NodeLoader {
	return &NodeLoader{sc: sc, cache: make(map[string]*pb.InfrastructureNode)}
}

func (l *NodeLoader) Load(ctx context.Context, nodeID string) (*pb.InfrastructureNode, error) {
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
	l.cache[nodeID] = resp.Node
	l.mu.Unlock()
	return resp.Node, nil
}

// ClusterLoader loads clusters by clusterID with request-scoped caching.
type ClusterLoader struct {
	sc    *clients.ServiceClients
	mu    sync.Mutex
	cache map[string]*pb.InfrastructureCluster
}

func NewClusterLoader(sc *clients.ServiceClients) *ClusterLoader {
	return &ClusterLoader{sc: sc, cache: make(map[string]*pb.InfrastructureCluster)}
}

func (l *ClusterLoader) Load(ctx context.Context, clusterID string) (*pb.InfrastructureCluster, error) {
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
	l.cache[clusterID] = resp.Cluster
	l.mu.Unlock()
	return resp.Cluster, nil
}

// NodesByClusterLoader loads all nodes for a given clusterID.
type NodesByClusterLoader struct {
	sc    *clients.ServiceClients
	mu    sync.Mutex
	cache map[string][]*pb.InfrastructureNode
}

func NewNodesByClusterLoader(sc *clients.ServiceClients) *NodesByClusterLoader {
	return &NodesByClusterLoader{sc: sc, cache: make(map[string][]*pb.InfrastructureNode)}
}

func (l *NodesByClusterLoader) Load(ctx context.Context, clusterID string) ([]*pb.InfrastructureNode, error) {
	l.mu.Lock()
	if ns, ok := l.cache[clusterID]; ok {
		l.mu.Unlock()
		return ns, nil
	}
	l.mu.Unlock()
	resp, err := l.sc.Quartermaster.ListNodes(ctx, clusterID, "", "", &pb.CursorPaginationRequest{First: 500})
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	l.cache[clusterID] = resp.Nodes
	l.mu.Unlock()
	return resp.Nodes, nil
}

// ServiceInstancesByClusterLoader loads service instances for a clusterID.
type ServiceInstancesByClusterLoader struct {
	sc    *clients.ServiceClients
	mu    sync.Mutex
	cache map[string][]*pb.ServiceInstance
}

func NewServiceInstancesByClusterLoader(sc *clients.ServiceClients) *ServiceInstancesByClusterLoader {
	return &ServiceInstancesByClusterLoader{sc: sc, cache: make(map[string][]*pb.ServiceInstance)}
}

func (l *ServiceInstancesByClusterLoader) Load(ctx context.Context, clusterID string) ([]*pb.ServiceInstance, error) {
	l.mu.Lock()
	if list, ok := l.cache[clusterID]; ok {
		l.mu.Unlock()
		return list, nil
	}
	l.mu.Unlock()
	resp, err := l.sc.Quartermaster.ListServiceInstances(ctx, clusterID, "", "", &pb.CursorPaginationRequest{First: 500})
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	l.cache[clusterID] = resp.Instances
	l.mu.Unlock()
	return resp.Instances, nil
}

// ServiceInstancesByNodeLoader loads service instances for a nodeID.
type ServiceInstancesByNodeLoader struct {
	sc    *clients.ServiceClients
	mu    sync.Mutex
	cache map[string][]*pb.ServiceInstance
}

func NewServiceInstancesByNodeLoader(sc *clients.ServiceClients) *ServiceInstancesByNodeLoader {
	return &ServiceInstancesByNodeLoader{sc: sc, cache: make(map[string][]*pb.ServiceInstance)}
}

func (l *ServiceInstancesByNodeLoader) Load(ctx context.Context, nodeID string) ([]*pb.ServiceInstance, error) {
	l.mu.Lock()
	if list, ok := l.cache[nodeID]; ok {
		l.mu.Unlock()
		return list, nil
	}
	l.mu.Unlock()
	// Use nodeID filter in the ListServiceInstances call
	resp, err := l.sc.Quartermaster.ListServiceInstances(ctx, "", "", nodeID, &pb.CursorPaginationRequest{First: 500})
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	l.cache[nodeID] = resp.Instances
	l.mu.Unlock()
	return resp.Instances, nil
}
