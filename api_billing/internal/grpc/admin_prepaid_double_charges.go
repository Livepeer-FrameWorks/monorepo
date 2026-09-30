package grpc

import (
	"context"
	"strings"

	"frameworks/api_billing/internal/database/purserdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const defaultPrepaidDoubleChargeLimit = 500

// AdminListPrepaidDoubleCharges lists the finalized usage invoices that rated
// again usage a tenant's prepaid balance had already paid as it was reported,
// before prepaid periods closed with statements. The amount charged twice is
// the invoice's usage up to what prepaid settlements took for the period. It
// only reads; the operator refunds.
func (s *PurserServer) AdminListPrepaidDoubleCharges(ctx context.Context, req *purserpb.AdminListPrepaidDoubleChargesRequest) (*purserpb.AdminListPrepaidDoubleChargesResponse, error) {
	tenantID := strings.TrimSpace(req.GetTenantId())
	if tenantID != "" {
		if _, err := uuid.Parse(tenantID); err != nil {
			return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
		}
	}
	limit := req.GetLimit()
	if limit < 0 {
		return nil, status.Error(codes.InvalidArgument, "limit must not be negative")
	}
	if limit == 0 {
		limit = defaultPrepaidDoubleChargeLimit
	}
	rows, err := purserdb.New(s.db).ListPrepaidDoubleCharges(ctx, purserdb.ListPrepaidDoubleChargesParams{
		FilterTenant: tenantID != "", TenantID: tenantID, ResultLimit: limit + 1,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list prepaid double charges: %v", err)
	}
	resp := &purserpb.AdminListPrepaidDoubleChargesResponse{}
	if len(rows) > int(limit) {
		rows, resp.Truncated = rows[:limit], true
	}
	for _, row := range rows {
		charge, convertErr := prepaidDoubleCharge(row)
		if convertErr != nil {
			return nil, status.Errorf(codes.Internal, "invoice %s: %v", row.InvoiceID, convertErr)
		}
		resp.Charges = append(resp.Charges, charge)
		resp.TotalDoubleChargedCents += charge.GetDoubleChargedCents()
	}
	s.logger.WithFields(logging.Fields{
		"operator":                   middleware.GetUserID(ctx),
		"tenant_id":                  tenantID,
		"invoices":                   len(resp.GetCharges()),
		"total_double_charged_cents": resp.GetTotalDoubleChargedCents(),
		"truncated":                  resp.GetTruncated(),
	}).Info("Listed prepaid double charges")
	return resp, nil
}

func prepaidDoubleCharge(row purserdb.ListPrepaidDoubleChargesRow) (*purserpb.PrepaidDoubleCharge, error) {
	cents := func(amount string) (int64, error) {
		value, err := decimal.NewFromString(amount)
		if err != nil {
			return 0, err
		}
		return value.Shift(2).Round(0).IntPart(), nil
	}
	amountCents, err := cents(row.Amount)
	if err != nil {
		return nil, err
	}
	creditCents, err := cents(row.PrepaidCreditApplied)
	if err != nil {
		return nil, err
	}
	usageCents, err := cents(row.UsageAmount)
	if err != nil {
		return nil, err
	}
	paidCents := max(decimal.New(row.SettledMicro, -4).Round(0).IntPart(), 0)
	return &purserpb.PrepaidDoubleCharge{
		TenantId:               row.TenantID,
		InvoiceId:              row.InvoiceID,
		InvoiceNumber:          row.InvoiceNumber,
		Status:                 row.Status,
		PeriodStart:            timestamppb.New(row.PeriodStart),
		PeriodEnd:              timestamppb.New(row.PeriodEnd),
		InvoiceAmountCents:     amountCents,
		InvoiceCreditCents:     creditCents,
		InvoiceUsageCents:      usageCents,
		PrepaidUsagePaidCents:  paidCents,
		DoubleChargedCents:     max(min(usageCents, paidCents), 0),
		PresentmentAmountCents: row.PresentmentAmountCents,
		PresentmentCurrency:    row.PresentmentCurrency,
	}, nil
}
