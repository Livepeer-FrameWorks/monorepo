package grpc

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type originalStore struct {
	fakeVodS3Client
	present       bool
	size          int64
	key, filename string
	expiry        time.Duration
}

func (s *originalStore) HeadObjectInfo(context.Context, string) (bool, int64, string, error) {
	return s.present, s.size, "etag", nil
}
func (s *originalStore) GeneratePresignedGETAttachment(_ context.Context, key, filename string, expiry time.Duration) (string, error) {
	s.key, s.filename, s.expiry = key, filename, expiry
	return "https://store.example/original?signature=private", nil
}
func originalRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"status", "active_object_key", "durable_backend_local", "backend_id", "original_s3_key", "original_size_bytes", "filename"})
}
func TestVodOriginalDownloadIsTenantScopedAndUsesAnAttachment(t *testing.T) {
	for _, name := range []string{"kept", "other tenant", "not kept", "wrong backend", "missing object", "changed size"} {
		t.Run(name, func(t *testing.T) {
			server, mock, done := newStatusServer(t, &fakeVodS3Client{})
			defer done()
			store := &originalStore{present: true, size: 8123}
			server.s3Client = store
			query := mock.ExpectQuery("GetVodOriginal").WithArgs("tenant", "hash")
			key, backend := "original.mov", server.localBackendID()
			var size any = int64(8123)
			want := codes.OK
			switch name {
			case "other tenant":
				query.WillReturnError(sql.ErrNoRows)
				want = codes.NotFound
			case "not kept":
				key, size, want = "", nil, codes.FailedPrecondition
			case "wrong backend":
				backend, want = "foreign", codes.FailedPrecondition
			case "missing object":
				store.present, want = false, codes.FailedPrecondition
			case "changed size":
				store.size, want = 9999, codes.FailedPrecondition
			}
			if name != "other tenant" {
				query.WillReturnRows(originalRows().AddRow("ready", "processed.mkv.att-1", true, backend, key, size, "phone.mov"))
			}
			response, err := server.CreateVodOriginalDownload(context.Background(), &sharedpb.CreateVodOriginalDownloadRequest{TenantId: "tenant", ArtifactHash: "hash"})
			if status.Code(err) != want {
				t.Fatalf("code %s want %s: %v", status.Code(err), want, err)
			}
			if want == codes.OK && (response.GetSizeBytes() != 8123 || store.key != "original.mov" || store.filename != "phone.mov" || store.expiry != 15*time.Minute) {
				t.Fatalf("wrong original download: %+v / %+v", response, store)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDeleteVodOriginalWaitsForProcessedSync(t *testing.T) {
	server, mock, done := newStatusServer(t, &fakeVodS3Client{})
	defer done()
	row := func() *sqlmock.Rows {
		return originalRows().AddRow("ready", "same.mkv", true, server.localBackendID(), "same.mkv", int64(20), "same.mkv")
	}
	mock.ExpectQuery("GetVodOriginal").WithArgs("tenant", "hash").WillReturnRows(row())
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT artifact_hash FROM foghorn.artifacts").WithArgs("tenant", "hash").WillReturnRows(sqlmock.NewRows([]string{"artifact_hash"}).AddRow("hash"))
	mock.ExpectQuery("GetVodOriginal").WithArgs("tenant", "hash").WillReturnRows(row())
	mock.ExpectRollback()
	_, err := server.DeleteVodOriginal(context.Background(), &sharedpb.DeleteVodOriginalRequest{TenantId: "tenant", ArtifactHash: "hash"})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("must preserve the only published object: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOriginalRetentionWaitsForReleaseActivation(t *testing.T) {
	for _, test := range []struct {
		name      string
		activated bool
		failed    bool
	}{
		{name: "pending"}, {name: "query failure", failed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, mock, done := newStatusServer(t, &fakeVodS3Client{})
			defer done()
			expected := mock.ExpectQuery("IsVodOriginalRetentionActivated")
			if test.failed {
				expected.WillReturnError(sql.ErrConnDone)
			} else {
				expected.WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(test.activated))
			}
			expectedCode := codes.FailedPrecondition
			if test.failed {
				expectedCode = codes.Unavailable
			}
			if err := server.requireOriginalRetentionReady(context.Background()); status.Code(err) != expectedCode {
				t.Fatalf("inactive retention admitted: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDeleteVodOriginalCannotSucceedBeforeRetentionFinishes(t *testing.T) {
	for _, state := range []string{"uploading", "processing", "ready"} {
		t.Run(state, func(t *testing.T) {
			server, mock, done := newStatusServer(t, &fakeVodS3Client{})
			defer done()
			row := func() *sqlmock.Rows {
				return originalRows().AddRow(state, "processed.mkv", true, server.localBackendID(), nil, nil, "phone.mov")
			}
			mock.ExpectQuery("GetVodOriginal").WithArgs("tenant", "hash").WillReturnRows(row())
			if state == "ready" {
				mock.ExpectBegin()
				mock.ExpectQuery("SELECT artifact_hash FROM foghorn.artifacts").WithArgs("tenant", "hash").WillReturnRows(sqlmock.NewRows([]string{"artifact_hash"}).AddRow("hash"))
				mock.ExpectQuery("GetVodOriginal").WithArgs("tenant", "hash").WillReturnRows(row())
				mock.ExpectCommit()
			}
			response, err := server.DeleteVodOriginal(context.Background(), &sharedpb.DeleteVodOriginalRequest{TenantId: "tenant", ArtifactHash: "hash"})
			if state != "ready" {
				if status.Code(err) != codes.FailedPrecondition {
					t.Fatalf("%s deletion must wait for processing: response=%v error=%v", state, response, err)
				}
			} else if err != nil || response.GetSuccess() {
				t.Fatalf("absent original must not report a deletion: response=%v error=%v", response, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCreateVodUploadContinuesWithoutRetentionDuringRollout(t *testing.T) {
	for _, state := range []string{"inactive"} {
		t.Run(state, func(t *testing.T) {
			server, mock, done := newStatusServer(t, &fakeVodS3Client{})
			defer done()
			mock.ExpectQuery("IsVodOriginalRetentionActivated").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
			mock.ExpectQuery(`FROM foghorn\.artifacts a`).WillReturnError(sql.ErrNoRows)
			mock.ExpectBegin()
			mock.ExpectExec(`INSERT INTO foghorn\.artifacts`).WillReturnResult(sqlmock.NewResult(1, 1))
			mock.ExpectExec(`INSERT INTO foghorn\.vod_metadata`).WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), false).WillReturnError(sql.ErrConnDone)
			mock.ExpectRollback()
			name, hash := "vod-test", "hash-1"
			req := &sharedpb.CreateVodUploadRequest{TenantId: "tenant", Filename: "phone.mov", SizeBytes: 1024, VodHash: &hash, InternalName: &name, ClusterId: "central-primary", KeepOriginal: proto.Bool(true)}
			_, err := server.CreateVodUpload(context.Background(), req)
			if status.Code(err) != codes.Internal {
				t.Fatalf("rollout must reach multipart persistence: %v", err)
			}
			if !req.GetKeepOriginal() {
				t.Fatal("handler mutated caller's retention preference")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
