package resolvers

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"
	gatewayerrors "frameworks/api_gateway/internal/errors"
	"frameworks/api_gateway/internal/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// An API token asking to manage API tokens is the caller's authorization
// fault: it is refused as FORBIDDEN and logged as a rejected request, not as a
// platform failure.
func TestDeveloperTokenManagementWithAPITokenIsClientRefusal(t *testing.T) {
	ctx := context.WithValue(clientstest.AuthedCtx("t1"), ctxkeys.KeyAuthType, "api_token")
	r := commoW2(&clientstest.FakeCommodore{})
	calls := map[string]func() error{
		"create": func() error {
			_, err := r.DoCreateDeveloperToken(ctx, model.CreateDeveloperTokenInput{Name: "x"})
			return err
		},
		"revoke": func() error { _, err := r.DoRevokeDeveloperToken(ctx, "tok"); return err },
		"list":   func() error { _, err := r.DoGetDeveloperTokens(ctx); return err },
		"connection": func() error {
			_, err := r.DoGetDeveloperTokensConnection(ctx, nil, nil, nil, nil)
			return err
		},
	}
	for name, call := range calls {
		err := call()
		if !errors.Is(err, middleware.ErrForbidden) {
			t.Fatalf("%s: err=%v, want a middleware.ErrForbidden refusal", name, err)
		}
		if got := gatewayerrors.LogLevel(err); got != logging.InfoLevel {
			t.Fatalf("%s: log level %s, want info", name, got)
		}
		if err.Error() != "API tokens cannot manage API tokens" {
			t.Fatalf("%s: message %q changed", name, err.Error())
		}
	}
}
