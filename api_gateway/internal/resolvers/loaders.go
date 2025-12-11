package resolvers

import (
	"context"
	"sync"

	pb "frameworks/pkg/proto"
)

// Simple per-request loaders. These de-dup repeated lookups and provide caching.

type NodeLoader struct {
	r     *Resolver
	mu    sync.Mutex
	cache map[string]*pb.InfrastructureNode
}

func NewNodeLoader(r *Resolver) *NodeLoader {
	return &NodeLoader{r: r, cache: make(map[string]*pb.InfrastructureNode)}
}

func (l *NodeLoader) Load(ctx context.Context, nodeID string) (*pb.InfrastructureNode, error) {
	l.mu.Lock()
	if n, ok := l.cache[nodeID]; ok {
		l.mu.Unlock()
		return n, nil
	}
	l.mu.Unlock()
	resp, err := l.r.Clients.Quartermaster.GetNode(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	l.cache[nodeID] = resp.Node
	l.mu.Unlock()
	return resp.Node, nil
}

type ClusterLoader struct {
	r     *Resolver
	mu    sync.Mutex
	cache map[string]*pb.InfrastructureCluster
}

func NewClusterLoader(r *Resolver) *ClusterLoader {
	return &ClusterLoader{r: r, cache: make(map[string]*pb.InfrastructureCluster)}
}

func (l *ClusterLoader) Load(ctx context.Context, clusterID string) (*pb.InfrastructureCluster, error) {
	l.mu.Lock()
	if c, ok := l.cache[clusterID]; ok {
		l.mu.Unlock()
		return c, nil
	}
	l.mu.Unlock()
	resp, err := l.r.Clients.Quartermaster.GetCluster(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	l.cache[clusterID] = resp.Cluster
	l.mu.Unlock()
	return resp.Cluster, nil
}

// Loaders bundles all dataloaders for request context
type Loaders struct {
	Node    *NodeLoader
	Cluster *ClusterLoader
}
