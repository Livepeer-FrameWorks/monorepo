//go:build schema_verify

package grpc

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
)

func TestMediaPlacementDeadlineRenewal_RealPG(t *testing.T) {
	db := startCommodoreRealPG(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	const streamID = "31000000-0000-4000-8000-000000000001"
	tenant, _ := quotedCommercialAuthorityFixture()
	tenant.OfficialClusterId = "official"
	if _, err := db.ExecContext(ctx, "INSERT INTO commodore.streams(id,tenant_id,user_id,stream_key,playback_id,internal_name,title) VALUES ($1,$2,'21000000-0000-4000-8000-000000000001','deadline-key','deadline-playback','deadline-internal','Deadline')", streamID, tenant.TenantId); err != nil {
		t.Fatal(err)
	}
	if err := seedCommercialTenantVersion(ctx, db, tenant, 1); err != nil {
		t.Fatal(err)
	}
	policyBytes, err := proto.Marshal(tenant.MediaPlacement)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO commodore.media_placement_policies(tenant_id,scope_kind,scope_id,revision,policy_payload) VALUES ($1,'tenant',$1,3,$2)", tenant.TenantId, policyBytes); err != nil {
		t.Fatal(err)
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	source := commercialSourceFunc(func(_ context.Context, req *pb.CommercialQuoteRequest) (*pb.CommercialQuoteResponse, error) {
		calls.Add(1)
		return commercialResponse(tenant, req)
	})
	server := &CommodoreServer{db: db, logger: logging.NewLogger(), authorityCommercialSource: source, mediaAuthorityKeyID: "deadline-key", mediaAuthorityPrivateKey: private}
	id := sharedauthority.LiveStreamAuthorityID(streamID)
	q := commodoredb.New(db)
	if err := server.compileLiveStreamAuthority(ctx, streamID); err != nil {
		t.Fatal(err)
	}
	claim := func() ([]commodoredb.ClaimMediaAuthorityDeadlineRefreshRow, error) {
		return q.ClaimMediaAuthorityDeadlineRefresh(ctx, commodoredb.ClaimMediaAuthorityDeadlineRefreshParams{BatchSize: 8, LeaseMs: mediaAuthorityDeadlineLease.Milliseconds()})
	}
	if future, err := claim(); err != nil || len(future) != 0 {
		t.Fatalf("future renewal claimed early: %+v %v", future, err)
	}
	ordinary, err := q.ClaimMediaAuthorityRefreshInbox(ctx, commodoredb.ClaimMediaAuthorityRefreshInboxParams{BatchSize: 8, LeaseMs: 1000})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range ordinary {
		if strings.HasPrefix(row.SourceEventID, "authority-deadline:") {
			t.Fatal("ordinary worker stole deadline renewal")
		}
	}
	makeDue := func(version int64) {
		t.Helper()
		if _, err := db.ExecContext(ctx, "UPDATE commodore.media_authority_refresh_inbox SET next_attempt_at=NOW()-INTERVAL '1 second' WHERE tenant_id=$1 AND source_service='commodore' AND source_event_id=$2", tenant.TenantId, mediaAuthorityDeadlineEvent("media_object", id, version)); err != nil {
			t.Fatal(err)
		}
	}
	checkVersion := func(want int64) {
		t.Helper()
		got, err := q.GetScheduledMediaAuthorityVersion(ctx, commodoredb.GetScheduledMediaAuthorityVersionParams{TenantID: tenant.TenantId, AuthorityKind: "media_object", AuthorityID: id})
		if err != nil || got != want {
			t.Fatalf("version=%d want=%d err=%v", got, want, err)
		}
		current, err := q.GetCurrentMediaAuthorityPayload(ctx, commodoredb.GetCurrentMediaAuthorityPayloadParams{AuthorityKind: "media_object", AuthorityID: id})
		if err != nil {
			t.Fatal(err)
		}
		payload := &mediapb.MediaObjectAuthority{}
		if err := proto.Unmarshal(current.Payload, payload); err != nil {
			t.Fatal(err)
		}
		if payload.SchemaVersion != 2 || payload.PlacementTenantRevision != 3 || len(payload.CommercialQuotes) != 2 {
			t.Fatal("renewal downgraded schema or lost policy/quotes")
		}
	}
	process := func(row commodoredb.ClaimMediaAuthorityDeadlineRefreshRow) {
		server.processMediaAuthorityDeadlineRefreshRow(ctx, commodoredb.ClaimMediaAuthorityRefreshInboxRow{SourceService: row.SourceService, SourceEventID: row.SourceEventID, TenantID: row.TenantID, Reason: row.Reason, Attempts: row.Attempts})
	}
	makeDue(1)
	var wait sync.WaitGroup
	claimed := make(chan []commodoredb.ClaimMediaAuthorityDeadlineRefreshRow, 2)
	failures := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() { defer wait.Done(); rows, err := claim(); claimed <- rows; failures <- err }()
	}
	wait.Wait()
	close(claimed)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	var rows []commodoredb.ClaimMediaAuthorityDeadlineRefreshRow
	for batch := range claimed {
		rows = append(rows, batch...)
	}
	if len(rows) != 1 {
		t.Fatalf("concurrent workers claimed %d renewals, want 1", len(rows))
	}
	process(rows[0])
	checkVersion(2)
	if calls.Load() != 4 {
		t.Fatalf("renewal did not refresh both quotes: %d", calls.Load())
	}
	makeDue(2)
	if err := server.compileLiveStreamAuthority(ctx, streamID); err != nil {
		t.Fatal(err)
	}
	checkVersion(3)
	stale, err := claim()
	if err != nil || len(stale) != 1 {
		t.Fatalf("stale job claim: %+v %v", stale, err)
	}
	process(stale[0])
	if calls.Load() != 6 {
		t.Fatal("stale version spawned another renewal chain")
	}
	checkVersion(3)
	var pending int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM commodore.media_authority_refresh_inbox WHERE tenant_id=$1 AND source_service='commodore' AND source_event_id LIKE 'authority-deadline:%' AND status<>'completed'", tenant.TenantId).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("pending renewal chains=%d err=%v", pending, err)
	}
	if _, err := q.GetScheduledMediaAuthorityVersion(ctx, commodoredb.GetScheduledMediaAuthorityVersionParams{TenantID: "81000000-0000-4000-8000-000000000002", AuthorityKind: "media_object", AuthorityID: id}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-tenant scheduled authority read: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CREATE FUNCTION commodore.reject_deadline_test() RETURNS trigger LANGUAGE plpgsql AS 'BEGIN IF NEW.source_event_id LIKE ''authority-deadline:%'' THEN RAISE EXCEPTION ''injected scheduling failure''; END IF; RETURN NEW; END'; CREATE TRIGGER reject_deadline_test BEFORE INSERT ON commodore.media_authority_refresh_inbox FOR EACH ROW EXECUTE FUNCTION commodore.reject_deadline_test()"); err != nil {
		t.Fatal(err)
	}
	if err := server.compileLiveStreamAuthority(ctx, streamID); err == nil {
		t.Fatal("failed renewal schedule still published authority")
	}
	checkVersion(3)
}
