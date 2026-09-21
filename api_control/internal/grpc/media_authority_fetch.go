package grpc

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

const (
	// A fetch compiles on its own clock. The cell that asked stops waiting after a
	// couple of seconds, but the version it caused is still delivered to it and
	// to every other cell, so the work is never abandoned half way.
	mediaAuthorityFetchTimeout = 20 * time.Second
	mediaAuthorityUseBatchMax  = 2000
)

// FetchMediaAuthority returns the current valid object and tenant envelopes
// signed for the asking cell, compiling a missing or expired pair. A cell calls it for an authority it
// does not hold, or holds past its validity: an object nobody has used for a
// while is kept in no cell, and asking for it is what brings it back. Being
// asked for is use, so the object and its tenant are renewed again from here.
func (s *CommodoreServer) FetchMediaAuthority(ctx context.Context, req *commodorepb.FetchMediaAuthorityRequest) (*commodorepb.FetchMediaAuthorityResponse, error) {
	if ctxkeys.GetAuthType(ctx) != "service" {
		return nil, status.Error(codes.PermissionDenied, "media authority fetch requires service authentication")
	}
	if !s.mediaAuthorityEnabled() {
		return nil, status.Error(codes.Unavailable, "media authority compiler is not configured")
	}
	cellID := strings.TrimSpace(req.GetControlCellId())
	if cellID == "" {
		return nil, status.Error(codes.InvalidArgument, "control_cell_id is required")
	}
	identity, err := s.resolveFetchedMediaAuthority(ctx, req)
	if err != nil {
		return nil, err
	}
	queries := commodoredb.New(s.db)
	params := commodoredb.ListCurrentMediaAuthorityEnvelopesForCellParams{CellID: cellID, TenantID: identity.tenantID, ObjectAuthorityID: identity.authorityID}
	rows, err := queries.ListCurrentMediaAuthorityEnvelopesForCell(ctx, params)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load current media authority: %v", err)
	}
	want := 1
	if identity.authorityID != "" {
		want = 2
	}
	if len(rows) == want {
		kind, id := "tenant", identity.tenantID
		if identity.authorityID != "" {
			kind, id = "media_object", identity.authorityID
		}
		if err = s.recordMediaAuthorityUse(ctx, kind, id, identity.tenantID); err != nil {
			return nil, status.Errorf(codes.Internal, "record fetched authority use: %v", err)
		}
		response := &commodorepb.FetchMediaAuthorityResponse{}
		for _, row := range rows {
			response.SignedAuthorities = append(response.SignedAuthorities, row.SignedEnvelope)
		}
		return response, nil
	}
	compileCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mediaAuthorityFetchTimeout)
	defer cancel()
	compileErr := s.compileFreshMediaAuthority(compileCtx, identity.flightKey(), func() error {
		return s.compileFetchedMediaAuthority(compileCtx, identity)
	})
	if compileErr != nil {
		class, _ := classifyAuthorityCompileError(compileErr)
		switch class {
		case authorityCompileSuperseded:
			return nil, status.Error(codes.Aborted, "media authority compilation was overtaken; retry this authority")
		case authorityCompilePark, authorityCompileAwaitTenant:
			return nil, status.Error(codes.FailedPrecondition, "media authority requires source or configuration correction")
		}
		s.logger.WithError(compileErr).WithFields(map[string]any{"cell_id": cellID, "tenant_id": identity.tenantID, "authority_id": identity.authorityID}).
			Warn("Failed to compile a fetched media authority")
		return nil, status.Error(codes.Unavailable, "media authority could not be compiled")
	}
	rows, err = queries.ListCurrentMediaAuthorityEnvelopesForCell(ctx, params)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load fetched media authority: %v", err)
	}
	response := &commodorepb.FetchMediaAuthorityResponse{}
	for _, row := range rows {
		response.SignedAuthorities = append(response.SignedAuthorities, row.SignedEnvelope)
	}
	return response, nil
}

// A fetch can clear a cell's trust barrier, so it cannot reuse a compile that
// began before the request. Wait out such an in-flight compile, then join or
// start its successor. All callers of that successor still share its work.
func (s *CommodoreServer) compileFreshMediaAuthority(ctx context.Context, key string, compile func() error) error {
	requestedAt := time.Now()
	contentionRetries := 0
	for {
		result := s.mediaAuthorityFetch.DoChan(key, func() (any, error) {
			startedAt := time.Now()
			return startedAt, compile()
		})
		var started any
		var err error
		select {
		case <-ctx.Done():
			return ctx.Err()
		case outcome := <-result:
			started, err = outcome.Val, outcome.Err
		}
		startedAt, ok := started.(time.Time)
		if !ok {
			return errors.New("media authority compile lacks freshness evidence")
		}
		if !startedAt.Before(requestedAt) {
			if errors.Is(err, errMediaAuthorityCompileSuperseded) && contentionRetries < 2 {
				contentionRetries++
				continue
			}
			return err
		}
	}
}

// fetchedMediaAuthority names what a cell asked for. authorityID is empty for a
// fetch of the tenant authority alone.
type fetchedMediaAuthority struct {
	tenantID    string
	targetKind  string
	objectID    string
	authorityID string
}

func (f fetchedMediaAuthority) flightKey() string { return f.tenantID + "\x00" + f.authorityID }

func (s *CommodoreServer) resolveFetchedMediaAuthority(ctx context.Context, req *commodorepb.FetchMediaAuthorityRequest) (fetchedMediaAuthority, error) {
	queries := commodoredb.New(s.db)
	notFound := status.Error(codes.NotFound, "media object not found")
	switch lookup := req.GetLookup().(type) {
	case *commodorepb.FetchMediaAuthorityRequest_TenantId:
		tenantID := strings.TrimSpace(lookup.TenantId)
		if _, err := uuid.Parse(tenantID); err != nil {
			return fetchedMediaAuthority{}, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
		}
		return fetchedMediaAuthority{tenantID: tenantID}, nil
	case *commodorepb.FetchMediaAuthorityRequest_AuthorityId:
		target := mediaObjectAuthorityTarget(strings.TrimSpace(lookup.AuthorityId))
		objectID := target.ObjectID()
		if _, err := uuid.Parse(objectID); err != nil {
			return fetchedMediaAuthority{}, status.Error(codes.InvalidArgument, "authority_id does not name a media object")
		}
		tenantID, err := s.fetchedMediaObjectTenant(ctx, queries, target.Kind, objectID)
		if errors.Is(err, sql.ErrNoRows) {
			return fetchedMediaAuthority{}, notFound
		}
		if err != nil {
			return fetchedMediaAuthority{}, status.Errorf(codes.Internal, "resolve media object: %v", err)
		}
		return fetchedMediaAuthority{tenantID: tenantID, targetKind: target.Kind, objectID: objectID, authorityID: strings.TrimSpace(lookup.AuthorityId)}, nil
	case *commodorepb.FetchMediaAuthorityRequest_PlaybackId:
		row, err := queries.ResolveMediaAuthorityIdentityByPlaybackID(ctx, strings.TrimSpace(lookup.PlaybackId))
		if errors.Is(err, sql.ErrNoRows) {
			return fetchedMediaAuthority{}, notFound
		}
		if err != nil {
			return fetchedMediaAuthority{}, status.Errorf(codes.Internal, "resolve media object: %v", err)
		}
		return newFetchedMediaObject(row.TargetKind, row.ObjectID, row.TenantID), nil
	case *commodorepb.FetchMediaAuthorityRequest_InternalName:
		row, err := queries.ResolveMediaAuthorityIdentityByInternalName(ctx, strings.TrimSpace(lookup.InternalName))
		if errors.Is(err, sql.ErrNoRows) {
			return fetchedMediaAuthority{}, notFound
		}
		if err != nil {
			return fetchedMediaAuthority{}, status.Errorf(codes.Internal, "resolve media object: %v", err)
		}
		return newFetchedMediaObject(row.TargetKind, row.ObjectID, row.TenantID), nil
	default:
		return fetchedMediaAuthority{}, status.Error(codes.InvalidArgument, "a lookup is required")
	}
}

func newFetchedMediaObject(targetKind, objectID, tenantID string) fetchedMediaAuthority {
	authorityID := sharedauthority.ArtifactAuthorityID(objectID)
	if targetKind == commodoredb.MediaAuthorityTargetLiveStream {
		authorityID = sharedauthority.LiveStreamAuthorityID(objectID)
	}
	return fetchedMediaAuthority{tenantID: tenantID, targetKind: targetKind, objectID: objectID, authorityID: authorityID}
}

func (s *CommodoreServer) fetchedMediaObjectTenant(ctx context.Context, queries *commodoredb.Queries, targetKind, objectID string) (string, error) {
	if targetKind == commodoredb.MediaAuthorityTargetLiveStream {
		source, err := queries.GetLiveStreamMediaAuthoritySource(ctx, objectID)
		return source.TenantID, err
	}
	source, err := queries.GetArtifactMediaAuthoritySource(ctx, objectID)
	return source.TenantID, err
}

// compileFetchedMediaAuthority records the use, compiles the tenant when its
// current version has lapsed (the object compile builds on that version, and
// an older version a cell may still hold does not help it), then compiles the
// object. Both
// run as in use, under the same fences as the obligation workers. Losing a
// fence means a newer compile of the same authority is already publishing.
func (s *CommodoreServer) compileFetchedMediaAuthority(ctx context.Context, identity fetchedMediaAuthority) error {
	queries := commodoredb.New(s.db)
	kind, id := "tenant", identity.tenantID
	if identity.authorityID != "" {
		kind, id = "media_object", identity.authorityID
	}
	if err := s.recordMediaAuthorityUse(ctx, kind, id, identity.tenantID); err != nil {
		return err
	}
	ctx = withMediaAuthorityInUse(ctx, true)
	tenantLapsed, err := mediaAuthorityCurrentLapsed(ctx, queries, "tenant", identity.tenantID, time.Now().UTC())
	if err != nil {
		return err
	}
	if tenantLapsed || identity.authorityID == "" {
		tenantTarget := commodoredb.TenantMediaAuthorityTarget(identity.tenantID)
		if err = s.withMediaAuthorityCompileFence(ctx, tenantTarget.Key, func(compileCtx context.Context) error {
			return s.compileTenantAuthority(compileCtx, identity.tenantID)
		}); err != nil {
			return err
		}
	}
	if identity.authorityID == "" {
		return nil
	}
	objectTarget := mediaObjectAuthorityTarget(identity.authorityID)
	err = s.withMediaAuthorityCompileFence(ctx, objectTarget.Key, func(compileCtx context.Context) error {
		if identity.targetKind == commodoredb.MediaAuthorityTargetLiveStream {
			return s.compileLiveStreamAuthority(compileCtx, identity.objectID)
		}
		return s.compileArtifactAuthority(compileCtx, identity.objectID)
	})
	return err
}

// ReportMediaAuthorityUse records the authorities a cell decided on. A cell
// reports each at most once a day, and a row is written at most once a day, so
// the cost follows the number of objects in use, not the number of decisions.
func (s *CommodoreServer) ReportMediaAuthorityUse(ctx context.Context, req *commodorepb.ReportMediaAuthorityUseRequest) (*emptypb.Empty, error) {
	if ctxkeys.GetAuthType(ctx) != "service" {
		return nil, status.Error(codes.PermissionDenied, "media authority use requires service authentication")
	}
	if strings.TrimSpace(req.GetControlCellId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "control_cell_id is required")
	}
	if len(req.GetUses()) > mediaAuthorityUseBatchMax {
		return nil, status.Errorf(codes.InvalidArgument, "at most %d uses per report", mediaAuthorityUseBatchMax)
	}
	for _, use := range req.GetUses() {
		kind, id, tenantID := strings.TrimSpace(use.GetAuthorityKind()), strings.TrimSpace(use.GetAuthorityId()), strings.TrimSpace(use.GetTenantId())
		if (kind != "tenant" && kind != "media_object") || id == "" || len(id) > 255 {
			continue
		}
		if _, err := uuid.Parse(tenantID); err != nil {
			continue
		}
		if err := s.recordMediaAuthorityUse(ctx, kind, id, tenantID); err != nil {
			return nil, status.Errorf(codes.Internal, "record media authority use: %v", err)
		}
	}
	return &emptypb.Empty{}, nil
}
