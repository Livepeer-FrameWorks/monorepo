package grpc

import (
	"context"
	"sort"
	"strings"

	quartermasterdb "frameworks/api_tenants/internal/database/quartermasterdb"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *QuartermasterServer) GetTenantControlRecipients(ctx context.Context, req *quartermasterpb.GetTenantControlRecipientsRequest) (*quartermasterpb.GetTenantControlRecipientsResponse, error) {
	if err := requireServiceOrPlatformOperator(ctx, "GetTenantControlRecipients"); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.GetTenantId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}
	rows, err := quartermasterdb.New(s.db).ListTenantControlRecipients(ctx, req.GetTenantId(), req.GetHistoricalClusterIds())
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "control recipient inventory: %v", err)
	}
	response := &quartermasterpb.GetTenantControlRecipientsResponse{Complete: true}
	byCell := map[string]*quartermasterpb.TenantControlRecipient{}
	for _, row := range rows {
		if row.CellID == "" {
			response.Complete = false
			response.UnresolvedClusterIds = append(response.UnresolvedClusterIds, row.ClusterID)
			continue
		}
		if byCell[row.CellID] == nil {
			byCell[row.CellID] = &quartermasterpb.TenantControlRecipient{CellId: row.CellID}
		}
		byCell[row.CellID].SourceClusterIds = append(byCell[row.CellID].SourceClusterIds, row.ClusterID)
	}
	keys := make([]string, 0, len(byCell))
	for cell := range byCell {
		keys = append(keys, cell)
	}
	sort.Strings(keys)
	for _, cell := range keys {
		response.Recipients = append(response.Recipients, byCell[cell])
	}
	return response, nil
}
