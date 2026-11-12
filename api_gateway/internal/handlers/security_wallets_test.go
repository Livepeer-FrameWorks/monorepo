package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/gin-gonic/gin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type securityWalletsClient struct {
	commodore.Interface
	err error
}

func (c *securityWalletsClient) ListWallets(context.Context) (*commodorepb.ListWalletsResponse, error) {
	if c.err != nil {
		return nil, c.err
	}
	return &commodorepb.ListWalletsResponse{Wallets: []*commodorepb.WalletIdentity{{Id: "wallet-1", WalletAddress: "0xowned"}}}, nil
}
func TestSecurityWalletReadPreservesAvailabilityFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name   string
		err    error
		status int
	}{{"live", nil, http.StatusOK}, {"outage", status.Error(codes.Unavailable, "database down"), http.StatusServiceUnavailable}, {"rejected", status.Error(codes.Unauthenticated, "expired"), http.StatusUnauthorized}} {
		t.Run(test.name, func(t *testing.T) {
			h := NewAuthHandlers(&securityWalletsClient{err: test.err}, logging.NewLogger(), AuthConfig{})
			router := gin.New()
			router.GET("/me/wallets", h.SecurityWallets())
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/me/wallets", nil))
			if response.Code != test.status {
				t.Fatalf("wallet read=%d want %d: %s", response.Code, test.status, response.Body)
			}
			if test.err != nil && response.Body.String() == `{"wallets":[]}` {
				t.Fatal("outage looked like no linked wallets")
			}
			if test.err == nil && response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("wallet response can be cached")
			}
		})
	}
}
