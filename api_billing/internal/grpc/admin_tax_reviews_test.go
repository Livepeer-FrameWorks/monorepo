package grpc

import (
	"context"
	"frameworks/api_billing/internal/appconfig/appconfigtest"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestBillingTaxReviewsRequireOperatorAndExplicitScope(t *testing.T) {
	s := &PurserServer{}
	for _, auth := range []string{"", "service", "api_token"} {
		ctx := context.WithValue(context.WithValue(t.Context(), ctxkeys.KeyAuthType, auth), ctxkeys.KeyPlatformOperator, true)
		if _, err := s.AdminListBillingTaxReviews(ctx, &purserpb.AdminListBillingTaxReviewsRequest{}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("list auth=%s: %v", auth, err)
		}
		if _, err := s.AdminAcknowledgeBillingTaxReview(ctx, &purserpb.AdminAcknowledgeBillingTaxReviewRequest{}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("ack auth=%s: %v", auth, err)
		}
	}
	ctx := context.WithValue(grantNoticeOperatorCtx(), ctxkeys.KeyUserID, uuid.NewString())
	for _, req := range []*purserpb.AdminListBillingTaxReviewsRequest{
		{}, {TenantId: uuid.NewString(), Category: "tax_document_missing"}, {TenantId: uuid.NewString(), Category: "vies_revalidation"}, {TenantId: uuid.NewString(), Provider: "str\nipe"}, {TenantId: uuid.NewString(), Limit: 101},
	} {
		if _, err := s.AdminListBillingTaxReviews(ctx, req); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid scope reached storage: %+v %v", req, err)
		}
	}
	if _, err := s.AdminAcknowledgeBillingTaxReview(ctx, &purserpb.AdminAcknowledgeBillingTaxReviewRequest{TenantId: uuid.NewString(), ReviewId: uuid.NewString(), EvidenceSha256: "bad", Reason: "reviewed"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("ack without reviewed evidence reached storage: %v", err)
	}
}

func TestCryptoTaxReadinessRequiresCompleteDutchSupplier(t *testing.T) {
	for key, value := range map[string]string{"SUPPLIER_NAME": "DDVTech B.V.", "SUPPLIER_ADDRESS": "Leiden, NL", "SUPPLIER_VAT_NUMBER": "NL123456789B01", "SUPPLIER_REGISTRATION_NUMBER": "12345678", "SUPPLIER_COUNTRY": "NL"} {
		appconfigtest.Set(t, key, value)
	}
	if err := cryptoTaxSupplierReadiness(); err != nil {
		t.Fatal(err)
	}
	t.Run("incomplete", func(t *testing.T) {
		appconfigtest.Set(t, "SUPPLIER_ADDRESS", "  ")
		if err := cryptoTaxSupplierReadiness(); err == nil {
			t.Fatal("incomplete supplier identity passed readiness")
		}
	})
	t.Run("unsupported country", func(t *testing.T) {
		appconfigtest.Set(t, "SUPPLIER_COUNTRY", "FR")
		if err := cryptoTaxSupplierReadiness(); err == nil {
			t.Fatal("non-Dutch supplier passed Dutch-policy readiness")
		}
	})
}
