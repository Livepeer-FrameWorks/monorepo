//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/storage"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// startFoghornGRPCRealPG starts a throwaway PostgreSQL with the embedded
// foghorn.sql baseline applied.
func startFoghornGRPCRealPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-foghorn-grpc-realpg-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatalf("resolve PostgreSQL test image: %v", err)
	}
	if out, err := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := dockerpg.WaitReady(conn, name); err != nil {
		t.Fatal(err)
	}
	schema, err := dbsql.Content.ReadFile("schema/foghorn.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(string(schema)); err != nil {
		t.Fatalf("apply foghorn.sql: %v", err)
	}
	return conn
}

type requestedEventRow struct {
	eventID, authType, userID, tokenHash string
}

func requestedEventRows(t *testing.T, conn *sql.DB, eventType, artifactHash string) []requestedEventRow {
	t.Helper()
	rows, err := conn.Query(`
		SELECT event_id::text, actor_auth_type, actor_user_id, actor_token_hash
		FROM foghorn.domain_event_outbox WHERE event_type = $1 AND aggregate_id = $2 ORDER BY enqueued_at`, eventType, artifactHash)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []requestedEventRow
	for rows.Next() {
		var r requestedEventRow
		if err := rows.Scan(&r.eventID, &r.authType, &r.userID, &r.tokenHash); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func takeDomainOutboxOffline(t *testing.T, conn *sql.DB) func() {
	t.Helper()
	if _, err := conn.Exec(`ALTER TABLE foghorn.domain_event_outbox RENAME TO domain_event_outbox_offline`); err != nil {
		t.Fatal(err)
	}
	return func() {
		if _, err := conn.Exec(`ALTER TABLE foghorn.domain_event_outbox_offline RENAME TO domain_event_outbox`); err != nil {
			t.Fatal(err)
		}
	}
}

// Creating and aborting an upload through the production handlers records
// upload.created and upload.aborted attributed to the principal Commodore named
// on the request, with the token hash Commodore computed. A failed event write
// rolls the transition back, and a retried request records nothing.
func TestRequestedUploadEventsCarryTheCaller_RealPG(t *testing.T) { //nolint:funlen // One database follows both requested transitions.
	conn := startFoghornGRPCRealPG(t)
	t.Cleanup(control.SetupTestRegistry("", nil))
	s3 := &fakeVodS3Client{}
	srv := NewFoghornGRPCServer(conn, logging.NewLogger(), nil, nil, nil, nil, s3, nil)
	srv.SetClusterID("central-primary")
	srv.SetQuartermasterClient(&mockQMRouting{clusterID: "central-primary"})
	srv.SetStorageResolverFactory(func(_ context.Context, _ string) *storage.ClusterResolver {
		return &storage.ClusterResolver{LocalClusterID: "central-primary", LocalS3ClientPresent: true}
	})
	ctx := context.Background()
	tenantID, userID := uuid.NewString(), uuid.NewString()
	tokenHash := events.HashIdentifier([]byte("shared-usage-secret"), "token-record-3")
	caller := events.RequestActor(events.Actor{AuthType: "api_token", UserID: userID, TokenHash: tokenHash})
	wantHash := strconv.FormatUint(tokenHash, 10)

	create := func(hash, uploadID string) (*sharedpb.CreateVodUploadResponse, error) {
		s3.createID = uploadID
		internalName := "vod-" + hash
		return srv.CreateVodUpload(ctx, &sharedpb.CreateVodUploadRequest{
			TenantId: tenantID, UserId: userID, Filename: "video.mp4", SizeBytes: 1024,
			VodHash: &hash, InternalName: &internalName, ClusterId: "central-primary", Actor: caller,
		})
	}
	artifactStatus := func(hash string) string {
		var s sql.NullString
		err := conn.QueryRow(`SELECT status FROM foghorn.artifacts WHERE artifact_hash = $1`, hash).Scan(&s)
		if err == sql.ErrNoRows {
			return ""
		}
		if err != nil {
			t.Fatal(err)
		}
		return s.String
	}
	assertAttributed := func(t *testing.T, eventType, hash string) requestedEventRow {
		t.Helper()
		rows := requestedEventRows(t, conn, eventType, hash)
		if len(rows) != 1 {
			t.Fatalf("%s rows for %s = %+v, want one", eventType, hash, rows)
		}
		if rows[0].authType != "api_token" || rows[0].userID != userID || rows[0].tokenHash != wantHash {
			t.Fatalf("%s actor = %+v, want api_token %s %s", eventType, rows[0], userID, wantHash)
		}
		var legacyID string
		if err := conn.QueryRow(`SELECT id::text FROM foghorn.artifact_event_outbox WHERE id = $1::uuid AND artifact_id = $2`, rows[0].eventID, hash).Scan(&legacyID); err != nil {
			t.Fatalf("legacy row with the %s event id: %v", eventType, err)
		}
		return rows[0]
	}

	t.Run("a failed upload.created write leaves no upload", func(t *testing.T) {
		const hash = "vodactorrollback0000000000000001"
		restore := takeDomainOutboxOffline(t, conn)
		_, err := create(hash, "up-rollback")
		restore()
		if status.Code(err) != codes.Internal {
			t.Fatalf("CreateVodUpload err = %v, want Internal", err)
		}
		if got := artifactStatus(hash); got != "" {
			t.Fatalf("artifact status = %q after the rollback, want no row", got)
		}
		if s3.abortUpID != "up-rollback" {
			t.Fatalf("multipart %q was not aborted after the rollback", s3.abortUpID)
		}
	})

	const hash = "vodactorcommit000000000000000001"
	t.Run("upload.created carries the caller and a retried create records nothing", func(t *testing.T) {
		if _, err := create(hash, "up-commit"); err != nil {
			t.Fatal(err)
		}
		if got := artifactStatus(hash); got != "uploading" {
			t.Fatalf("artifact status = %q, want uploading", got)
		}
		assertAttributed(t, "upload.created", hash)
		if _, err := create(hash, "up-commit"); err != nil {
			t.Fatalf("retried CreateVodUpload: %v", err)
		}
		if rows := requestedEventRows(t, conn, "upload.created", hash); len(rows) != 1 {
			t.Fatalf("upload.created rows after the retry = %+v, want one", rows)
		}
	})

	t.Run("a failed upload.aborted write leaves the abort for recovery", func(t *testing.T) {
		const other = "vodactorabortfail000000000000001"
		if _, err := create(other, "up-abortfail"); err != nil {
			t.Fatal(err)
		}
		restore := takeDomainOutboxOffline(t, conn)
		_, err := srv.AbortVodUpload(ctx, &sharedpb.AbortVodUploadRequest{TenantId: tenantID, UploadId: "up-abortfail", Actor: caller})
		restore()
		if status.Code(err) != codes.Internal {
			t.Fatalf("AbortVodUpload err = %v, want Internal", err)
		}
		if got := artifactStatus(other); got != "aborting" {
			t.Fatalf("artifact status = %q after the rolled-back finalize, want aborting", got)
		}
		if rows := requestedEventRows(t, conn, "upload.aborted", other); len(rows) != 0 {
			t.Fatalf("upload.aborted rows after the rollback = %+v, want none", rows)
		}
	})

	t.Run("upload.aborted carries the caller and a retried abort records nothing", func(t *testing.T) {
		if _, err := srv.AbortVodUpload(ctx, &sharedpb.AbortVodUploadRequest{TenantId: tenantID, UploadId: "up-commit", Actor: caller}); err != nil {
			t.Fatal(err)
		}
		if got := artifactStatus(hash); got != "deleted" {
			t.Fatalf("artifact status = %q, want deleted", got)
		}
		assertAttributed(t, "upload.aborted", hash)
		if _, err := srv.AbortVodUpload(ctx, &sharedpb.AbortVodUploadRequest{TenantId: tenantID, UploadId: "up-commit", Actor: caller}); status.Code(err) != codes.NotFound {
			t.Fatalf("retried AbortVodUpload err = %v, want NotFound", err)
		}
		if rows := requestedEventRows(t, conn, "upload.aborted", hash); len(rows) != 1 {
			t.Fatalf("upload.aborted rows after the retry = %+v, want one", rows)
		}
	})

	t.Run("a request that names no principal records no actor", func(t *testing.T) {
		const unnamed = "vodactorunnamed00000000000000001"
		s3.createID = "up-unnamed"
		internalName := "vod-unnamed"
		unnamedHash := unnamed
		if _, err := srv.CreateVodUpload(ctx, &sharedpb.CreateVodUploadRequest{
			TenantId: tenantID, UserId: userID, Filename: "video.mp4", SizeBytes: 1024,
			VodHash: &unnamedHash, InternalName: &internalName, ClusterId: "central-primary",
		}); err != nil {
			t.Fatal(err)
		}
		rows := requestedEventRows(t, conn, "upload.created", unnamed)
		if len(rows) != 1 || rows[0].authType != "" || rows[0].userID != "" || rows[0].tokenHash != "" {
			t.Fatalf("unnamed upload.created rows = %+v, want one without an actor", rows)
		}
	})
}
