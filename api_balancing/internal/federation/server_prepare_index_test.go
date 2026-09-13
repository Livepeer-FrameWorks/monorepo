package federation

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
)

func TestPrepareArtifact_PublishedIndex(t *testing.T) {
	for _, tc := range []struct {
		name   string
		synced bool
		key    string
		want   bool
	}{
		{"published", true, "clips/tenant-a/index.att-current", true},
		{"pending", false, "clips/tenant-a/index.att-pending", false},
		{"absent", false, "", false},
		{"missing recorded key", true, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes", "authoritative_cluster", "recorded_object_key", "dtsh_synced", "dtsh_key"}).
				AddRow("clip-a", "stream-a", "clip", "mkv", "s3", "synced", 8192, nil, "clips/tenant-a/media.att-current", tc.synced, tc.key)
			mock.ExpectQuery("FROM foghorn.artifacts").WithArgs("hash", "tenant-a").WillReturnRows(rows)
			fake := &fakeS3Client{presignedGETResult: "https://storage.example/signed"}
			srv := NewFederationServer(FederationServerConfig{AllowFederationMutations: true, Logger: logging.NewLogger(), DB: db, S3Client: fake})
			resp, err := srv.PrepareArtifact(serviceAuthContext(), &foghornfederationpb.PrepareArtifactRequest{ArtifactId: "hash", TenantId: "tenant-a", ArtifactType: "clip"})
			if err != nil || !resp.GetReady() {
				t.Fatalf("prepare: %v, %v", resp, err)
			}
			if (resp.GetDtshUrl() != "") != tc.want {
				t.Fatalf("published index availability: %v", resp)
			}
			if tc.want {
				if len(fake.presignGETKeys) != 2 || fake.presignGETKeys[1] != tc.key {
					t.Fatalf("did not sign exact published index key: %v", fake.presignGETKeys)
				}
			} else if len(fake.presignGETKeys) != 1 {
				t.Fatalf("signed absent/unpublished index: %v", fake.presignGETKeys)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
