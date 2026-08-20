package grpc

import (
	"context"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
)

func serviceTestContext() context.Context {
	return context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
}
