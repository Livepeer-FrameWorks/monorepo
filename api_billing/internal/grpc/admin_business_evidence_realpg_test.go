//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestBusinessEstablishmentReviews_RealPG(t *testing.T) { //nolint:funlen // One schema proves the retained approval and revocation lifecycle.
	db := startPurserTransitionRealPG(t)
	ctx := context.Background()
	tenant, tier := uuid.NewString(), uuid.NewString()
	if _, err := db.Exec(`INSERT INTO purser.billing_tiers(id,tier_name,display_name) VALUES ($1::uuid,$1::text,'Evidence test')`, tier); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO purser.tenant_subscriptions(id,tenant_id,tier_id,billing_customer_type,billing_email,billing_name,billing_address,tax_id) VALUES ($1,$2,$3,'unspecified','buyer@example.test','US Buyer','{"street":"Main 1","city":"Seattle","postal_code":"98101","country":"US"}','US-REG-123')`, uuid.NewString(), tenant, tier); err != nil {
		t.Fatal(err)
	}
	server := &PurserServer{db: db, logger: logging.NewLogger()}
	op := operatorAssignCtx(uuid.NewString())
	inspect := func() (string, int64) {
		t.Helper()
		page, err := server.AdminFinancialAuditPage(op, &purserpb.AdminFinancialAuditPageRequest{TenantId: tenant, Source: "tax_profile", From: timestamppb.New(time.Unix(0, 0))})
		if err != nil {
			t.Fatal(err)
		}
		var profile struct {
			SHA      string `json:"profile_sha256"`
			Revision int64  `json:"evidence_revision"`
		}
		if err := json.Unmarshal(page.RecordsJson[0], &profile); err != nil {
			t.Fatal(err)
		}
		return profile.SHA, profile.Revision
	}
	fingerprint, revision := inspect()
	req := &purserpb.AdminRecordBusinessEvidenceRequest{TenantId: tenant, ProfileSha256: fingerprint, ExpectedRevision: revision, Reference: "retained/KYB-US-1", ValidUntil: timestamppb.New(time.Now().Add(time.Hour))}
	for _, auth := range []string{"", "service", "api_token", "jwt"} {
		unauthorised := context.WithValue(ctx, ctxkeys.KeyAuthType, auth)
		if _, err := (&PurserServer{}).AdminRecordBusinessEvidence(unauthorised, req); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("%s bypassed operator JWT check: %v", auth, err)
		}
	}
	approved, err := server.AdminRecordBusinessEvidence(op, req)
	if err != nil {
		t.Fatal(err)
	}
	if approved.Revision != 1 || approved.Country != "US" || approved.ProfileSha256 != fingerprint {
		t.Fatalf("incorrect approval: %v", approved)
	}
	q := purserdb.New(db)
	query := purserdb.GetBusinessEstablishmentReviewSnapshotParams{TenantID: tenant, ProfileSha256: sql.NullString{String: fingerprint, Valid: true}, Country: sql.NullString{String: "US", Valid: true}, ValidAt: time.Now()}
	if _, err := q.GetBusinessEstablishmentReviewSnapshot(ctx, query); err != nil {
		t.Fatal(err)
	}
	other := query
	other.TenantID = uuid.NewString()
	if _, err := q.GetBusinessEstablishmentReviewSnapshot(ctx, other); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-tenant proof: %v", err)
	}
	if _, err := db.Exec(`UPDATE purser.business_establishment_reviews SET reference='altered' WHERE tenant_id=$1`, tenant); err == nil {
		t.Fatal("retained proof was mutable")
	}
	if _, err := db.Exec(`DELETE FROM purser.business_establishment_reviews WHERE tenant_id=$1`, tenant); err == nil {
		t.Fatal("retained proof was deletable")
	}
	if _, err := server.AdminRecordBusinessEvidence(op, req); status.Code(err) != codes.Aborted {
		t.Fatalf("stale approval revision accepted: %v", err)
	}
	revoked, err := server.AdminRecordBusinessEvidence(op, &purserpb.AdminRecordBusinessEvidenceRequest{TenantId: tenant, Reference: "review withdrawn", Revoke: true})
	if err != nil {
		t.Fatal(err)
	}
	if !revoked.Revoked || revoked.Revision != 2 {
		t.Fatalf("revocation: %v", revoked)
	}
	if _, err := q.GetBusinessEstablishmentReviewSnapshot(ctx, query); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("revocation left approval usable: %v", err)
	}
	req.ExpectedRevision = 2
	req.ProfileSha256 = strings.Repeat("0", 64)
	if _, err := server.AdminRecordBusinessEvidence(op, req); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("changed profile accepted: %v", err)
	}
	req.ProfileSha256 = fingerprint
	approved, err = server.AdminRecordBusinessEvidence(op, req)
	if err != nil {
		t.Fatal(err)
	}
	if approved.Revision != 3 {
		t.Fatalf("reapproval: %v", approved)
	}
	// A newer short-lived review must not fall back to the older, longer-lived one.
	req.ExpectedRevision = 3
	req.ValidUntil = timestamppb.New(time.Now().Add(10 * time.Minute))
	if _, err := server.AdminRecordBusinessEvidence(op, req); err != nil {
		t.Fatal(err)
	}
	query.ValidAt = time.Now().Add(20 * time.Minute)
	if _, err := q.GetBusinessEstablishmentReviewSnapshot(ctx, query); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expired review revived old approval: %v", err)
	}
	// The profile is unique per tenant; cancelled historical rows cannot hide a
	// second active profile from the evidence lock.
	_, duplicateErr := db.Exec(`INSERT INTO purser.tenant_subscriptions(id,tenant_id,tier_id,status) VALUES($1,$2,$3,'cancelled')`, uuid.NewString(), tenant, tier)
	var constraint *pq.Error
	if !errors.As(duplicateErr, &constraint) || constraint.Code != "23505" {
		t.Fatalf("second subscription bypassed tenant uniqueness: %v", duplicateErr)
	}
	if _, err = db.Exec(`UPDATE purser.tenant_subscriptions SET status='cancelled' WHERE tenant_id=$1`, tenant); err != nil {
		t.Fatal(err)
	}
	req.ExpectedRevision = 4
	if _, err := server.AdminRecordBusinessEvidence(op, req); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("cancelled profile received new approval: %v", err)
	}
	if _, err := server.AdminRecordBusinessEvidence(op, &purserpb.AdminRecordBusinessEvidenceRequest{TenantId: tenant, Reference: "cancelled profile revoked", Revoke: true}); err != nil {
		t.Fatalf("cancelled profile could not revoke evidence: %v", err)
	}
}
