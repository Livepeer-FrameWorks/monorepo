package grpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// reconcileMediaAuthorityPage distinguishes a cell below its acknowledged
// floor from normal applies whose acknowledgement has not arrived yet.
func (s *CommodoreServer) reconcileMediaAuthorityPage(ctx context.Context, req *commodorepb.RequestMediaAuthorityReplayRequest) (*commodorepb.RequestMediaAuthorityReplayResponse, error) {
	if req.GetAcknowledgedBefore() == nil || !req.GetAcknowledgedBefore().IsValid() {
		return nil, status.Error(codes.InvalidArgument, "inventory requires the preceding core clock watermark")
	}
	if (req.GetAfterKind() == "") != (req.GetAfterId() == "") ||
		(req.GetAfterKind() != "" && req.GetAfterKind() != "tenant" && req.GetAfterKind() != "media_object") ||
		len(req.GetAfterId()) > 255 || strings.ContainsRune(req.GetAfterId(), '\x00') {
		return nil, status.Error(codes.InvalidArgument, "invalid authority recovery cursor")
	}
	queries := commodoredb.New(s.db)
	observedAt, err := queries.MediaAuthorityRecoveryTime(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read recovery clock: %v", err)
	}
	if req.GetAcknowledgedBefore().AsTime().After(observedAt) {
		return nil, status.Error(codes.FailedPrecondition, "recovery watermark is ahead of the core database clock")
	}
	if len(req.GetHeld()) > sharedauthority.RecoveryPageSize || (len(req.GetHeld()) == 0 && !req.GetFinalPage()) {
		return nil, status.Error(codes.InvalidArgument, "invalid authority recovery page size")
	}
	type heldVersion struct {
		Kind    string `json:"authority_kind"`
		ID      string `json:"authority_id"`
		Version int64  `json:"authority_version"`
	}
	items := make([]heldVersion, 0, len(req.GetHeld()))
	lastKind, lastID := req.GetAfterKind(), req.GetAfterId()
	for _, item := range req.GetHeld() {
		kind, id := item.GetAuthorityKind(), item.GetAuthorityId()
		if (kind != "tenant" && kind != "media_object") || id == "" || len(id) > 255 || strings.ContainsRune(id, '\x00') || item.GetAuthorityVersion() <= 0 ||
			kind < lastKind || (kind == lastKind && id <= lastID) {
			return nil, status.Error(codes.InvalidArgument, "authority recovery page must contain ordered, unique identities and positive versions")
		}
		items = append(items, heldVersion{kind, id, item.GetAuthorityVersion()})
		lastKind, lastID = kind, id
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return nil, status.Error(codes.Internal, "encode authority inventory")
	}
	rows, err := queries.ReconcileMediaAuthorityPage(ctx, commodoredb.ReconcileMediaAuthorityPageParams{
		CellID: strings.TrimSpace(req.GetControlCellId()), AsOf: req.GetAsOf().AsTime(), Held: encoded,
		AfterKind: req.GetAfterKind(), AfterID: req.GetAfterId(), LastKind: lastKind, LastID: lastID, FinalPage: req.GetFinalPage(),
		AcknowledgedBefore: req.GetAcknowledgedBefore().AsTime(),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "compare authority inventory: %v", err)
	}
	response := &commodorepb.RequestMediaAuthorityReplayResponse{RecoveryProtocol: sharedauthority.RecoveryProtocol, SummaryChecked: true, HeldMatches: true, ObservedAt: timestamppb.New(observedAt)}
	regressed, raced := false, false
	for _, row := range rows {
		if !row.Known {
			return nil, status.Error(codes.FailedPrecondition, "cell holds authority absent from control-plane delivery history; reconcile the control-plane database")
		}
		regressed = regressed || row.Regressed
		raced = raced || row.Raced
		if row.Confirmed {
			response.Confirmed = append(response.Confirmed, &commodorepb.HeldMediaAuthority{
				AuthorityKind: row.AuthorityKind, AuthorityId: row.AuthorityID, AuthorityVersion: row.HeldVersion,
			})
		}
	}
	if regressed {
		count, err := s.replayRegressedMediaAuthorityCell(ctx, strings.TrimSpace(req.GetControlCellId()))
		if err != nil {
			return nil, err
		}
		response.HeldMatches, response.RequeuedCount = false, count
	} else if raced {
		response.SummaryChecked, response.HeldMatches = false, false
	}
	return response, nil
}

func (s *CommodoreServer) replayRegressedMediaAuthorityCell(ctx context.Context, cellID string) (int64, error) {
	var total int64
	err := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		queries := commodoredb.New(tx)
		if err := queries.MarkMediaAuthorityCellAcknowledgementsUntrusted(ctx, cellID); err != nil {
			return err
		}
		corrections, err := queries.EnqueueMediaAuthorityCorrectionsForCell(ctx, cellID)
		if err != nil {
			return err
		}
		count, err := queries.RequeueCurrentMediaAuthoritiesForCell(ctx, cellID)
		total = count + corrections
		return err
	})
	if err != nil {
		return 0, status.Errorf(codes.Internal, "replay regressed authority: %v", err)
	}
	return total, nil
}
