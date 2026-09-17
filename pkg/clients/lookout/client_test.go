package lookout

import (
	"context"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"google.golang.org/grpc/metadata"
)

func TestAttachAuthMetadataUsesServiceTokenWithoutJWT(t *testing.T) {
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs(
		"authorization", "Bearer stale",
		"x-tenant-id", "stale-tenant",
		"x-trace-id", "trace-1",
	))
	md, _ := metadata.FromOutgoingContext(attachAuthMetadata(ctx, "service-secret"))
	if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer service-secret" {
		t.Fatalf("authorization = %v", got)
	}
	if got := md.Get("x-tenant-id"); len(got) != 0 {
		t.Fatalf("stale x-tenant-id forwarded: %v", got)
	}
	if got := md.Get("x-trace-id"); len(got) != 1 || got[0] != "trace-1" {
		t.Fatalf("unrelated metadata = %v", got)
	}
}

func TestAttachAuthMetadataForwardsCallerJWTAndIdentity(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyJWTToken, "caller-jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "user-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-1")
	md, _ := metadata.FromOutgoingContext(attachAuthMetadata(ctx, "service-secret"))
	if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer caller-jwt" {
		t.Fatalf("authorization = %v", got)
	}
	if got := md.Get("x-user-id"); len(got) != 1 || got[0] != "user-1" {
		t.Fatalf("x-user-id = %v", got)
	}
	if got := md.Get("x-tenant-id"); len(got) != 1 || got[0] != "tenant-1" {
		t.Fatalf("x-tenant-id = %v", got)
	}
}
