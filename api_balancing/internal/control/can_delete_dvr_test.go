package control

import (
	"context"
	"strings"
	"testing"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"github.com/DATA-DOG/go-sqlmock"
)

// canDeleteForSyncedDVR runs a CanDelete for a finalized, synced DVR whose
// catalog s3_url is the recording prefix, with the segment ledger reporting
// uploadedSegments and S3 holding the given object keys.
func canDeleteForSyncedDVR(t *testing.T, uploadedSegments int, s3Keys []string) *ipcpb.CanDeleteResponse {
	t.Helper()
	const prefix = "dvr/tenant-1/live+stream/dvrhash0123456789"
	prevRepo, prevS3, prevDB := artifactRepo, s3Client, db
	artifactRepo = &mockArtifactRepo{
		getArtifactSyncInfoFn: func(_ context.Context, hash string) (*state.ArtifactSyncInfo, error) {
			return &state.ArtifactSyncInfo{
				ArtifactHash:    hash,
				ArtifactType:    "dvr",
				LifecycleStatus: "completed",
				SyncStatus:      "synced",
				S3URL:           "s3://frameworks/" + prefix,
			}, nil
		},
	}
	s3Client = &mockS3Client{
		listPrefixFn: func(_ context.Context, p string) ([]string, error) {
			var out []string
			for _, k := range s3Keys {
				if strings.HasPrefix(k, p) {
					out = append(out, k)
				}
			}
			return out, nil
		},
	}
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(`FROM foghorn.dvr_segments`).
		WithArgs("dvrhash0123456789").
		WillReturnRows(sqlmock.NewRows([]string{"uploaded_count", "lost_count"}).AddRow(uploadedSegments, 0))
	db = mockDB
	t.Cleanup(func() {
		artifactRepo, s3Client, db = prevRepo, prevS3, prevDB
		_ = mockDB.Close()
	})

	stream := &fakeControlStream{}
	processCanDeleteRequest(&ipcpb.CanDeleteRequest{AssetHash: "dvrhash0123456789", NodeId: "node-1"}, "node-1", stream, logging.NewLogger())
	return stream.sent[0].GetCanDeleteResponse()
}

// A finalized DVR's durable copy is its segment objects under the recording
// prefix; no object equals the prefix itself. CanDelete must approve the local
// copy once S3 holds every uploaded ledger segment.
func TestProcessCanDelete_DVRPrefixVerifiedBySegmentObjects(t *testing.T) {
	resp := canDeleteForSyncedDVR(t, 2, []string{
		"dvr/tenant-1/live+stream/dvrhash0123456789/segments/seg-0001.ts",
		"dvr/tenant-1/live+stream/dvrhash0123456789/segments/seg-0002.ts",
	})
	if resp == nil || !resp.GetSafeToDelete() || resp.GetReason() != "synced_verified" {
		t.Fatalf("response=%+v, want safe synced_verified", resp)
	}
}

func TestProcessCanDelete_DVRRejectedWhenUploadedSegmentMissingFromS3(t *testing.T) {
	resp := canDeleteForSyncedDVR(t, 3, []string{
		"dvr/tenant-1/live+stream/dvrhash0123456789/segments/seg-0001.ts",
		"dvr/tenant-1/live+stream/dvrhash0123456789/segments/seg-0002.ts",
		// A sibling recording whose hash shares the prefix must not count.
		"dvr/tenant-1/live+stream/dvrhash0123456789x/segments/seg-0003.ts",
	})
	if resp == nil || resp.GetSafeToDelete() || resp.GetReason() != "s3_object_missing" {
		t.Fatalf("response=%+v, want unsafe s3_object_missing", resp)
	}
}
