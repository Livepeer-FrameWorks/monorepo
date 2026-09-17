package placement

import (
	"fmt"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNodePlacementNotReadyErrorIsStructurallyDistinct(t *testing.T) {
	err := NodePlacementNotReadyError("cells not ready")
	if status.Code(err) != codes.FailedPrecondition || status.Convert(err).Message() != "cells not ready" {
		t.Fatalf("unexpected status: %v", err)
	}
	if !IsNodePlacementNotReady(err) {
		t.Fatal("node placement refusal not recognized")
	}
	if IsNodePlacementNotReady(fmt.Errorf("wrapped: %w", err)) != IsNodePlacementNotReady(err) {
		t.Fatal("wrapped refusal changes recognition")
	}

	otherReason, _ := status.New(codes.FailedPrecondition, "x").WithDetails(&errdetails.ErrorInfo{Reason: "OTHER", Domain: ErrorInfoDomain})
	otherDomain, _ := status.New(codes.FailedPrecondition, "x").WithDetails(&errdetails.ErrorInfo{Reason: ReasonNodePlacementNotReady, Domain: "billing.frameworks.network"})
	otherCode, _ := status.New(codes.InvalidArgument, "x").WithDetails(&errdetails.ErrorInfo{Reason: ReasonNodePlacementNotReady, Domain: ErrorInfoDomain})
	for name, candidate := range map[string]error{
		"nil":          nil,
		"plain":        fmt.Errorf("node placement not ready"),
		"stale review": status.Error(codes.FailedPrecondition, "review expired"),
		"other reason": otherReason.Err(),
		"other domain": otherDomain.Err(),
		"other code":   otherCode.Err(),
	} {
		if IsNodePlacementNotReady(candidate) {
			t.Fatalf("%s recognized as node placement refusal", name)
		}
	}
}
