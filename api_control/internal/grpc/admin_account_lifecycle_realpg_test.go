//go:build schema_verify

package grpc

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"

	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	fwdb "github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestOperatorAccountLifecycle_RealPG(t *testing.T) {
	var db *sql.DB
	if strings.HasSuffix(t.Name(), "_RealYugabyte") {
		var ok bool
		db, ok = dockerpg.OpenSharedYugabyteBaseline(t, "operator_accounts", "commodore")
		if !ok {
			t.Skip("requires the shared Yugabyte fixture")
		}
	} else {
		db = startCommodoreRealPG(t)
	}
	testAccountRevocationStopsIngest(t, db)
	testAccountReactivationDuringCellResolution(t, db)
	testAccountIngestRecipientIsolation(t, db)
	testRetiredIngestCellSettlesStop(t, db)
	testPendingStopCellsMatchIngestRecipients(t, db)
	ctx := context.Background()
	tenantID, userID := uuid.NewString(), uuid.NewString()
	if _, writeErr := db.ExecContext(ctx, `INSERT INTO commodore.users(id,tenant_id,email,password_hash,is_active,verified) VALUES($1,$2,'trial@example.com','x',true,true)`, userID, tenantID); writeErr != nil {
		t.Fatal(writeErr)
	}
	if _, writeErr := db.ExecContext(ctx, `INSERT INTO commodore.refresh_tokens(tenant_id,user_id,token_hash,expires_at) VALUES($1,$2,'trial-refresh',NOW()+INTERVAL '1 day')`, tenantID, userID); writeErr != nil {
		t.Fatal(writeErr)
	}
	streamID := uuid.NewString()
	if _, writeErr := db.ExecContext(ctx, `INSERT INTO commodore.streams(id,tenant_id,user_id,internal_name,stream_key,playback_id,title) VALUES($1,$2,$3,'trial-test','trial-key','trial-playback','Trial')`, streamID, tenantID, userID); writeErr != nil {
		t.Fatal(writeErr)
	}
	server := &CommodoreServer{authorityTenantSource: operatorAccountTenantSource{}, db: db, logger: logrus.New(), tokenHasher: testTokenHasher(t)}
	op := context.WithValue(ctxAsOperator(uuid.NewString(), tenantID, "owner"), ctxkeys.KeyAuthType, "jwt")
	server.authorityTenantSource = operatorAccountTenantSource{missing: true}
	unknown := uuid.NewString()
	if _, adminDeactivateTenantErr := server.AdminDeactivateTenant(op, &commodorepb.AdminDeactivateTenantRequest{TenantId: unknown, TenantName: "Missing", Reason: "typo"}); status.Code(adminDeactivateTenantErr) != codes.NotFound {
		t.Fatalf("unknown tenant closure=%v", adminDeactivateTenantErr)
	}
	var unknownState int
	if readErr := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM commodore.tenant_account_states WHERE tenant_id=$1`, unknown).Scan(&unknownState); readErr != nil {
		t.Fatal(readErr)
	}
	if unknownState != 0 {
		t.Fatal("unknown tenant acquired closure state")
	}
	server.authorityTenantSource = operatorAccountTenantSource{}
	response, err := server.AdminSetUserActive(op, &commodorepb.AdminSetUserActiveRequest{TenantId: tenantID, UserId: userID, Active: false, Reason: "trial revoked"})
	if err != nil {
		t.Fatal(err)
	}
	if response.GetIsActive() {
		t.Fatal("deactivation left user active")
	}
	var revoked, events int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM commodore.refresh_tokens WHERE tenant_id=$1 AND user_id=$2 AND revoked`, tenantID, userID).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM commodore.domain_event_outbox WHERE tenant_id=$1 AND event_type='user.sessions_revoked'`, tenantID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if revoked != 1 || events != 1 {
		t.Fatalf("revoked refresh=%d session events=%d, want 1/1", revoked, events)
	}
	historyPage, historyErr := server.AdminListAccountHistory(op, &commodorepb.AdminListAccountHistoryRequest{TenantId: tenantID, Limit: 1})
	if historyErr != nil || len(historyPage.GetEntries()) != 1 || historyPage.GetEntries()[0].GetReason() != "trial revoked" {
		t.Fatalf("private history=%v err=%v", historyPage, historyErr)
	}
	otherPage, historyErr := server.AdminListAccountHistory(op, &commodorepb.AdminListAccountHistoryRequest{TenantId: uuid.NewString()})
	if historyErr != nil || len(otherPage.GetEntries()) != 0 {
		t.Fatalf("cross-tenant history leaked: %v err=%v", otherPage, historyErr)
	}
	if _, adminSetUserActiveErr := server.AdminSetUserActive(op, &commodorepb.AdminSetUserActiveRequest{TenantId: tenantID, UserId: userID, Active: false, Reason: "repeat"}); adminSetUserActiveErr != nil {
		t.Fatal(adminSetUserActiveErr)
	}
	repeated, err := server.AdminListAccountHistory(op, &commodorepb.AdminListAccountHistoryRequest{TenantId: tenantID})
	if err != nil || len(repeated.GetEntries()) != 1 {
		t.Fatalf("repeated deactivation wrote audit: %v err=%v", repeated, err)
	}
	var privateReasons int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM commodore.operator_account_audit WHERE tenant_id=$1 AND reason='trial revoked'`, tenantID).Scan(&privateReasons); err != nil {
		t.Fatal(err)
	}
	if privateReasons != 1 {
		t.Fatalf("private operator audit reasons=%d", privateReasons)
	}
	var publicPayload []byte
	if err = db.QueryRowContext(ctx, `SELECT payload FROM commodore.domain_event_outbox WHERE tenant_id=$1 AND event_type='user.active_changed'`, tenantID).Scan(&publicPayload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(publicPayload), "trial revoked") {
		t.Fatal("operator reason leaked into tenant-visible event")
	}
	source, err := commodoredb.New(db).GetLiveStreamMediaAuthoritySource(ctx, streamID)
	if err != nil {
		t.Fatal(err)
	}
	if source.MayPublish {
		t.Fatal("deactivated owner retained signed publish permission")
	}
	if source.UserID != userID {
		t.Fatal("deactivation changed tenant media ownership")
	}
	denied, denyErr := server.ValidateStreamKey(context.WithValue(ctx, ctxkeys.KeyAuthType, "service"), &commodorepb.ValidateStreamKeyRequest{StreamKey: "trial-key"})
	if denyErr != nil || denied.GetValid() || denied.GetRejectionReason() != commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_USER_INACTIVE {
		t.Fatalf("inactive key response=%+v err=%v", denied, denyErr)
	}

	server.purserClient = startPurserEntitlementFake(t, &purserEntitlementFake{})
	playback, playErr := server.ResolveStreamContext(context.WithValue(ctx, ctxkeys.KeyAuthType, "service"), &commodorepb.ResolveStreamContextRequest{Identifier: &commodorepb.ResolveStreamContextRequest_PlaybackId{PlaybackId: "trial-playback"}})
	if playErr != nil || !playback.GetAdmitted() {
		t.Fatalf("inactive owner playback=%+v err=%v", playback, playErr)
	}

	response, err = server.AdminSetUserActive(op, &commodorepb.AdminSetUserActiveRequest{TenantId: tenantID, UserId: userID, Active: true, Reason: "trial restored"})
	if err != nil {
		t.Fatal(err)
	}
	if !response.GetIsActive() {
		t.Fatal("activation did not restore user")
	}
	server.authorityBillingSource = unusedMediaAuthorityBillingSource{}
	server.mediaAuthorityKeyID = "closure-test"
	server.mediaAuthorityPrivateKey = ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	original := &mediapb.TenantAuthority{BillingModel: mediapb.TenantBillingModel_TENANT_BILLING_MODEL_POSTPAID, SchemaVersion: 1, TenantId: tenantID, Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE, BillingDecision: mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW}
	now := time.Now().UTC()
	if err = server.persistTenantAuthority(ctx, original, nil, []*mediapb.AuthoritySourceRevision{{Service: "commodore", Revision: "before-closure"}}, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err = server.AdminDeactivateTenant(op, &commodorepb.AdminDeactivateTenantRequest{TenantId: tenantID, TenantName: "Wrong Name", Reason: "typo"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("name mismatch error=%v", err)
	}
	// Quartermaster is deliberately unavailable: closure must persist locally and remain retryable.
	if _, err = server.AdminDeactivateTenant(op, &commodorepb.AdminDeactivateTenantRequest{TenantId: tenantID, TenantName: "Trial Tenant", Reason: "permanent closure"}); status.Code(err) != codes.Unavailable {
		t.Fatalf("closure error=%v, want Unavailable", err)
	}
	for _, request := range []*commodorepb.ResolveStreamContextRequest{{Identifier: &commodorepb.ResolveStreamContextRequest_PlaybackId{PlaybackId: "trial-playback"}}, {Identifier: &commodorepb.ResolveStreamContextRequest_InternalName{InternalName: "trial-test"}}} {
		result, resolveErr := server.ResolveStreamContext(context.WithValue(ctx, ctxkeys.KeyAuthType, "service"), request)
		if resolveErr != nil || result.GetAdmitted() {
			t.Fatalf("locally closed tenant resolved during Quartermaster outage: %v err=%v", result, resolveErr)
		}
	}
	if err = server.compileTenantAuthority(ctx, tenantID); err != nil {
		t.Fatal(err)
	}
	current, err := commodoredb.New(db).GetCurrentMediaAuthorityPayload(ctx, commodoredb.GetCurrentMediaAuthorityPayloadParams{AuthorityKind: "tenant", AuthorityID: tenantID})
	if err != nil {
		t.Fatal(err)
	}
	closedAuthority := &mediapb.TenantAuthority{}
	if err = proto.Unmarshal(current.Payload, closedAuthority); err != nil {
		t.Fatal(err)
	}
	if closedAuthority.GetLifecycle() != mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_INACTIVE || closedAuthority.GetDecisionReason() != "tenant_inactive" {
		t.Fatalf("closure published destructive or wrong authority: %+v", closedAuthority)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO commodore.users(id,tenant_id,email,is_active) VALUES($1,$2,'escaped@example.com',true)`, uuid.NewString(), tenantID); err == nil {
		t.Fatal("closed tenant accepted an active user")
	}
	if fwdb.SQLState(err) != "23514" {
		t.Fatalf("closed tenant insert error=%v, want SQLSTATE 23514", err)
	}
	if _, err = server.AdminSetUserActive(op, &commodorepb.AdminSetUserActiveRequest{TenantId: tenantID, UserId: userID, Active: true, Reason: "try reopening"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("activation error=%v, want FailedPrecondition", err)
	}
	pending, err := commodoredb.New(db).ClaimPendingTenantAccountClosures(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].TenantID != tenantID {
		t.Fatalf("pending closure=%v, want tenant", pending)
	}
	protectedTenant, protectedUser := uuid.NewString(), uuid.NewString()
	if _, writeErr := db.ExecContext(ctx, `INSERT INTO commodore.users(id,tenant_id,email,is_active,verified,platform_operator) VALUES($1,$2,'last-operator@example.test',true,true,true)`, protectedUser, protectedTenant); writeErr != nil {
		t.Fatal(writeErr)
	}
	if _, adminSetUserActiveErr2 := server.AdminSetUserActive(op, &commodorepb.AdminSetUserActiveRequest{TenantId: protectedTenant, UserId: protectedUser, Reason: "last operator", Active: false}); status.Code(adminSetUserActiveErr2) != codes.FailedPrecondition {
		t.Fatalf("last operator deactivation=%v", adminSetUserActiveErr2)
	}
	testConcurrentAccountCreationDuringClosure(t, db)
	testReadCommittedAccountCreationAndClosure(t, db)
	testTenantClosureFailuresDoNotStarveLaterTenants(t, db)
	testInactiveOwnerPublishingAuthority(t, db)
	testPlacementOwnerPublishingRevocation(t, db)
	testAccountHistoryStablePaging(t, db)
}

func TestOperatorAccountLifecycle_RealYugabyte(t *testing.T) { TestOperatorAccountLifecycle_RealPG(t) }

type operatorAccountTenantSource struct {
	unusedMediaAuthorityTenantSource
	missing bool
}

func (f operatorAccountTenantSource) GetTenant(_ context.Context, id string) (*quartermasterpb.GetTenantResponse, error) {
	if f.missing {
		return nil, status.Error(codes.NotFound, "tenant not found")
	}
	return &quartermasterpb.GetTenantResponse{Tenant: &quartermasterpb.Tenant{Id: id, Name: "Trial Tenant", IsActive: true}}, nil
}

type operatorAccountQuartermaster struct {
	quartermasterpb.UnimplementedTenantServiceServer
	entered        chan struct{}
	closureHealthy string
}

func (f *operatorAccountQuartermaster) GetTenant(_ context.Context, r *quartermasterpb.GetTenantRequest) (*quartermasterpb.GetTenantResponse, error) {
	select {
	case f.entered <- struct{}{}:
	default:
	}
	return &quartermasterpb.GetTenantResponse{Tenant: &quartermasterpb.Tenant{Id: r.GetTenantId(), Name: "Concurrent", IsActive: true}}, nil
}

func testConcurrentAccountCreationDuringClosure(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	fake := &operatorAccountQuartermaster{entered: make(chan struct{}, 1)}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rpc := grpc.NewServer()
	quartermasterpb.RegisterTenantServiceServer(rpc, fake)
	go func() { _ = rpc.Serve(listener) }()
	t.Cleanup(func() { rpc.Stop(); _ = listener.Close() })
	qm, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{GRPCAddr: listener.Addr().String(), AllowInsecure: true, Logger: logging.NewLogger(), ServiceToken: "test", PreferServiceToken: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = qm.Close() })
	tenant := uuid.NewString()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	q := commodoredb.New(tx)
	if err = q.EnsureTenantAccountState(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err = q.LockTenantAccountState(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	if err = q.CloseTenantAccount(ctx, commodoredb.CloseTenantAccountParams{TenantID: tenant, ClosedBy: "test", Reason: "close concurrently"}); err != nil {
		t.Fatal(err)
	}
	server := &CommodoreServer{db: db, logger: logging.NewLogger(), quartermasterClient: qm}
	result := make(chan error, 1)
	go func() {
		_, callErr := server.CreateUserInTenant(context.WithValue(ctx, ctxkeys.KeyAuthType, "service"), &commodorepb.CreateUserInTenantRequest{TenantId: tenant, Email: tenant + "@example.test", Password: "strong-enough-test-password", Role: "owner"})
		result <- callErr
	}()
	select {
	case <-fake.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent create did not reach tenant verification")
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-result:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent create did not finish")
	}
	if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "tenant account is permanently closed") {
		t.Fatalf("concurrent create error=%v", err)
	}
	var users int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM commodore.users WHERE tenant_id=$1`, tenant).Scan(&users); err != nil {
		t.Fatal(err)
	}
	if users != 0 {
		t.Fatalf("closed tenant gained %d users", users)
	}
}

func testReadCommittedAccountCreationAndClosure(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, insertFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("read_committed_insert_first_%t", insertFirst), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			tenant, user := uuid.NewString(), uuid.NewString()
			q := commodoredb.New(db)
			if ensureTenantAccountStateErr := q.EnsureTenantAccountState(ctx, tenant); ensureTenantAccountStateErr != nil {
				t.Fatal(ensureTenantAccountStateErr)
			}
			first, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = first.Rollback() }()
			insert := func(tx *sql.Tx) error {
				_, insertErr := tx.ExecContext(ctx, `INSERT INTO commodore.users(id,tenant_id,email,is_active) VALUES($1,$2,$3,true)`, user, tenant, user+"@guard.test")
				return insertErr
			}
			closeAccount := func(tx *sql.Tx) error {
				queries := commodoredb.New(tx)
				if _, lockTenantAccountStateErr := queries.LockTenantAccountState(ctx, tenant); lockTenantAccountStateErr != nil {
					return lockTenantAccountStateErr
				}
				if closeTenantAccountErr := queries.CloseTenantAccount(ctx, commodoredb.CloseTenantAccountParams{TenantID: tenant, Reason: "concurrent closure", ClosedBy: "test"}); closeTenantAccountErr != nil {
					return closeTenantAccountErr
				}
				_, deactivateErr := tx.ExecContext(ctx, `UPDATE commodore.users SET is_active=false WHERE tenant_id=$1`, tenant)
				return deactivateErr
			}
			firstAction, secondAction := insert, closeAccount
			if !insertFirst {
				firstAction, secondAction = closeAccount, insert
			}
			if actionErr := firstAction(first); actionErr != nil {
				t.Fatal(actionErr)
			}
			entered, result := make(chan struct{}), make(chan error, 1)
			go func() {
				tx, beginErr := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
				if beginErr != nil {
					result <- beginErr
					return
				}
				defer func() { _ = tx.Rollback() }()
				close(entered)
				if actionErr := secondAction(tx); actionErr != nil {
					result <- actionErr
					return
				}
				result <- tx.Commit()
			}()
			<-entered
			select {
			case prematureErr := <-result:
				t.Fatalf("guard did not block the second writer: %v", prematureErr)
			case <-time.After(150 * time.Millisecond):
			}
			if commitErr := first.Commit(); commitErr != nil {
				t.Fatal(commitErr)
			}
			err = <-result
			if insertFirst && err != nil {
				t.Fatal(err)
			}
			if !insertFirst && fwdb.SQLState(err) != "23514" {
				t.Fatalf("insert after closure: %v, want 23514", err)
			}
			var total, active int
			if readErr := db.QueryRowContext(ctx, `SELECT COUNT(*),COUNT(*) FILTER(WHERE is_active) FROM commodore.users WHERE tenant_id=$1`, tenant).Scan(&total, &active); readErr != nil {
				t.Fatal(readErr)
			}
			want := 0
			if insertFirst {
				want = 1
			}
			if total != want || active != 0 {
				t.Fatalf("users=%d active=%d, want %d/0", total, active, want)
			}
		})
	}
}

func (f *operatorAccountQuartermaster) DeleteTenant(_ context.Context, r *quartermasterpb.DeleteTenantRequest) (*emptypb.Empty, error) {
	if r.GetTenantId() != f.closureHealthy {
		return nil, status.Error(codes.Unavailable, "injected tenant failure")
	}
	return &emptypb.Empty{}, nil
}

func testTenantClosureFailuresDoNotStarveLaterTenants(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	healthy := uuid.NewString()
	fake := &operatorAccountQuartermaster{closureHealthy: healthy}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rpc := grpc.NewServer()
	quartermasterpb.RegisterTenantServiceServer(rpc, fake)
	go func() { _ = rpc.Serve(listener) }()
	defer rpc.Stop()
	qm, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{GRPCAddr: listener.Addr().String(), AllowInsecure: true, Logger: logging.NewLogger(), ServiceToken: "test", PreferServiceToken: true})
	if err != nil {
		t.Fatal(err)
	}
	defer qm.Close()
	// First 100 older rows are permanently failing; the last must still be tried.
	for i := 0; i < 101; i++ {
		tenant := uuid.NewString()
		if i == 100 {
			tenant = healthy
		}
		if _, writeErr := db.ExecContext(ctx, `INSERT INTO commodore.tenant_account_states(tenant_id,closed_at) VALUES($1,NOW()-INTERVAL '1 day'+$2*INTERVAL '1 second')`, tenant, i); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	server := &CommodoreServer{db: db, logger: logging.NewLogger(), quartermasterClient: qm}
	server.retryTenantAccountClosuresOnce(ctx)
	server.retryTenantAccountClosuresOnce(ctx)
	var done bool
	var failed int
	if readErr := db.QueryRowContext(ctx, `SELECT quartermaster_closed_at IS NOT NULL FROM commodore.tenant_account_states WHERE tenant_id=$1`, healthy).Scan(&done); readErr != nil {
		t.Fatal(readErr)
	}
	if readErr := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM commodore.tenant_account_states WHERE closure_error IS NOT NULL AND closure_retry_after>NOW() AND closed_at<NOW()-INTERVAL '23 hours'`).Scan(&failed); readErr != nil {
		t.Fatal(readErr)
	}
	if !done || failed != 100 {
		t.Fatalf("healthy completed=%v failed with backoff=%d, want true/100", done, failed)
	}
	if count, err := commodoredb.New(db).CountRepeatedTenantAccountClosureFailures(ctx); err != nil || count != 0 {
		t.Fatalf("one closure failure counted as repeated: %d,%v", count, err)
	}
	var duplicateTenant string
	var duplicateAttempt sql.NullTime
	if err := db.QueryRow(`SELECT tenant_id::text,closure_attempted_at FROM commodore.tenant_account_states WHERE closure_error IS NOT NULL AND closed_at<NOW()-INTERVAL '23 hours' LIMIT 1`).Scan(&duplicateTenant, &duplicateAttempt); err != nil {
		t.Fatal(err)
	}
	if err := commodoredb.New(db).RecordTenantAccountClosureFailure(ctx, commodoredb.RecordTenantAccountClosureFailureParams{TenantID: duplicateTenant, AttemptedAt: duplicateAttempt, Failure: "same attempt reported twice"}); err != nil {
		t.Fatal(err)
	}
	if count, err := commodoredb.New(db).CountRepeatedTenantAccountClosureFailures(ctx); err != nil || count != 0 {
		t.Fatalf("duplicate closure report counted as another attempt: %d,%v", count, err)
	}

	if _, err := db.ExecContext(ctx, `UPDATE commodore.tenant_account_states SET closure_retry_after=NOW()-INTERVAL '1 second' WHERE quartermaster_closed_at IS NULL AND closure_error IS NOT NULL AND closed_at<NOW()-INTERVAL '23 hours'`); err != nil {
		t.Fatal(err)
	}
	server.retryTenantAccountClosuresOnce(ctx)
	if count, err := commodoredb.New(db).CountRepeatedTenantAccountClosureFailures(ctx); err != nil || count != 100 {
		t.Fatalf("second closure failure not counted: %d,%v", count, err)
	}
	// A claimed row is invisible to a concurrent replica until its lease expires.
	tenant := uuid.NewString()
	if _, writeErr := db.ExecContext(ctx, `INSERT INTO commodore.tenant_account_states(tenant_id,closed_at) VALUES($1,NOW()),($2,NOW())`, tenant, uuid.NewString()); writeErr != nil {
		t.Fatal(writeErr)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	claimed, err := commodoredb.New(tx).ClaimPendingTenantAccountClosures(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claim=%v", claimed)
	}
	other, err := commodoredb.New(db).ClaimPendingTenantAccountClosures(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 1 || other[0].TenantID == claimed[0].TenantID {
		t.Fatalf("replica should claim the next unclaimed closure: %v", other)
	}
	remaining, err := commodoredb.New(db).ClaimPendingTenantAccountClosures(ctx)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("replica claimed in-flight or leased rows: %v err=%v", remaining, err)
	}
}

func testInactiveOwnerPublishingAuthority(t *testing.T, db *sql.DB) {
	t.Helper()
	fixture := newStickyOriginFixture(t, db)
	fixture.compiler.tokenHasher = testTokenHasher(t)
	op := context.WithValue(ctxAsOperator(uuid.NewString(), obligationTenantID, "owner"), ctxkeys.KeyAuthType, "jwt")
	if _, writeErr := db.ExecContext(fixture.ctx, `INSERT INTO commodore.streams(id,tenant_id,user_id,internal_name,stream_key,playback_id,title) VALUES($1,$2,$2,'other-owned','other-owned-key','other-owned-playback','Other owned')`, uuid.NewString(), obligationTenantID); writeErr != nil {
		t.Fatal(writeErr)
	}
	for _, active := range []bool{false, true} {
		before := fixture.refreshes()

		if _, adminSetUserActiveErr3 := fixture.compiler.AdminSetUserActive(op, &commodorepb.AdminSetUserActiveRequest{TenantId: obligationTenantID, UserId: obligationTenantID, Active: active, Reason: "offline publishing permission"}); adminSetUserActiveErr3 != nil {
			t.Fatal(adminSetUserActiveErr3)
		}
		if got := fixture.refreshes(); got != before+1 {
			t.Fatalf("owner access refresh revision=%d, want %d", got, before+1)
		}
		var queued int
		if readErr := db.QueryRowContext(fixture.ctx, `SELECT COUNT(*) FROM commodore.media_authority_refresh_obligations WHERE tenant_id=$1 AND lane='event' AND last_reason='user_active_changed'`, obligationTenantID).Scan(&queued); readErr != nil {
			t.Fatal(readErr)
		}
		if queued != 2 {
			t.Fatalf("owned stream obligations=%d, want 2", queued)
		}
		if !active {
			fixture.compiler.purserClient = startPurserEntitlementFake(t, &purserEntitlementFake{subscription: func(context.Context, *purserpb.GetSubscriptionRequest) (*purserpb.GetSubscriptionResponse, error) {
				return nil, status.Error(codes.Unavailable, "injected process dependency failure")
			}})
			if compileLiveStreamAuthorityErr := fixture.compiler.compileLiveStreamAuthority(withMediaAuthorityInUse(fixture.ctx, true), stickyStreamID); compileLiveStreamAuthorityErr == nil {
				t.Fatal("expected injected compile failure")
			}
			denied, err := commodoredb.New(db).GetCurrentMediaAuthorityPayload(fixture.ctx, commodoredb.GetCurrentMediaAuthorityPayloadParams{AuthorityKind: "media_object", AuthorityID: sharedauthority.LiveStreamAuthorityID(stickyStreamID)})
			if err != nil {
				t.Fatal(err)
			}
			payload := &mediapb.MediaObjectAuthority{}
			if unmarshalErr := proto.Unmarshal(denied.Payload, payload); unmarshalErr != nil {
				t.Fatal(unmarshalErr)
			}
			if len(payload.GetLiveStream().GetPublishingCredentialSha256()) != 0 {
				t.Fatal("failed owner refresh retained publishing credential")
			}
			if payload.GetLifecycle() != mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE || payload.GetPlaybackPolicy().GetKind() != mediapb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC {
				t.Fatal("publishing-only safety revocation disabled playback")
			}
			fixture.compiler.purserClient = nil
		}
		fixture.compile()
		current, err := commodoredb.New(db).GetCurrentMediaAuthorityPayload(fixture.ctx, commodoredb.GetCurrentMediaAuthorityPayloadParams{AuthorityKind: "media_object", AuthorityID: sharedauthority.LiveStreamAuthorityID(stickyStreamID)})
		if err != nil {
			t.Fatal(err)
		}
		payload := &mediapb.MediaObjectAuthority{}
		if unmarshalErr2 := proto.Unmarshal(current.Payload, payload); unmarshalErr2 != nil {
			t.Fatal(unmarshalErr2)
		}
		credential := payload.GetLiveStream().GetPublishingCredentialSha256()
		if (len(credential) > 0) != active {
			t.Fatalf("owner active=%v signed credential bytes=%d; inactive owner must have no publishing credential", active, len(credential))
		}
		if active && !sharedauthority.VerifyPublishingCredential(stickyStreamKey, credential) {
			t.Fatal("activation did not restore the original publishing credential")
		}
		if payload.GetLifecycle() != mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE || payload.GetPlaybackPolicy().GetKind() != mediapb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC {
			t.Fatalf("owner access changed playback: %v", payload)
		}
	}
}

// Placement authorities retain playback when the publishing correction can be
// issued, and publish a deny if a commercial dependency prevents that correction.
func testPlacementOwnerPublishingRevocation(t *testing.T, db *sql.DB) {
	for _, test := range []struct{ commercialDown, earlyFailure, rotatedSecret, missingRevision bool }{{}, {commercialDown: true}, {earlyFailure: true}, {earlyFailure: true, rotatedSecret: true}, {earlyFailure: true, rotatedSecret: true, missingRevision: true}} {
		t.Run(fmt.Sprintf("placement publishing revocation commercial_down=%v early_failure=%v rotated_secret=%v", test.commercialDown, test.earlyFailure, test.rotatedSecret), func(t *testing.T) {
			ctx := context.Background()
			tenant, object := quotedCommercialAuthorityFixture()
			tenant.TenantId = uuid.NewString()
			object.TenantId = tenant.TenantId
			object.GetLiveStream().StreamId = uuid.NewString()
			object.OriginClusterId = "official"
			object.GetLiveStream().OutageIngestClusterId = "official"
			object.InternalName = object.GetLiveStream().StreamId
			object.PlaybackId = object.InternalName
			object.GetLiveStream().PublishingCredentialSha256 = sharedauthority.PublishingCredentialDigest("owner-key")
			if _, writeErr := db.ExecContext(ctx, `INSERT INTO commodore.users(id,tenant_id,email,is_active) VALUES($1::text::uuid,$1::text::uuid,$1::text||'@fixture.test',true)`, tenant.TenantId); writeErr != nil {
				t.Fatal(writeErr)
			}
			if _, writeErr := db.ExecContext(ctx, `INSERT INTO commodore.streams(id,tenant_id,user_id,stream_key,playback_id,internal_name,title) VALUES($1::text::uuid,$2,$2,$1::text,$1::text,$1::text,'placement owner')`, object.GetLiveStream().StreamId, tenant.TenantId); writeErr != nil {
				t.Fatal(writeErr)
			}
			if actionErr := seedCommercialTenantVersion(ctx, db, tenant, 1); actionErr != nil {
				t.Fatal(actionErr)
			}
			server := &CommodoreServer{db: db, logger: logging.NewLogger(), tokenHasher: testTokenHasher(t), mediaAuthorityKeyID: "owner-test", mediaAuthorityPrivateKey: ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))}
			server.authorityCommercialSource = commercialSourceFunc(func(_ context.Context, request *placementpb.CommercialQuoteRequest) (*placementpb.CommercialQuoteResponse, error) {
				return commercialResponse(tenant, request)
			})
			id := sharedauthority.LiveStreamAuthorityID(object.GetLiveStream().GetStreamId())
			now := time.Now().UTC().Truncate(time.Microsecond)
			sourceCtx := withPlaybackAccessSource(withMediaAuthorityInUse(ctx, true), false, "", "")
			if test.rotatedSecret {
				object.PlaybackPolicy = &mediapb.PlaybackPolicy{Kind: mediapb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_WEBHOOK}
				object.SealedPlaybackSecrets = []*mediapb.SealedCellSecret{{AudienceCellId: "cell-a", RecipientKeyId: "old-secret", EphemeralPublicKey: make([]byte, 32), Nonce: make([]byte, 12), Ciphertext: make([]byte, 16)}}
				sourceCtx = withPlaybackAccessSource(sourceCtx, true, `{"type":"webhook"}`, "old-encrypted-secret")
			}
			originalUntil := now.Add(time.Minute)
			if persistMediaObjectAuthorityErr := server.persistMediaObjectAuthority(sourceCtx, id, object, []string{"cell-a"}, []*mediapb.AuthoritySourceRevision{{Service: "commodore", Revision: "original-source"}}, now, originalUntil); persistMediaObjectAuthorityErr != nil {
				t.Fatal(persistMediaObjectAuthorityErr)
			}
			if test.missingRevision {
				if _, removeErr := db.ExecContext(ctx, `UPDATE commodore.media_authority_versions SET source_revisions='[{"service":"commodore","revision":"original-source"}]'::jsonb WHERE authority_kind='media_object' AND authority_id=$1`, id); removeErr != nil {
					t.Fatal(removeErr)
				}
			}
			op := context.WithValue(ctxAsOperator(uuid.NewString(), tenant.TenantId, "owner"), ctxkeys.KeyAuthType, "jwt")
			if _, adminSetUserActiveErr4 := server.AdminSetUserActive(op, &commodorepb.AdminSetUserActiveRequest{TenantId: tenant.TenantId, UserId: tenant.TenantId, Active: false, Reason: "owner departure"}); adminSetUserActiveErr4 != nil {
				t.Fatal(adminSetUserActiveErr4)
			}
			server.purserClient = startPurserEntitlementFake(t, &purserEntitlementFake{subscription: func(context.Context, *purserpb.GetSubscriptionRequest) (*purserpb.GetSubscriptionResponse, error) {
				return nil, status.Error(codes.Unavailable, "process dependency down")
			}})
			if test.commercialDown {
				server.authorityCommercialSource = nil
			}
			if test.earlyFailure {
				failure := errors.New("dependency failed before playback policy compilation")
				callCtx := withPlaybackAccessSource(withMediaAuthorityLongValidity(withMediaAuthorityInUse(ctx, true), now.Add(24*time.Hour)), false, "", "")
				if test.rotatedSecret {
					callCtx = withPlaybackAccessSource(callCtx, true, `{"type":"webhook"}`, "rotated-encrypted-secret")
				}
				if revokeChangedAccessOnCompileFailureErr := server.revokeChangedAccessOnCompileFailure(callCtx, id, mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM, nil, &mediapb.LiveStreamAuthority{IngestMode: "push"}, failure); !errors.Is(revokeChangedAccessOnCompileFailureErr, failure) {
					t.Fatalf("early publishing correction lost compile retry: %v", revokeChangedAccessOnCompileFailureErr)
				}
			} else if compileLiveStreamAuthorityErr2 := server.compileLiveStreamAuthority(withMediaAuthorityInUse(ctx, true), object.GetLiveStream().GetStreamId()); compileLiveStreamAuthorityErr2 == nil {
				t.Fatal("expected dependency failure")
			}
			current, err := commodoredb.New(db).GetCurrentMediaAuthorityPayload(ctx, commodoredb.GetCurrentMediaAuthorityPayloadParams{AuthorityKind: "media_object", AuthorityID: id})
			if err != nil {
				t.Fatal(err)
			}
			got := &mediapb.MediaObjectAuthority{}
			if unmarshalErr3 := proto.Unmarshal(current.Payload, got); unmarshalErr3 != nil {
				t.Fatal(unmarshalErr3)
			}
			if test.commercialDown || test.rotatedSecret {
				if got.GetLifecycle() != mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_INACTIVE {
					t.Fatal("failed publishing correction retained an active publishing credential")
				}
			} else if len(got.GetLiveStream().GetPublishingCredentialSha256()) != 0 || got.GetLifecycle() != mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE || got.GetPlaybackPolicy().GetKind() != mediapb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC {
				t.Fatal("placement publishing correction did not revoke only the credential")
			}
			if !test.commercialDown && !test.rotatedSecret {
				if current.ValidUntil.After(originalUntil) {
					t.Fatalf("publishing correction extended validity: %s > %s", current.ValidUntil, originalUntil)
				}
				snapshot, err := commodoredb.New(db).GetCurrentMediaAuthorityRevocationSnapshot(ctx, commodoredb.GetCurrentMediaAuthorityRevocationSnapshotParams{AuthorityKind: "media_object", AuthorityID: id})
				if err != nil || !strings.Contains(string(snapshot.SourceRevisions), "original-source") {
					t.Fatalf("publishing correction lost source provenance: %s err=%v", snapshot.SourceRevisions, err)
				}
			}
			var deliveries int
			if readErr := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM commodore.media_authority_deliveries d JOIN commodore.media_authority_current c USING(authority_kind,authority_id,authority_version) WHERE d.authority_kind='media_object' AND d.authority_id=$1 AND d.cell_id='cell-a'`, id).Scan(&deliveries); readErr != nil {
				t.Fatal(readErr)
			}
			if deliveries != 1 {
				t.Fatalf("revocation deliveries=%d, want cell-a", deliveries)
			}
		})
	}
}

func testAccountHistoryStablePaging(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	tenant := uuid.NewString()
	operator := uuid.NewString()
	op := context.WithValue(ctxAsOperator(operator, tenant, "owner"), ctxkeys.KeyAuthType, "jwt")
	at := time.Now().UTC().Truncate(time.Microsecond)
	for _, id := range []string{"90000000-0000-4000-8000-000000000001", "90000000-0000-4000-8000-000000000002", "90000000-0000-4000-8000-000000000003"} {
		if _, writeErr := db.ExecContext(ctx, `INSERT INTO commodore.operator_account_audit(id,tenant_id,actor_user_id,target_id,action,reason,created_at) VALUES($1,$2,$3,$3,'user_active:false','private',$4)`, id, tenant, operator, at); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	server := &CommodoreServer{db: db}
	page, err := server.AdminListAccountHistory(op, &commodorepb.AdminListAccountHistoryRequest{TenantId: " " + tenant + " ", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.GetEntries()) != 1 || !page.GetHasMore() || page.GetNextCursor() == "" || page.Entries[0].Id != "90000000-0000-4000-8000-000000000003" {
		t.Fatalf("first history page=%v", page)
	}
	if _, writeErr := db.ExecContext(ctx, `INSERT INTO commodore.operator_account_audit(tenant_id,actor_user_id,target_id,action,reason,created_at) VALUES($1,$2,$2,'user_active:true','newer',$3)`, tenant, operator, at.Add(time.Second)); writeErr != nil {
		t.Fatal(writeErr)
	}
	next, err := server.AdminListAccountHistory(op, &commodorepb.AdminListAccountHistoryRequest{TenantId: tenant, Limit: 2, Before: page.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.GetEntries()) != 2 || next.GetHasMore() || next.Entries[0].Id != "90000000-0000-4000-8000-000000000002" || next.Entries[1].Id != "90000000-0000-4000-8000-000000000001" {
		t.Fatalf("concurrent insert shifted keyset history page=%v", next)
	}
}
