package federation

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
)

func TestPrepareArtifact_ServedPrivateOriginStorage(t *testing.T) {
	local := S3Backing{Bucket: "cell-a", Endpoint: "https://storage-a.example", Region: "us-east-1", Prefix: "media/"}
	for _, artifact := range []struct{ stored, requested string }{{"clip", "clip"}, {"vod", "chapter"}} {
		for _, tc := range []struct {
			name       string
			served     bool
			advertised S3Backing
			advertises bool
			noClient   bool
			wantReady  bool
		}{
			{name: "served private origin uses cell storage", served: true, wantReady: true},
			{name: "served private origin has empty advertisement", served: true, advertises: true, wantReady: true},
			{name: "matching dedicated backing", served: true, advertised: local, advertises: true, wantReady: true},
			{name: "foreign private origin cannot use cell storage"},
			{name: "different bucket remains remote", served: true, advertised: S3Backing{Bucket: "other"}, advertises: true},
			{name: "different endpoint remains remote", served: true, advertised: S3Backing{Bucket: local.Bucket, Endpoint: "https://other.example", Region: local.Region, Prefix: local.Prefix}, advertises: true},
			{name: "different prefix remains remote", served: true, advertised: S3Backing{Bucket: local.Bucket, Endpoint: local.Endpoint, Region: local.Region, Prefix: "other/"}, advertises: true},
			{name: "served origin without an S3 client is unavailable", served: true, noClient: true},
		} {
			t.Run(artifact.requested+"/"+tc.name, func(t *testing.T) {
				db, mock, err := sqlmock.New()
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				mock.ExpectQuery("FROM foghorn.artifacts").WithArgs("hash-private", "tenant-a").WillReturnRows(
					sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes", "authoritative_cluster", "recorded_object_key", "dtsh_synced", "dtsh_key"}).
						AddRow("artifact-private", "stream-private", artifact.stored, "mkv", "s3", "synced", 8192, "private-edge", "recorded/media.att-current", true, "recorded/index.att-current"))
				fake := &fakeS3Client{presignedGETResult: "https://storage-a.example/signed"}
				cfg := FederationServerConfig{
					AllowFederationMutations: true,
					Logger:                   logging.NewLogger(),
					ClusterID:                "cell-a",
					DB:                       db,
					LocalS3Backing:           local,
					IsServedCluster:          func(id string) bool { return tc.served && id == "private-edge" },
					AdvertisedBacking: func(_ context.Context, tenant, cluster string) (S3Backing, bool) {
						if tenant != "tenant-a" || cluster != "private-edge" {
							t.Fatalf("backing lookup escaped tenant/cluster: %q/%q", tenant, cluster)
						}
						return tc.advertised, tc.advertises
					},
				}
				if !tc.noClient {
					cfg.S3Client = fake
				}
				server := NewFederationServer(cfg)
				response, err := server.PrepareArtifact(serviceAuthContext(), &foghornfederationpb.PrepareArtifactRequest{
					ArtifactId: "hash-private", ArtifactType: artifact.requested, TenantId: "tenant-a", RequestingCluster: "cell-b",
				})
				if err != nil {
					t.Fatal(err)
				}
				if response.GetReady() != tc.wantReady {
					t.Fatalf("ready=%v, redirect=%q, want ready=%v", response.GetReady(), response.GetRedirectClusterId(), tc.wantReady)
				}
				if tc.wantReady {
					if response.GetRedirectClusterId() != "" || response.GetUrl() == "" || response.GetDtshUrl() == "" {
						t.Fatalf("served private origin did not return media and index: %v", response)
					}
					if len(fake.presignGETKeys) != 2 || fake.presignGETKeys[0] != "recorded/media.att-current" || fake.presignGETKeys[1] != "recorded/index.att-current" {
						t.Fatalf("did not sign the recorded objects: %v", fake.presignGETKeys)
					}
				} else if response.GetRedirectClusterId() != "private-edge" || len(fake.presignGETKeys) != 0 {
					t.Fatalf("unowned storage minted URLs: %v", response)
				}
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
