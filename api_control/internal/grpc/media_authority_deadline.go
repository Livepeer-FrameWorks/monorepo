package grpc

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"golang.org/x/sync/errgroup"
)

const (
	mediaAuthorityDeadlineTimeout = 12 * time.Second
	mediaAuthorityDeadlineLease   = 20 * time.Second
)

func mediaAuthorityDeadlineEvent(authorityKind, authorityID string, version int64) string {
	sum := sha256.Sum256([]byte(authorityKind + "\x00" + authorityID))
	return "authority-deadline:" + hex.EncodeToString(sum[:]) + ":" + strconv.FormatInt(version, 10)
}

func scheduleMediaObjectAuthorityRefresh(ctx context.Context, queries *commodoredb.Queries, payload *mediapb.MediaObjectAuthority, authorityID string, version int64, refreshAt time.Time) error {
	var kind, id string
	switch payload.GetObjectKind() {
	case mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM:
		kind, id = "live_stream", payload.GetLiveStream().GetStreamId()
	case mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT:
		id = payload.GetArtifact().GetArtifactId()
		switch payload.GetArtifact().GetArtifactKind() {
		case mediapb.ArtifactKind_ARTIFACT_KIND_CLIP:
			kind = "clip"
		case mediapb.ArtifactKind_ARTIFACT_KIND_DVR:
			kind = "dvr"
		case mediapb.ArtifactKind_ARTIFACT_KIND_VOD:
			kind = "vod"
		case mediapb.ArtifactKind_ARTIFACT_KIND_CHAPTER:
			kind = "chapter"
		}
	}
	reason := "media_object:" + kind + ":" + id + ":deadline_refresh"
	if kind == "" || id == "" || len(reason) > 255 || version <= 0 {
		return errors.New("invalid scheduled media authority identity")
	}
	rows, err := queries.ScheduleMediaAuthorityRefresh(ctx, commodoredb.ScheduleMediaAuthorityRefreshParams{SourceEventID: mediaAuthorityDeadlineEvent("media_object", authorityID, version), TenantID: payload.GetTenantId(), Reason: reason, NextAttemptAt: refreshAt})
	if err != nil || rows != 1 {
		return fmt.Errorf("schedule media authority renewal: rows=%d: %w", rows, err)
	}
	return nil
}

func (s *CommodoreServer) processMediaAuthorityDeadlineRefreshBatch(ctx context.Context) {
	if !s.mediaAuthorityEnabled() {
		return
	}
	rows, err := commodoredb.New(s.db).ClaimMediaAuthorityDeadlineRefresh(ctx, commodoredb.ClaimMediaAuthorityDeadlineRefreshParams{LeaseMs: mediaAuthorityDeadlineLease.Milliseconds(), BatchSize: mediaAuthorityRefreshBatch})
	if err != nil {
		s.logger.WithError(err).Warn("Failed to claim deadline media authority refreshes")
		return
	}
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(mediaAuthorityRefreshWorkers)
	for _, row := range rows {
		group.Go(func() error {
			rowCtx, cancel := context.WithTimeout(groupCtx, mediaAuthorityDeadlineTimeout)
			defer cancel()
			s.processMediaAuthorityDeadlineRefreshRow(rowCtx, commodoredb.ClaimMediaAuthorityRefreshInboxRow(row))
			return nil
		})
	}
	if err := group.Wait(); err != nil && ctx.Err() == nil {
		s.logger.WithError(err).Warn("Deadline authority refresh batch ended early")
	}
}

func (s *CommodoreServer) processMediaAuthorityDeadlineRefreshRow(ctx context.Context, row commodoredb.ClaimMediaAuthorityRefreshInboxRow) {
	kind, id, ok := parseMediaObjectRefreshReason(row.Reason)
	authorityKind := "media_object"
	authorityID := sharedauthority.ArtifactAuthorityID(id)
	if kind == "live_stream" {
		authorityID = sharedauthority.LiveStreamAuthorityID(id)
	}
	if row.Reason == tenantMediaAuthorityDeadlineReason {
		authorityKind, authorityID, ok = "tenant", row.TenantID, row.TenantID != ""
	}
	parts := strings.Split(row.SourceEventID, ":")
	var version int64
	var parseErr error
	if len(parts) == 3 {
		version, parseErr = strconv.ParseInt(parts[2], 10, 64)
	}
	if row.SourceService != "commodore" || !ok || !strings.HasSuffix(row.Reason, ":deadline_refresh") || parseErr != nil || version <= 0 || row.SourceEventID != mediaAuthorityDeadlineEvent(authorityKind, authorityID, version) {
		s.settleMediaAuthorityDeadlineRefresh(row, errors.New("invalid scheduled authority refresh binding"))
		return
	}
	current, err := commodoredb.New(s.db).GetScheduledMediaAuthorityVersion(ctx, commodoredb.GetScheduledMediaAuthorityVersionParams{TenantID: row.TenantID, AuthorityKind: authorityKind, AuthorityID: authorityID})
	if errors.Is(err, sql.ErrNoRows) || err == nil && current != version {
		s.settleMediaAuthorityDeadlineRefresh(row, nil)
		return
	}
	if err != nil {
		s.settleMediaAuthorityDeadlineRefresh(row, err)
		return
	}
	if authorityKind == "tenant" {
		compileCtx := context.WithValue(ctx, tenantMediaAuthorityDeadlineContextKey{}, true)
		compileErr := s.withMediaAuthorityCompileFence(compileCtx, "tenant:"+row.TenantID, func(fencedCtx context.Context) error {
			return s.compileTenantAuthority(fencedCtx, row.TenantID)
		})
		s.settleMediaAuthorityDeadlineRefresh(row, compileErr)
		return
	}
	s.processMediaAuthorityRefreshRow(ctx, row)
}

func (s *CommodoreServer) settleMediaAuthorityDeadlineRefresh(row commodoredb.ClaimMediaAuthorityRefreshInboxRow, cause error) {
	ctx, cancel := context.WithTimeout(context.Background(), mediaAuthoritySettleTimeout)
	defer cancel()
	if cause != nil {
		s.failMediaAuthorityRefresh(ctx, row, cause)
		return
	}
	rows, err := commodoredb.New(s.db).CompleteMediaAuthorityRefreshInbox(ctx, commodoredb.CompleteMediaAuthorityRefreshInboxParams{SourceService: row.SourceService, SourceEventID: row.SourceEventID})
	if err != nil || rows != 1 {
		s.logger.WithError(err).Warn("Failed to settle superseded scheduled authority refresh")
	}
}
