package grpc

import (
	"context"
	"database/sql/driver"
	"github.com/lib/pq"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/listmonk"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/sirupsen/logrus"
)

func TestGetMe_MapsInlineUserAndWalletRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	createdAt := time.Unix(1_700_000_000, 0).UTC()
	updatedAt := createdAt.Add(30 * time.Minute)
	lastLoginAt := createdAt.Add(10 * time.Minute)
	walletCreatedAt := createdAt.Add(5 * time.Minute)

	mock.ExpectQuery("FROM commodore.users WHERE id = \\$1 AND tenant_id = \\$2").
		WithArgs("user-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "tenant_id", "email", "first_name", "last_name", "role", "permissions",
			"is_active", "verified", "last_login_at", "created_at", "updated_at", "platform_operator",
		}).AddRow(
			"user-1",
			"tenant-1",
			"user@example.com",
			"Ada",
			"Lovelace",
			"owner",
			"{read,write}",
			true,
			true,
			lastLoginAt,
			createdAt,
			updatedAt,
			false,
		))

	mock.ExpectQuery("FROM commodore.wallet_identities").
		WithArgs("user-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "wallet_address", "created_at", "last_auth_at",
		}).AddRow(
			"wallet-1",
			"0xabc",
			walletCreatedAt,
			lastLoginAt,
		))

	server := &CommodoreServer{db: db, logger: logrus.New()}
	ctx := context.WithValue(context.Background(), ctxkeys.KeyUserID, "user-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-1")

	resp, err := server.GetMe(ctx, &commodorepb.GetMeRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.GetId() != "user-1" || resp.GetTenantId() != "tenant-1" {
		t.Fatalf("unexpected identity mapping: id=%q tenant_id=%q", resp.GetId(), resp.GetTenantId())
	}
	if resp.GetEmail() != "user@example.com" {
		t.Fatalf("unexpected email: %q", resp.GetEmail())
	}
	if resp.GetFirstName() != "Ada" || resp.GetLastName() != "Lovelace" {
		t.Fatalf("unexpected name mapping: %q %q", resp.GetFirstName(), resp.GetLastName())
	}
	if resp.GetRole() != "owner" {
		t.Fatalf("unexpected role: %q", resp.GetRole())
	}
	if len(resp.GetPermissions()) != 2 || resp.GetPermissions()[0] != "read" || resp.GetPermissions()[1] != "write" {
		t.Fatalf("unexpected permissions: %#v", resp.GetPermissions())
	}
	if resp.GetCreatedAt() == nil || resp.GetUpdatedAt() == nil || resp.GetLastLoginAt() == nil {
		t.Fatal("expected created_at/updated_at/last_login_at to be populated")
	}
	if len(resp.GetWallets()) != 1 {
		t.Fatalf("expected one wallet, got %d", len(resp.GetWallets()))
	}
	if resp.GetWallets()[0].GetId() != "wallet-1" || resp.GetWallets()[0].GetWalletAddress() != "0xabc" {
		t.Fatalf("unexpected wallet mapping: %+v", resp.GetWallets()[0])
	}
	if resp.GetWallets()[0].GetLastAuthAt() == nil {
		t.Fatal("expected wallet last_auth_at to be mapped")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func refreshTokenUserRows() *sqlmock.Rows {
	createdAt := time.Unix(1_700_100_000, 0).UTC()
	updatedAt := createdAt.Add(15 * time.Minute)
	return sqlmock.NewRows([]string{
		"email", "role", "permissions", "first_name", "last_name",
		"is_active", "verified", "created_at", "updated_at", "platform_operator",
	}).AddRow(
		"refresh@example.com",
		"member",
		"{billing_read,billing_write}",
		"Grace",
		"Hopper",
		true,
		true,
		createdAt,
		updatedAt,
		false,
	)
}

func refreshTokenTestSettings() RuntimeSettings {
	return RuntimeSettings{JWTSecret: []byte("test-secret-for-refresh-token")}
}

func refreshSessionRows(userID, tenantID string) *sqlmock.Rows {
	values := sessionValues(authSessionTestID, nil)
	values[1], values[2] = tenantID, userID
	return sqlmock.NewRows(authSessionColumns).AddRow(values...)
}

func expectRefreshOwner(mock sqlmock.Sqlmock, refresh, userID, tenantID string) {
	mock.ExpectQuery(`SELECT user_id, tenant_id FROM commodore.refresh_tokens`).WithArgs(hashToken(refresh)).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "tenant_id"}).AddRow(userID, tenantID))
}

func expectRefreshLock(mock sqlmock.Sqlmock, refresh, userID, tenantID string, revoked bool, rotated, successor any) {
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM commodore.users WHERE tenant_id = \$1 AND id = \$2 FOR UPDATE`).
		WithArgs(tenantID, userID).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(userID))
	mock.ExpectQuery(`FROM commodore.refresh_tokens\s+WHERE token_hash`).WithArgs(hashToken(refresh)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "tenant_id", "revoked", "rotated_at", "replaced_by"}).
			AddRow("rt-1", userID, tenantID, revoked, rotated, successor))
	mock.ExpectQuery(`SELECT session_id FROM commodore.refresh_tokens`).WithArgs(tenantID, userID, "rt-1").
		WillReturnRows(sqlmock.NewRows([]string{"session_id"}).AddRow(authSessionTestID))
	mock.ExpectQuery(`FROM commodore.auth_sessions\s+WHERE tenant_id = \$1 AND user_id = \$2 AND id = \$3 FOR UPDATE`).
		WithArgs(tenantID, userID, authSessionTestID).WillReturnRows(refreshSessionRows(userID, tenantID))
}

func expectRefreshUserAndTouch(mock sqlmock.Sqlmock, userID, tenantID string) {
	mock.ExpectQuery(`FROM commodore.users WHERE id = \$1 AND tenant_id = \$2`).WithArgs(userID, tenantID).
		WillReturnRows(refreshTokenUserRows())
	mock.ExpectQuery(`UPDATE commodore.auth_sessions\s+SET last_seen_at`).WithArgs(tenantID, userID, authSessionTestID, "").
		WillReturnRows(refreshSessionRows(userID, tenantID))
}

func TestRefreshToken_MapsInlineUserStructToAuthResponse(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	s.runtimeSettings = refreshTokenTestSettings
	expectRefreshOwner(mock, "raw-refresh", "user-2", "tenant-2")
	expectRefreshLock(mock, "raw-refresh", "user-2", "tenant-2", false, nil, nil)
	expectRefreshUserAndTouch(mock, "user-2", "tenant-2")
	mock.ExpectQuery(`INSERT INTO commodore.refresh_tokens`).WithArgs("tenant-2", "user-2", authSessionTestID, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("rt-2"))
	mock.ExpectExec(`UPDATE commodore.refresh_tokens`).WithArgs("rt-1", "rt-2").WillReturnResult(sqlmock.NewResult(0, 1))
	expectLegacyEventInsert(mock, eventAuthTokenRefreshed)
	mock.ExpectCommit()
	resp, err := s.RefreshToken(context.Background(), &commodorepb.RefreshTokenRequest{RefreshToken: "raw-refresh"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetRefreshToken() == "" || resp.GetUser().GetId() != "user-2" || resp.GetUser().GetTenantId() != "tenant-2" || resp.GetUser().GetEmail() != "refresh@example.com" || len(resp.GetUser().GetPermissions()) != 2 {
		t.Fatalf("identity mapping: %+v", resp)
	}
	claims, err := auth.ValidateInteractiveJWT(resp.GetToken(), refreshTokenTestSettings().JWTSecret)
	if err != nil || claims.SessionID != authSessionTestID || claims.AuthTime >= time.Now().Add(-30*time.Minute).Unix() {
		t.Fatalf("refresh lost session identity or original auth_time: %+v %v", claims, err)
	}
	if !resp.GetExpiresAt().AsTime().Equal(claims.ExpiresAt.Time) {
		t.Fatal("response expiry differs from JWT")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshToken_ReplayWithinGraceReturnsRetryable(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	expectRefreshOwner(mock, "raw-refresh", "user-2", "tenant-2")
	expectRefreshLock(mock, "raw-refresh", "user-2", "tenant-2", true, time.Now().Add(-time.Minute), "rt-2")
	mock.ExpectRollback()
	resp, err := s.RefreshToken(context.Background(), &commodorepb.RefreshTokenRequest{RefreshToken: "raw-refresh"})
	if status.Code(err) != codes.Unavailable || resp != nil {
		t.Fatalf("grace replay forked a new chain: response=%v err=%v", resp != nil, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshToken_LostRotationResponseRecoversSession(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	s.runtimeSettings = refreshTokenTestSettings
	expectRefreshOwner(mock, "raw-refresh", "user-2", "tenant-2")
	expectRefreshLock(mock, "raw-refresh", "user-2", "tenant-2", true, time.Now().Add(-time.Hour), "rt-2")
	mock.ExpectQuery(`FROM commodore.refresh_tokens WHERE tenant_id`).WithArgs("tenant-2", "user-2", "rt-2").
		WillReturnRows(sqlmock.NewRows([]string{"id", "revoked", "replaced_by"}).AddRow("rt-2", false, nil))
	expectRefreshUserAndTouch(mock, "user-2", "tenant-2")
	mock.ExpectQuery(`INSERT INTO commodore.refresh_tokens`).WithArgs("tenant-2", "user-2", authSessionTestID, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("rt-3"))
	mock.ExpectExec(`UPDATE commodore.refresh_tokens SET revoked = true WHERE id`).WithArgs("rt-2").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE commodore.refresh_tokens`).WithArgs("rt-1", "rt-3").WillReturnResult(sqlmock.NewResult(0, 1))
	expectLegacyEventInsert(mock, eventAuthTokenRefreshed)
	mock.ExpectCommit()
	resp, err := s.RefreshToken(context.Background(), &commodorepb.RefreshTokenRequest{RefreshToken: "raw-refresh"})
	if err != nil || resp.GetRefreshToken() == "" {
		t.Fatalf("lost response recovery: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshToken_StoreFailureRollsBack(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	expectRefreshOwner(mock, "raw-refresh", "user-2", "tenant-2")
	expectRefreshLock(mock, "raw-refresh", "user-2", "tenant-2", false, nil, nil)
	expectRefreshUserAndTouch(mock, "user-2", "tenant-2")
	mock.ExpectQuery(`INSERT INTO commodore.refresh_tokens`).WithArgs("tenant-2", "user-2", authSessionTestID, sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnError(context.DeadlineExceeded)
	mock.ExpectRollback()
	_, err := s.RefreshToken(context.Background(), &commodorepb.RefreshTokenRequest{RefreshToken: "raw-refresh"})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("storage failure: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshTokenDeletedOwnerIsRejected(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	expectRefreshOwner(mock, "orphan-refresh", "deleted-user", "tenant-2")
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM commodore.users WHERE tenant_id = \$1 AND id = \$2 FOR UPDATE`).
		WithArgs("tenant-2", "deleted-user").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectRollback()
	_, err := s.RefreshToken(context.Background(), &commodorepb.RefreshTokenRequest{RefreshToken: "orphan-refresh"})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("deleted owner treated as an outage instead of invalid_grant: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetNewsletterStatus_ListmonkErrorReturnsUnsubscribed(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	listmonkServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer listmonkServer.Close()

	mock.ExpectQuery("SELECT email FROM commodore.users WHERE id = \\$1 AND tenant_id = \\$2").
		WithArgs("user-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"email"}).AddRow("user@example.com"))

	server := &CommodoreServer{
		db:                   db,
		logger:               logrus.New(),
		listmonkClient:       listmonk.NewClient(listmonkServer.URL, "user", "pass"),
		defaultMailingListID: 1,
	}
	ctx := context.WithValue(context.Background(), ctxkeys.KeyUserID, "user-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-1")

	resp, err := server.GetNewsletterStatus(ctx, &commodorepb.GetNewsletterStatusRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetSubscribed() {
		t.Fatal("expected unsubscribed when Listmonk status cannot be read")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// recordArg matches any value and remembers each one it saw.
type recordArg struct{ seen *[]driver.Value }

func (r recordArg) Match(v driver.Value) bool {
	*r.seen = append(*r.seen, v)
	return true
}

// TestRefreshToken_ReplayReturnsTheCommittedAttemptsToken proves a rotation whose commit fails with 40001 is replayed,
// and the refresh token handed back is the one the committed attempt stored, not the aborted attempt's.
func TestRefreshToken_ReplayReturnsTheCommittedAttemptsToken(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	s.runtimeSettings = refreshTokenTestSettings
	var storedHashes []driver.Value
	expectRefreshOwner(mock, "raw-refresh", "user-2", "tenant-2")
	for i, newID := range []string{"rt-2", "rt-3"} {
		expectRefreshLock(mock, "raw-refresh", "user-2", "tenant-2", false, nil, nil)
		expectRefreshUserAndTouch(mock, "user-2", "tenant-2")
		mock.ExpectQuery(`INSERT INTO commodore.refresh_tokens`).WithArgs("tenant-2", "user-2", authSessionTestID, recordArg{seen: &storedHashes}, sqlmock.AnyArg()).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(newID))
		mock.ExpectExec(`UPDATE commodore.refresh_tokens`).WithArgs("rt-1", newID).WillReturnResult(sqlmock.NewResult(0, 1))
		expectLegacyEventInsert(mock, eventAuthTokenRefreshed)
		if i == 0 {
			mock.ExpectCommit().WillReturnError(&pq.Error{Code: "40001", Message: "could not serialize access"})
		} else {
			mock.ExpectCommit()
		}
	}
	resp, err := s.RefreshToken(context.Background(), &commodorepb.RefreshTokenRequest{RefreshToken: "raw-refresh"})
	if err != nil {
		t.Fatal(err)
	}
	if len(storedHashes) != 2 || storedHashes[0] == storedHashes[1] || hashToken(resp.GetRefreshToken()) != storedHashes[1] {
		t.Fatalf("returned refresh token does not match committed attempt: %v", storedHashes)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
