package grpc

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"frameworks/api_control/internal/database/commodoredb"
	foghornclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const vodUploadSessionType = "VodUploadSession"

func vodUploadSessionID(artifactHash, storageUploadID string) string {
	return globalid.Encode(vodUploadSessionType, artifactHash+":"+storageUploadID)
}

type vodUploadRoute struct {
	client          *foghornclient.GRPCClient
	clusterID       string
	artifactHash    string
	storageUploadID string
}

// The session carries a locator, not authority: tenant-owned catalog state
// selects the origin, and that origin validates its tenant-scoped multipart ID.
func (s *CommodoreServer) resolveVodUploadRoute(ctx context.Context, tenantID, sessionID string) (*vodUploadRoute, error) {
	if len(sessionID) > 16384 {
		return nil, status.Error(codes.InvalidArgument, "invalid upload session")
	}
	kind, value, ok := globalid.Decode(sessionID)
	artifactHash, storageUploadID, separated := strings.Cut(value, ":")
	if !ok || kind != vodUploadSessionType || !separated || artifactHash == "" || storageUploadID == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid upload session")
	}
	origin, err := commodoredb.New(s.db).GetVODOriginCluster(ctx, commodoredb.GetVODOriginClusterParams{
		TenantID: tenantID, VodHash: artifactHash,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "upload not found")
	}
	if err != nil {
		return nil, status.Error(codes.Unavailable, "upload owner unavailable")
	}
	if !origin.Valid || strings.TrimSpace(origin.String) == "" {
		return nil, status.Error(codes.Unavailable, "upload owner unavailable")
	}
	client, err := s.resolveFoghornForCluster(ctx, origin.String, tenantID)
	if err != nil {
		return nil, err
	}
	return &vodUploadRoute{client: client, clusterID: origin.String, artifactHash: artifactHash, storageUploadID: storageUploadID}, nil
}
