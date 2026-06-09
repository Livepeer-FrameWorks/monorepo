package grpc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/grpc/codes"
)

// policyBundleSigningSecret is a trust-boundary resolver: an explicit env
// secret must win verbatim, the SERVICE_TOKEN fallback must be a *derived*
// (not raw) key, and a fully-unset environment must fail closed rather than
// sign bundles with an empty key.
func TestPolicyBundleSigningSecret(t *testing.T) {
	t.Run("explicit_secret_used_verbatim", func(t *testing.T) {
		t.Setenv("POLICY_BUNDLE_SIGNING_SECRET", "explicit-key")
		t.Setenv("SERVICE_TOKEN", "ignored")
		got, err := policyBundleSigningSecret()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(got) != "explicit-key" {
			t.Errorf("secret = %q, want verbatim explicit-key", string(got))
		}
	})

	t.Run("service_token_is_derived_not_raw", func(t *testing.T) {
		t.Setenv("POLICY_BUNDLE_SIGNING_SECRET", "")
		t.Setenv("SERVICE_TOKEN", "svc-tok")
		got, err := policyBundleSigningSecret()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := sha256.Sum256([]byte("policy-bundle-v1:svc-tok"))
		if !bytes.Equal(got, want[:]) {
			t.Errorf("derived secret mismatch")
		}
		if string(got) == "svc-tok" {
			t.Errorf("secret must not be the raw SERVICE_TOKEN")
		}
	})

	t.Run("unset_fails_closed", func(t *testing.T) {
		t.Setenv("POLICY_BUNDLE_SIGNING_SECRET", "")
		t.Setenv("SERVICE_TOKEN", "")
		if _, err := policyBundleSigningSecret(); err == nil {
			t.Errorf("expected error when no secret source is set")
		}
	})
}

// lookupPolicyForStream is the per-stream tenant gate: a stream owned by a
// different tenant must be rejected (PermissionDenied), a missing stream is
// NotFound, and a public stream (empty policy) returns nil policy with no error.
func TestLookupPolicyForStream(t *testing.T) {
	const streamID = "11111111-1111-1111-1111-111111111111"

	t.Run("tenant_mismatch_denied", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("FROM commodore.streams").
			WithArgs(streamID).
			WillReturnRows(sqlmock.NewRows([]string{"policy", "internal_name", "tenant_id"}).
				AddRow("", "live+abc", "other-tenant"))
		_, _, err := s.lookupPolicyForStream(context.Background(), "t1", streamID)
		wantCode(t, err, codes.PermissionDenied)
	})

	t.Run("missing_stream_not_found", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("FROM commodore.streams").
			WithArgs(streamID).
			WillReturnError(sql.ErrNoRows)
		_, _, err := s.lookupPolicyForStream(context.Background(), "t1", streamID)
		wantCode(t, err, codes.NotFound)
	})

	t.Run("public_stream_returns_nil_policy", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("FROM commodore.streams").
			WithArgs(streamID).
			WillReturnRows(sqlmock.NewRows([]string{"policy", "internal_name", "tenant_id"}).
				AddRow("", "live+abc", "t1"))
		policy, name, err := s.lookupPolicyForStream(context.Background(), "t1", streamID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if policy != nil {
			t.Errorf("policy = %q, want nil for public stream", string(policy))
		}
		if name != "live+abc" {
			t.Errorf("internal_name = %q, want live+abc", name)
		}
	})
}

func TestNextPolicyBundleVersion(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	mock.ExpectQuery("policy_bundle_versions").
		WithArgs("t1", "s1").
		WillReturnRows(sqlmock.NewRows([]string{"next"}).AddRow(int64(7)))
	got, err := s.nextPolicyBundleVersion(context.Background(), "t1", "s1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 7 {
		t.Errorf("next version = %d, want 7", got)
	}
}

// GetSignedPolicyBundle is the central trust-minting path. The test asserts the
// emitted JWT actually verifies under the resolved HMAC secret and carries the
// canonical claims (tenant, stream, monotonic version, entitled clusters,
// embedded policy) — i.e. the signed envelope matches what Foghorn reads back.
func TestGetSignedPolicyBundle(t *testing.T) {
	const (
		tenantID = "22222222-2222-2222-2222-222222222222"
		streamID = "33333333-3333-3333-3333-333333333333"
	)

	t.Run("requires_ids", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		t.Setenv("POLICY_BUNDLE_SIGNING_SECRET", "k")
		_, err := s.GetSignedPolicyBundle(context.Background(), &commodorepb.GetSignedPolicyBundleRequest{StreamId: streamID})
		wantCode(t, err, codes.InvalidArgument)
		_, err = s.GetSignedPolicyBundle(context.Background(), &commodorepb.GetSignedPolicyBundleRequest{TenantId: tenantID})
		wantCode(t, err, codes.InvalidArgument)
	})

	t.Run("mints_verifiable_jwt", func(t *testing.T) {
		const secret = "bundle-secret"
		t.Setenv("POLICY_BUNDLE_SIGNING_SECRET", secret)
		s, mock, done := newMockServer(t)
		defer done()

		// 1. per-stream policy + ownership
		mock.ExpectQuery("FROM commodore.streams").
			WithArgs(streamID).
			WillReturnRows(sqlmock.NewRows([]string{"policy", "internal_name", "tenant_id"}).
				AddRow(`{"require_auth":true}`, "live+abc", tenantID))
		// 2. entitled clusters
		mock.ExpectQuery("tenant_cluster_access").
			WithArgs(tenantID).
			WillReturnRows(sqlmock.NewRows([]string{"cluster_id"}).AddRow("cluster-a").AddRow("cluster-b"))
		// 3. plan class
		mock.ExpectQuery("cluster_class").
			WithArgs(tenantID).
			WillReturnRows(sqlmock.NewRows([]string{"cluster_class"}).AddRow("premium"))
		// 4. next monotonic version
		mock.ExpectQuery("MAX").
			WithArgs(tenantID, streamID).
			WillReturnRows(sqlmock.NewRows([]string{"next"}).AddRow(int64(5)))
		// 5. persist
		mock.ExpectExec("INSERT INTO commodore.policy_bundle_versions").
			WithArgs(tenantID, streamID, int64(5), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 1))

		resp, err := s.GetSignedPolicyBundle(context.Background(), &commodorepb.GetSignedPolicyBundleRequest{
			TenantId: tenantID, StreamId: streamID,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		bundle := resp.GetBundle()
		if bundle.GetBundleVersion() != 5 {
			t.Errorf("BundleVersion = %d, want 5", bundle.GetBundleVersion())
		}

		var claims signedBundleClaims
		tok, err := jwt.ParseWithClaims(bundle.GetBundleJwt(), &claims, func(*jwt.Token) (any, error) {
			return []byte(secret), nil
		})
		if err != nil || !tok.Valid {
			t.Fatalf("JWT failed to verify under resolved secret: %v", err)
		}
		if claims.TenantID != tenantID || claims.StreamID != streamID {
			t.Errorf("claims ids = (%s,%s), want (%s,%s)", claims.TenantID, claims.StreamID, tenantID, streamID)
		}
		if claims.BundleVersion != 5 {
			t.Errorf("claims.BundleVersion = %d, want 5", claims.BundleVersion)
		}
		if len(claims.AllowedClusterIDs) != 2 || claims.AllowedClusterIDs[0] != "cluster-a" {
			t.Errorf("AllowedClusterIDs = %v, want [cluster-a cluster-b]", claims.AllowedClusterIDs)
		}
		if claims.TenantPlanClass != "premium" {
			t.Errorf("TenantPlanClass = %q, want premium", claims.TenantPlanClass)
		}
		var pol map[string]any
		if err := json.Unmarshal(claims.PlaybackPolicy, &pol); err != nil || pol["require_auth"] != true {
			t.Errorf("PlaybackPolicy claim not preserved: %v (%v)", string(claims.PlaybackPolicy), err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet: %v", err)
		}
	})
}
