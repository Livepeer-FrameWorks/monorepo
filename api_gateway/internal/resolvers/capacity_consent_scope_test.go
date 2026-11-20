package resolvers

import (
	"context"
	"testing"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
)

func TestCapacityConsentWriteIncludesRead(t *testing.T) {
	ctx := streamScopeAPITokenCtx("placement:write")
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "user")
	ctx = context.WithValue(ctx, ctxkeys.KeyRole, "owner")
	resolver := &Resolver{Clients: &clients.ServiceClients{Quartermaster: &clientstest.FakeQuartermaster{}}}
	if denied := resolver.capacityConsentAccess(ctx, false); denied != nil {
		t.Fatalf("placement write did not include read: %+v", denied)
	}
}
