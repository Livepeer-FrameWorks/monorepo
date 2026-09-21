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

func TestListenHostStripsIPv6Brackets(t *testing.T) {
	cases := map[string]string{
		"":            "",
		"127.0.0.1":   "127.0.0.1",
		"[::1]":       "::1",
		"::1":         "::1",
		" localhost ": "localhost",
	}
	for in, want := range cases {
		if got := listenHost(in); got != want {
			t.Errorf("listenHost(%q) = %q, want %q", in, got, want)
		}
	}
}
