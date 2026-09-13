package grpc

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	foghornclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *CommodoreServer) resolveDVRStartOwner(ctx context.Context, tenantID, streamID string) (*foghornclient.GRPCClient, string, error) {
	row, err := commodoredb.New(s.db).GetDVRSourceRoute(ctx, commodoredb.GetDVRSourceRouteParams{ID: streamID, TenantID: tenantID})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", status.Error(codes.NotFound, "stream not found")
	}
	if err != nil {
		return nil, "", status.Error(codes.Unavailable, "recording source authority unavailable")
	}
	clusterID, fresh := selectActiveIngestCluster(row.ActiveIngestClusterID, row.ActiveIngestClusterUpdatedAt, time.Now())
	if !fresh {
		return nil, "", status.Error(codes.FailedPrecondition, "stream has no active ingest owner")
	}
	client, err := s.resolveFoghornForCluster(ctx, clusterID, tenantID)
	return client, clusterID, err
}
