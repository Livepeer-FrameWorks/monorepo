package resolvers

import (
	"context"
	"testing"
	"time"

	"frameworks/api_gateway/internal/clients/clientstest"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestDoGetPaymentUsesTenantScopedClient(t *testing.T) {
	r := purserResolver(&clientstest.FakePurser{
		GetPaymentFn: func(_ context.Context, paymentID string) (*purserpb.Payment, error) {
			if paymentID != "pay-1" {
				t.Fatalf("payment id = %q", paymentID)
			}
			return &purserpb.Payment{Id: paymentID, InvoiceId: "inv-1"}, nil
		},
	})
	payment, err := r.DoGetPayment(clientstest.AuthedCtx("tenant-1"), "pay-1")
	if err != nil || payment.GetInvoiceId() != "inv-1" {
		t.Fatalf("payment = %+v, err = %v", payment, err)
	}
}

func TestDoGetPaymentsConnectionPreservesFiltersAndPagination(t *testing.T) {
	now := time.Now()
	invoiceID, paymentStatus, method := "inv-1", "failed", "card"
	r := purserResolver(&clientstest.FakePurser{
		ListPaymentsFn: func(_ context.Context, req *purserpb.ListPaymentsRequest) (*purserpb.ListPaymentsResponse, error) {
			if req.GetTenantId() != "tenant-1" || req.GetInvoiceId() != invoiceID || req.GetStatus() != paymentStatus || req.GetMethod() != method {
				t.Fatalf("filters = %+v", req)
			}
			if req.GetPagination().GetFirst() != 20 {
				t.Fatalf("pagination = %+v", req.GetPagination())
			}
			return &purserpb.ListPaymentsResponse{
				Payments: []*purserpb.Payment{{
					Id: "pay-1", InvoiceId: invoiceID, Status: paymentStatus, Method: method,
					CreatedAt: timestamppb.New(now), UpdatedAt: timestamppb.New(now),
				}},
				Pagination: &commonpb.CursorPaginationResponse{TotalCount: 1},
			}, nil
		},
	})
	first := 20
	conn, err := r.DoGetPaymentsConnection(
		clientstest.AuthedCtx("tenant-1"), &first, nil, nil, nil,
		&invoiceID, &paymentStatus, &method,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(conn.Edges) != 1 || len(conn.Nodes) != 1 || conn.TotalCount != 1 || conn.Edges[0].Cursor == "" {
		t.Fatalf("connection = %+v", conn)
	}
}

func TestDoGetPaymentsConnectionRequiresTenant(t *testing.T) {
	r := &Resolver{Logger: clientstest.DiscardLogger()}
	if _, err := r.DoGetPaymentsConnection(context.Background(), nil, nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("missing tenant should fail before client access")
	}
}
