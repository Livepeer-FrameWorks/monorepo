package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SecurityWallets reads sign-in methods independently of the cached profile;
// failure is visible rather than appearing as an account with no linked wallets.
func (h *AuthHandlers) SecurityWallets() gin.HandlerFunc {
	return func(c *gin.Context) {
		response, err := h.commodore.ListWallets(c.Request.Context())
		if err != nil {
			code := http.StatusServiceUnavailable
			if status.Code(err) == codes.Unauthenticated {
				code = http.StatusUnauthorized
			}
			if status.Code(err) == codes.PermissionDenied {
				code = http.StatusForbidden
			}
			c.JSON(code, gin.H{"error": "could not load sign-in methods"})
			return
		}
		wallets := make([]gin.H, 0, len(response.GetWallets()))
		for _, wallet := range response.GetWallets() {
			var createdAt, lastAuthAt any
			if wallet.GetCreatedAt() != nil {
				createdAt = wallet.GetCreatedAt().AsTime()
			}
			if wallet.GetLastAuthAt() != nil {
				lastAuthAt = wallet.GetLastAuthAt().AsTime()
			}
			wallets = append(wallets, gin.H{"id": wallet.GetId(), "address": wallet.GetWalletAddress(), "createdAt": createdAt, "lastAuthAt": lastAuthAt})
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{"wallets": wallets})
	}
}
