package grpc

import (
	"context"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetSignedPolicyBundle preserves the shipped RPC surface while retiring the
// HMAC policy-bundle prototype. Media nodes consume the Ed25519 media-authority
// envelope instead; this method must not perform legacy database or control-
// plane work if an old caller still invokes it.
func (s *CommodoreServer) GetSignedPolicyBundle(context.Context, *commodorepb.GetSignedPolicyBundleRequest) (*commodorepb.GetSignedPolicyBundleResponse, error) {
	return nil, status.Error(codes.Unimplemented, "signed policy bundles were replaced by media authority")
}
