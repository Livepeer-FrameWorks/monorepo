package grpc

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"frameworks/api_billing/internal/database/purserdb"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *PurserServer) getProviderBillingDocument(ctx context.Context, tenant, id, kind string) (*purserpb.GetBillingDocumentResponse, error) {
	doc, err := purserdb.New(s.db).GetProviderBillingDocument(ctx, purserdb.GetProviderBillingDocumentParams{TenantID: tenant, DocumentID: id, DocumentKind: kind})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "billing document not found")
	}
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to load provider document")
	}
	if len(doc.OriginalPdf) == 0 || !doc.PdfSha256.Valid {
		return nil, status.Error(codes.FailedPrecondition, "original provider PDF is awaiting capture")
	}
	digest := sha256.Sum256(doc.OriginalPdf)
	if hex.EncodeToString(digest[:]) != doc.PdfSha256.String {
		return nil, status.Error(codes.DataLoss, "provider document integrity check failed")
	}
	row := billingDocumentRow{id: doc.ID, kind: kind, number: doc.OriginalNumber, amountCents: doc.GrossMinor, currency: doc.Currency, status: doc.Status, issuedAt: doc.IssuedAt, retentionUntil: doc.RetentionUntil}
	if doc.Currency == "EUR" {
		row.eurAmountCents = sql.NullInt64{Int64: doc.GrossMinor, Valid: true}
		row.netEURCents = doc.NetMinor
		row.vatEURCents = doc.VatMinor
	}
	return &purserpb.GetBillingDocumentResponse{Document: billingDocumentProto(row), ContentType: billingDocumentContentType, Content: doc.OriginalPdf, Sha256: doc.PdfSha256.String}, nil
}
