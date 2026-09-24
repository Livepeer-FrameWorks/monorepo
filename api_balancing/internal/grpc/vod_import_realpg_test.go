//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"testing"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/database/foghorndb"
	"frameworks/api_balancing/internal/storage"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"github.com/google/uuid"
)

// An import is recorded in one transaction as a 'processing' VOD whose source
// is the tenant URL: a normal process job with no job source (Mist reads the
// relay), upload.created and upload.completed keyed by the artifact ID, and
// the relay queries resolving the source. A retry records nothing new.
func TestVodImportRecordsSourceAndQueuesProcessing_RealPG(t *testing.T) { //nolint:funlen // One database follows the whole accept.
	conn := startFoghornGRPCRealPG(t)
	t.Cleanup(control.SetupTestRegistry("", nil))
	srv := NewFoghornGRPCServer(conn, logging.NewLogger(), nil, nil, nil, nil, &fakeVodS3Client{}, nil)
	srv.SetClusterID("central-primary")
	srv.SetQuartermasterClient(&mockQMRouting{clusterID: "central-primary"})
	srv.SetStorageResolverFactory(func(_ context.Context, _ string) *storage.ClusterResolver {
		return &storage.ClusterResolver{LocalClusterID: "central-primary", LocalS3ClientPresent: true}
	})
	ctx := context.Background()
	tenantID, userID := uuid.NewString(), uuid.NewString()
	const (
		hash     = "vodimport00000000000000000000001"
		playback = "pbimport0001"
		source   = "https://cdn.example/talks/keynote.mp4?sig=abc"
	)
	hashArg, playbackArg, internal, requestID := hash, playback, "vodimportinternal0001", uuid.NewString()
	req := &sharedpb.ImportVodAssetRequest{
		TenantId: tenantID, UserId: userID, SourceUrl: source, Filename: "keynote.mp4",
		VodHash: &hashArg, PlaybackId: &playbackArg, InternalName: &internal,
		ClusterId: "central-primary", RequestId: &requestID,
	}

	resp, err := srv.ImportVodAsset(ctx, req)
	if err != nil {
		t.Fatalf("ImportVodAsset: %v", err)
	}
	if resp.GetAsset().GetArtifactHash() != hash {
		t.Fatalf("asset = %+v", resp.GetAsset())
	}

	var status, format string
	var s3URL sql.NullString
	if err := conn.QueryRow(`SELECT status, format, s3_url FROM foghorn.artifacts WHERE artifact_hash = $1`, hash).
		Scan(&status, &format, &s3URL); err != nil {
		t.Fatal(err)
	}
	if status != "processing" || format != "mp4" || s3URL.Valid {
		t.Fatalf("artifact = %s/%s/%v, want processing/mp4 with no stored object", status, format, s3URL)
	}

	var jobType string
	var jobSource sql.NullString
	if err := conn.QueryRow(`SELECT job_type, source_url FROM foghorn.processing_jobs WHERE artifact_hash = $1`, hash).Scan(&jobType, &jobSource); err != nil {
		t.Fatal(err)
	}
	// A job source would be handed to Mist directly (resolveProcessSource),
	// bypassing the relay and its public-destination client.
	if jobType != "process" || jobSource.Valid {
		t.Fatalf("processing job = %s source=%v, want a process job without a job source", jobType, jobSource)
	}

	queries := foghorndb.New(conn)
	relayRow, err := queries.GetRelayVodMetadata(ctx, hash)
	if err != nil || relayRow.SourceUrl.String != source || relayRow.TenantID.String != tenantID {
		t.Fatalf("relay metadata = %+v, %v", relayRow, err)
	}
	if uploadFormat, err := queries.UploadedArtifactFormat(ctx, hash); err != nil || uploadFormat != "mp4" {
		t.Fatalf("processing input format = %q, %v; the relay upload URL needs it", uploadFormat, err)
	}

	for _, eventType := range []string{"upload.created", "upload.completed"} {
		var payload []byte
		if err := conn.QueryRow(`SELECT payload FROM foghorn.domain_event_outbox WHERE event_type = $1 AND aggregate_id = $2`, eventType, hash).Scan(&payload); err != nil {
			t.Fatalf("%s: %v", eventType, err)
		}
		_, msg, err := events.Decode(eventType, payload)
		if err != nil {
			t.Fatal(err)
		}
		if got := msg.(interface{ GetArtifact() *publicv1.Artifact }).GetArtifact().GetArtifactId(); got != hash {
			t.Fatalf("%s artifact ID = %q, want %q", eventType, got, hash)
		}
	}

	// A retry after a lost response records nothing new.
	if _, err := srv.ImportVodAsset(ctx, req); err != nil {
		t.Fatalf("retried ImportVodAsset: %v", err)
	}
	var jobs, created int
	if err := conn.QueryRow(`SELECT count(*) FROM foghorn.processing_jobs WHERE artifact_hash = $1`, hash).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(`SELECT count(*) FROM foghorn.domain_event_outbox WHERE event_type = 'upload.created' AND aggregate_id = $1`, hash).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 || created != 1 {
		t.Fatalf("after a retry: %d jobs, %d upload.created; want 1 and 1", jobs, created)
	}
}
