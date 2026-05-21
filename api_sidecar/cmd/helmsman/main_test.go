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
