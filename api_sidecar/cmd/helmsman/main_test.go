package main

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/gin-gonic/gin"
)

func TestRegisterMistAdminRoutesDoesNotConflictWithProxyCatchAll(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("registerMistAdminRoutes panicked: %v", recovered)
		}
	}()
	registerMistAdminRoutes(r, "http://127.0.0.1:4242", logging.NewLogger())
}

func TestHelmsmanManagementBindRequiresLoopback(t *testing.T) {
	for _, addr := range []string{"127.0.0.1", "::1", "[::1]", "localhost"} {
		if !isLoopbackBind(addr) {
			t.Errorf("isLoopbackBind(%q) = false, want true", addr)
		}
	}
	for _, addr := range []string{"", "0.0.0.0", "::", "10.0.0.2", "helmsman"} {
		if isLoopbackBind(addr) {
			t.Errorf("isLoopbackBind(%q) = true, want false", addr)
		}
	}
}
