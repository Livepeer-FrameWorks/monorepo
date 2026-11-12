package resolvers

import (
	"context"
	"testing"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type accountSecurityBackend struct {
	commodore.Interface
	list     func(context.Context, *commonpb.CursorPaginationRequest) (*commodorepb.ListSessionsResponse, error)
	revoke   func(context.Context, string) (*commodorepb.RevokeSessionResponse, error)
	others   func(context.Context) (*commodorepb.RevokeOtherSessionsResponse, error)
	rename   func(context.Context, string, string) (*commodorepb.AuthSession, error)
	password func(context.Context, string, string) (*commodorepb.ChangePasswordResponse, error)
}

func (b *accountSecurityBackend) ListSessions(ctx context.Context, p *commonpb.CursorPaginationRequest) (*commodorepb.ListSessionsResponse, error) {
	return b.list(ctx, p)
}
func (b *accountSecurityBackend) RevokeSession(ctx context.Context, id string) (*commodorepb.RevokeSessionResponse, error) {
	return b.revoke(ctx, id)
}
func (b *accountSecurityBackend) RevokeOtherSessions(ctx context.Context) (*commodorepb.RevokeOtherSessionsResponse, error) {
	return b.others(ctx)
}
func (b *accountSecurityBackend) RenameSession(ctx context.Context, id, label string) (*commodorepb.AuthSession, error) {
	return b.rename(ctx, id, label)
}
func (b *accountSecurityBackend) ChangePassword(ctx context.Context, current, next string) (*commodorepb.ChangePasswordResponse, error) {
	return b.password(ctx, current, next)
}

func securityTestContext(kind string) context.Context {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, kind)
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "own-user")
	ctx = context.WithValue(ctx, ctxkeys.KeySessionID, "current")
	return context.WithValue(ctx, ctxkeys.KeyTenantID, "own-tenant")
}

func TestAccountSecurityLegacySessionRequestsRenewalBeforeRPC(t *testing.T) {
	r := &Resolver{}
	ctx := context.WithValue(securityTestContext("jwt"), ctxkeys.KeySessionID, "")
	a, err := r.DoRevokeOtherAuthSessions(ctx)
	if refusal, ok := a.(*model.ValidationError); err != nil || !ok || refusal.Code == nil || *refusal.Code != "SESSION_REFRESH_REQUIRED" {
		t.Fatalf("legacy revoke others=%T %+v %v", a, a, err)
	}
	b, err := r.DoChangePassword(ctx, "old", "new")
	if refusal, ok := b.(*model.ValidationError); err != nil || !ok || refusal.Code == nil || *refusal.Code != "SESSION_REFRESH_REQUIRED" {
		t.Fatalf("legacy password change=%T %+v %v", b, b, err)
	}
}

func TestAccountSecurityLinkEmailRequiresInteractiveNamedSession(t *testing.T) {
	for _, kind := range []string{"jwt", "wallet", "api_token", "service"} {
		t.Run(kind, func(t *testing.T) {
			backend := &clientstest.FakeCommodore{LinkEmailFn: func(context.Context, string, string) (*commodorepb.LinkEmailResponse, error) {
				return nil, status.Error(codes.FailedPrecondition, "renew this session before linking email")
			}}
			r := commoB3(backend)
			ctx := context.WithValue(securityTestContext(kind), ctxkeys.KeySessionID, "")
			result, err := r.DoLinkEmail(ctx, model.LinkEmailInput{Email: "owner@example.test", Password: "new-password"})
			if kind == "jwt" {
				refusal, ok := result.(*model.ValidationError)
				if err != nil || !ok || refusal.Code == nil || *refusal.Code != "SESSION_REFRESH_REQUIRED" {
					t.Fatalf("legacy email-link refusal=%T %+v err=%v", result, result, err)
				}
			} else if _, ok := result.(*model.AuthError); err != nil || !ok {
				t.Fatalf("programmatic email-link refusal=%T %+v err=%v", result, result, err)
			}
			if backend.Calls != 0 {
				t.Fatalf("credential guard made %d RPCs", backend.Calls)
			}
		})
	}
}

func TestAccountSecurityRejectsNonInteractiveCallersBeforeRPC(t *testing.T) {
	r := &Resolver{}
	for _, kind := range []string{"api_token", "wallet_bearer", "wallet", "service", ""} {
		t.Run(kind, func(t *testing.T) {
			ctx := securityTestContext(kind)
			if _, err := r.DoAuthSessionsConnection(ctx, nil, nil); status.Code(err) != codes.PermissionDenied {
				t.Fatalf("list error=%v", err)
			}
			a, err := r.DoRevokeAuthSession(ctx, "other-session")
			if _, ok := a.(*model.AuthError); err != nil || !ok {
				t.Fatalf("revoke returned %T %v", a, err)
			}
			b, err := r.DoRevokeOtherAuthSessions(ctx)
			if _, ok := b.(*model.AuthError); err != nil || !ok {
				t.Fatalf("others returned %T %v", b, err)
			}
			c, err := r.DoRenameAuthSession(ctx, "other-session", "New name")
			if _, ok := c.(*model.AuthError); err != nil || !ok {
				t.Fatalf("rename returned %T %v", c, err)
			}
			d, err := r.DoChangePassword(ctx, "old", "new")
			if _, ok := d.(*model.AuthError); err != nil || !ok {
				t.Fatalf("password returned %T %v", d, err)
			}
		})
	}
	if _, err := r.DoAuthSessionsConnection(context.Background(), nil, nil); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("anonymous list=%v", err)
	}
}

func TestAuthSessionsConnectionPreservesOwnerPaginationAndPrivacy(t *testing.T) {
	now := timestamppb.New(time.Unix(1800000000, 0))
	cursor := "opaque-next-page"
	backend := &accountSecurityBackend{list: func(ctx context.Context, p *commonpb.CursorPaginationRequest) (*commodorepb.ListSessionsResponse, error) {
		if ctxkeys.GetTenantID(ctx) != "own-tenant" || ctxkeys.GetUserID(ctx) != "own-user" || p.GetFirst() != 7 || p.GetAfter() != cursor {
			t.Fatalf("owner/page lost: %+v", p)
		}
		return &commodorepb.ListSessionsResponse{Sessions: []*commodorepb.AuthSession{{Id: "session", ClientId: "tray-mac", AuthMethod: "pkce", DeviceLabel: "My Mac", LastIp: "203.0.113.37", UserAgent: "private user-agent", CreatedAt: now, LastSeenAt: now, ExpiresAt: now, Current: true}}, Pagination: &commonpb.CursorPaginationResponse{TotalCount: 42, HasNextPage: true, EndCursor: &cursor}}, nil
	}}
	r := &Resolver{Clients: &clients.ServiceClients{Commodore: backend}}
	limit := 7
	result, err := r.DoAuthSessionsConnection(securityTestContext("jwt"), &limit, &cursor)
	if err != nil {
		t.Fatal(err)
	}
	if result.TotalCount != 42 || !result.PageInfo.HasNextPage || result.PageInfo.EndCursor == nil || *result.PageInfo.EndCursor != cursor || len(result.Nodes) != 1 || len(result.Edges) != 1 {
		t.Fatalf("pagination lost: %+v", result)
	}
	row := result.Nodes[0]
	if row.IPAddress == nil || *row.IPAddress != "203.0.113.x" || !row.Current || row.ClientKind != model.AuthClientKindDesktop || row.AuthMethod != model.AuthMethodBrowserHandoff || row.Label != "My Mac" {
		t.Fatalf("unsafe/incorrect session mapping: %+v", row)
	}
	for _, invalid := range []int{0, -1, 101, 1 << 40} {
		if _, err := r.DoAuthSessionsConnection(securityTestContext("jwt"), &invalid, nil); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("unbounded first=%d: %v", invalid, err)
		}
	}
}

func TestAuthSessionIPMasking(t *testing.T) {
	for _, tc := range []struct{ ip, want string }{{"203.0.113.37", "203.0.113.x"}, {"::ffff:203.0.113.37", "203.0.113.x"}, {"2001:db8:abcd:1234::8", "2001:db8:abcd::/48"}, {"invalid", ""}, {"", ""}} {
		got := maskedAuthSessionIP(tc.ip)
		if tc.want == "" {
			if got != nil {
				t.Fatalf("invalid address exposed: %q", *got)
			}
		} else if got == nil || *got != tc.want {
			t.Fatalf("mask %q=%v want %q", tc.ip, got, tc.want)
		}
	}
}

func TestAccountSecurityMutationPayloadsAndErrors(t *testing.T) {
	ctx := securityTestContext("jwt")
	backend := &accountSecurityBackend{
		revoke: func(_ context.Context, id string) (*commodorepb.RevokeSessionResponse, error) {
			if id != "current" {
				return nil, status.Error(codes.NotFound, "session not found")
			}
			return &commodorepb.RevokeSessionResponse{Success: true, Current: true}, nil
		},
		others: func(context.Context) (*commodorepb.RevokeOtherSessionsResponse, error) {
			return &commodorepb.RevokeOtherSessionsResponse{RevokedCount: 9}, nil
		},
		password: func(_ context.Context, current, next string) (*commodorepb.ChangePasswordResponse, error) {
			if current != "old" || next != "new" {
				t.Fatal("password proof changed")
			}
			return &commodorepb.ChangePasswordResponse{Success: true, RevokedCount: 3}, nil
		},
	}
	r := &Resolver{Clients: &clients.ServiceClients{Commodore: backend}}
	a, err := r.DoRevokeAuthSession(ctx, "current")
	if p, ok := a.(*model.RevokeAuthSessionPayload); err != nil || !ok || !p.Current || !p.Success {
		t.Fatalf("current revoke %T %+v %v", a, a, err)
	}
	b, err := r.DoRevokeAuthSession(ctx, "foreign")
	if _, ok := b.(*model.NotFoundError); err != nil || !ok {
		t.Fatalf("foreign revoke %T %v", b, err)
	}
	c, err := r.DoRevokeOtherAuthSessions(ctx)
	if p, ok := c.(*model.RevokeOtherAuthSessionsPayload); err != nil || !ok || p.RevokedCount != 9 {
		t.Fatalf("others %T %+v %v", c, c, err)
	}
	d, err := r.DoChangePassword(ctx, "old", "new")
	if p, ok := d.(*model.ChangePasswordPayload); err != nil || !ok || !p.Success || p.RevokedCount != 3 {
		t.Fatalf("password %T %+v %v", d, d, err)
	}
}
