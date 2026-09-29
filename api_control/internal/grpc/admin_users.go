package grpc

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// AdminLookupUserByEmail resolves an email address to its user and tenant for
// operator tooling that addresses a tenant by its owner's email. It is an
// explicit cross-tenant read: emails are unique across tenants, and only an
// operator JWT reaches the query.
func (s *CommodoreServer) AdminLookupUserByEmail(ctx context.Context, req *commodorepb.AdminLookupUserByEmailRequest) (*commodorepb.AdminLookupUserByEmailResponse, error) {
	if err := middleware.RequirePlatformOperatorJWT(ctx); err != nil {
		return nil, err
	}
	email := normalizeEmail(req.GetEmail())
	if email == "" {
		return nil, status.Error(codes.InvalidArgument, "email is required")
	}
	if !strings.Contains(email, "@") {
		return nil, status.Errorf(codes.InvalidArgument, "%q is not an email address", email)
	}
	user, err := commodoredb.New(s.db).AdminGetUserByEmail(ctx, email)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Errorf(codes.NotFound, "no user has email %s", email)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	return &commodorepb.AdminLookupUserByEmailResponse{
		UserId:   user.ID,
		TenantId: user.TenantID,
		Email:    user.Email,
		Role:     user.Role,
		IsActive: user.IsActive,
		Verified: user.Verified,
	}, nil
}
