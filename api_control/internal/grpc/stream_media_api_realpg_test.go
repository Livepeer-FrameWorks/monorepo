//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestStreamMediaCatalog_RealPG(t *testing.T) { testStreamMediaCatalog(t, startCommodoreRealPG(t)) }
func TestStreamMediaCatalog_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "commodore_stream_media_api", "commodore")
	if !ok {
		t.Skip("requires the shared Yugabyte contract fixture")
	}
	testStreamMediaCatalog(t, db)
}
func testStreamMediaCatalog(t *testing.T, db *sql.DB) {
	t.Helper()
	server := &CommodoreServer{db: db, logger: logrus.New()}
	const tenant = "11111111-1111-1111-1111-111111111111"
	const stream = tenant
	ctx := context.WithValue(retentionAuthCtx(tenant), ctxkeys.KeyAuthType, "api_token")
	ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, []string{"streams:read"})
	if _, err := db.Exec(`INSERT INTO commodore.streams(id,tenant_id,user_id,stream_key,playback_id,internal_name,title) VALUES($1,$1,$1,'media-key','media-play','live+media','Media')`, tenant); err != nil {
		t.Fatal(err)
	}
	ids := []string{"20000000-0000-0000-0000-000000000001", "20000000-0000-0000-0000-000000000002", "20000000-0000-0000-0000-000000000003"}
	when := time.Unix(1800000000, 123456000).UTC()
	for _, id := range ids {
		hash := uuid.MustParse(id).String()[:8] + id[28:]
		if _, err := db.Exec(`INSERT INTO commodore.dvr_recordings(id,tenant_id,user_id,stream_id,dvr_hash,internal_name,playback_id,created_at,origin_cluster_id,stream_internal_name) VALUES($1::uuid,$2::uuid,$2::uuid,$2::uuid,$3::varchar,$3::varchar,$3::citext,$4,'origin','live+media')`, id, tenant, hash, when); err != nil {
			t.Fatal(err)
		}
	}
	first, err := server.ListStreamArtifacts(ctx, &pb.ListStreamArtifactsRequest{StreamId: stream, Kind: "dvr", Pagination: &commonpb.CursorPaginationRequest{First: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Artifacts) != 2 || first.Artifacts[0].Id != ids[2] || first.Artifacts[1].Id != ids[1] || !first.Pagination.HasNextPage || first.Pagination.HasPreviousPage || first.Pagination.TotalCount != 3 {
		t.Fatalf("first tied-time page: %v", first)
	}
	second, err := server.ListStreamArtifacts(ctx, &pb.ListStreamArtifactsRequest{StreamId: stream, Kind: "dvr", Pagination: &commonpb.CursorPaginationRequest{First: 2, After: first.Pagination.EndCursor}})
	if err != nil || len(second.Artifacts) != 1 || second.Artifacts[0].Id != ids[0] || second.Pagination.HasNextPage {
		t.Fatalf("second page: %v %v", second, err)
	}
	previous, err := server.ListStreamArtifacts(ctx, &pb.ListStreamArtifactsRequest{StreamId: stream, Kind: "dvr", Pagination: &commonpb.CursorPaginationRequest{Last: 2, Before: second.Pagination.StartCursor}})
	if err != nil || len(previous.Artifacts) != 2 || previous.Artifacts[0].Id != ids[2] || previous.Artifacts[1].Id != ids[1] {
		t.Fatalf("previous page: %v %v", previous, err)
	}
	hash := first.Artifacts[0].ArtifactHash
	request := &pb.UpdateArtifactCatalogSnapshotRequest{TenantId: tenant, AssetType: pb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_DVR, AssetKey: hash, SourceClusterId: proto.String("origin"), SourceRevision: 7}
	if _, err := server.UpdateArtifactCatalogSnapshot(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	request.RecordingTimesPresent = true
	request.StartedAt = timestamppb.New(when)
	request.EndedAt = timestamppb.New(when.Add(time.Hour))
	response, err := server.UpdateArtifactCatalogSnapshot(context.Background(), request)
	if err != nil || !response.GetRecordingTimesPresent() {
		t.Fatalf("capture time repair: %v %v", response, err)
	}
	recording, err := server.GetDVRRecording(ctx, &pb.GetDVRRecordingRequest{Id: ids[2]})
	if err != nil || !recording.GetStartedAt().AsTime().Equal(when) || !recording.GetEndedAt().AsTime().Equal(when.Add(time.Hour)) {
		t.Fatalf("catalog recording times: %v %v", recording, err)
	}

	legacy := proto.Clone(request).(*pb.UpdateArtifactCatalogSnapshotRequest)
	legacy.SourceRevision = 8
	legacy.RecordingTimesPresent = false
	legacy.StartedAt = nil
	legacy.EndedAt = nil
	if _, err := server.UpdateArtifactCatalogSnapshot(context.Background(), legacy); err != nil {
		t.Fatal(err)
	}
	request.StartedAt = timestamppb.New(when.Add(24 * time.Hour))
	request.EndedAt = timestamppb.New(when.Add(25 * time.Hour))
	if _, err := server.UpdateArtifactCatalogSnapshot(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	preserved, err := server.GetDVRRecording(ctx, &pb.GetDVRRecordingRequest{Id: ids[2]})
	if err != nil || !preserved.GetStartedAt().AsTime().Equal(when) {
		t.Fatalf("legacy/stale projection erased capture time: %v %v", preserved, err)
	}
	chapterHash := "catalog-chapter"
	if _, err := db.Exec(`INSERT INTO commodore.vod_assets(tenant_id,user_id,stream_id,vod_hash,internal_name,playback_id,filename,origin_type,origin_id,library_visible) VALUES($1::uuid,$1::uuid,$1::uuid,$2::varchar,$2::varchar,$2::citext,'chapter.mkv','dvr_chapter','chapter-id',false)`, tenant, chapterHash); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO commodore.dvr_chapter_playback(chapter_id,tenant_id,playback_id,artifact_hash,dvr_hash) VALUES('chapter-id',$1,'chapter-play',$2,$3)`, tenant, chapterHash, hash); err != nil {
		t.Fatal(err)
	}
	chapters, err := server.ListStorageArtifacts(ctx, &pb.ListStorageArtifactsRequest{StreamId: proto.String(stream), Kinds: []string{"chapter"}})
	if err != nil || len(chapters.GetArtifacts()) != 1 || chapters.Artifacts[0].GetParentRecordingId() != ids[2] || chapters.Artifacts[0].GetOriginType() != "dvr_chapter" {
		t.Fatalf("chapter parent catalog join: %v %v", chapters, err)
	}
	foreign := context.WithValue(ctx, ctxkeys.KeyTenantID, uuid.NewString())
	_, err = server.GetDVRRecording(foreign, &pb.GetDVRRecordingRequest{Id: ids[2]})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("foreign recording read: %v", err)
	}
}
