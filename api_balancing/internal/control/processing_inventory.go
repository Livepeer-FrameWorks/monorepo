package control

import (
	"context"
	"fmt"
	"time"

	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
)

// RegisteredProcessingNodes includes disconnected edges. A missing runtime report
// is unknown capacity, rather than evidence that the cluster can never fit a job.
func RegisteredProcessingNodes(ctx context.Context, clusterID string) ([]string, error) {
	if quartermasterClient == nil {
		return nil, fmt.Errorf("processing inventory unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	page := &commonpb.CursorPaginationRequest{First: 100}
	var ids []string
	for {
		response, listErr := quartermasterClient.ListNodes(ctx, clusterID, "edge", "", page)
		if listErr != nil {
			return nil, listErr
		}
		for _, node := range response.GetNodes() {
			if node.GetClusterId() == clusterID && node.GetNodeId() != "" && node.GetStatus() != "retired" && node.GetStatus() != "evicted" {
				ids = append(ids, node.GetNodeId())
			}
		}
		pagination := response.GetPagination()
		if !pagination.GetHasNextPage() {
			return ids, nil
		}
		cursor := pagination.GetEndCursor()
		if cursor == "" || cursor == page.GetAfter() {
			return nil, fmt.Errorf("processing inventory pagination did not advance")
		}
		page.After = &cursor
	}
}
