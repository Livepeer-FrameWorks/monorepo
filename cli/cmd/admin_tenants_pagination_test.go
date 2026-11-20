package cmd

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type adminTenantPages func(context.Context, *commonpb.CursorPaginationRequest) (*quartermasterpb.ListTenantsResponse, error)

func (f adminTenantPages) ListTenants(ctx context.Context, page *commonpb.CursorPaginationRequest) (*quartermasterpb.ListTenantsResponse, error) {
	return f(ctx, page)
}

func TestRunTenantsListReadsAllPages(t *testing.T) {
	for _, outputJSON := range []bool{false, true} {
		t.Run(fmt.Sprintf("json=%v", outputJSON), func(t *testing.T) {
			calls := 0
			client := adminTenantPages(func(_ context.Context, page *commonpb.CursorPaginationRequest) (*quartermasterpb.ListTenantsResponse, error) {
				start := calls * 50
				wantCursor := ""
				if calls > 0 {
					wantCursor = fmt.Sprintf("tenant-%03d", start)
				}
				if page.GetAfter() != wantCursor || calls >= 3 {
					t.Fatalf("page %d cursor=%q; want %q", calls, page.GetAfter(), wantCursor)
				}
				calls++
				end := min(start+50, 120)
				response := &quartermasterpb.ListTenantsResponse{Pagination: &commonpb.CursorPaginationResponse{
					TotalCount: 120, HasNextPage: end < 120, EndCursor: proto.String(fmt.Sprintf("tenant-%03d", end)),
				}}
				for index := start; index < end; index++ {
					response.Tenants = append(response.Tenants, &quartermasterpb.Tenant{Id: fmt.Sprintf("tenant-%03d", index+1), Name: "Tenant"})
				}
				return response, nil
			})
			var output bytes.Buffer
			if err := runTenantsList(context.Background(), &output, client, "", outputJSON); err != nil {
				t.Fatal(err)
			}
			if outputJSON {
				var response quartermasterpb.ListTenantsResponse
				if err := protojson.Unmarshal(output.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if len(response.GetTenants()) != 120 || response.GetPagination().GetHasNextPage() {
					t.Fatalf("printed %d tenants, next=%v; want 120 and no more pages", len(response.GetTenants()), response.GetPagination().GetHasNextPage())
				}
			} else if strings.Count(output.String(), "(id=tenant-") != 120 || !strings.Contains(output.String(), "Tenants (120)") {
				t.Fatalf("printed %d tenants; want 120", strings.Count(output.String(), "(id=tenant-"))
			}
			if calls != 3 {
				t.Fatalf("read %d pages; want 3", calls)
			}
		})
	}
}

func TestTenantPagesHaveIndependentRPCContexts(t *testing.T) {
	var previous context.Context
	calls := 0
	client := adminTenantPages(func(ctx context.Context, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ListTenantsResponse, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("page has no RPC deadline")
		}
		if previous != nil && (ctx == previous || previous.Err() != context.Canceled) {
			t.Fatal("tenant pages share an RPC deadline/context")
		}
		previous = ctx
		calls++
		return &quartermasterpb.ListTenantsResponse{Pagination: &commonpb.CursorPaginationResponse{HasNextPage: calls == 1, EndCursor: proto.String("next")}}, nil
	})
	var output bytes.Buffer
	if err := runTenantsList(context.Background(), &output, client, "", false); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("got %d pages", calls)
	}
}
