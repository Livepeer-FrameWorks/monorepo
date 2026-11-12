package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

type aliasAssignmentReader interface {
	ListServiceClusterAssignments(context.Context, string, string) (*quartermasterpb.ListServiceClusterAssignmentsResponse, error)
}

// localAliasClusters follows each local controller's serving assignments. The
// host's mesh cluster identifies Privateer, not the tenants it serves.
func (a *Agent) localAliasClusters(ctx context.Context) ([]string, error) {
	pagination := &commonpb.CursorPaginationRequest{First: 200}
	instances := map[string]string{}
	seenCursors := map[string]bool{}
	for {
		response, err := a.registryClient.ListServiceInstances(ctx, "", "", a.nodeID, pagination)
		if err != nil {
			return nil, fmt.Errorf("list local Foghorn instances: %w", err)
		}
		if response == nil {
			return nil, errors.New("local service inventory returned no authoritative response")
		}
		for _, instance := range response.GetInstances() {
			if instance.GetServiceId() != foghornServiceType || !strings.EqualFold(instance.GetStatus(), "running") {
				continue
			}
			id, cluster := strings.TrimSpace(instance.GetInstanceId()), strings.TrimSpace(instance.GetClusterId())
			if id == "" || cluster == "" {
				return nil, errors.New("local Foghorn inventory lacks its instance or media cluster identity")
			}
			if previous, exists := instances[id]; exists && previous != cluster {
				return nil, errors.New("local Foghorn has conflicting media cluster identities")
			}
			instances[id] = cluster
		}
		page := response.GetPagination()
		if !page.GetHasNextPage() {
			break
		}
		cursor := page.GetEndCursor()
		if cursor == "" || seenCursors[cursor] {
			return nil, errors.New("local service inventory has incomplete pagination")
		}
		seenCursors[cursor] = true
		pagination = &commonpb.CursorPaginationRequest{First: 200, After: &cursor}
	}
	scopes := map[string]bool{}
	ids := make([]string, 0, len(instances))
	for id := range instances {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	reader, ok := a.registryClient.(aliasAssignmentReader)
	if len(ids) > 0 && !ok {
		return nil, errors.New("local registry cannot resolve Foghorn serving assignments")
	}
	for _, id := range ids {
		response, err := reader.ListServiceClusterAssignments(ctx, id, foghornServiceType)
		if err != nil {
			return nil, fmt.Errorf("resolve Foghorn %s serving assignments: %w", id, err)
		}
		if response == nil {
			return nil, errors.New("foghorn assignments returned no authoritative response")
		}
		scopes[instances[id]] = true
		for _, cluster := range response.GetClusterIds() {
			if cluster = strings.TrimSpace(cluster); cluster != "" {
				scopes[cluster] = true
			}
		}
	}
	clusters := make([]string, 0, len(scopes))
	for cluster := range scopes {
		clusters = append(clusters, cluster)
	}
	sort.Strings(clusters)
	return clusters, nil
}
