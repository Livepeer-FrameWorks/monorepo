package clients

import (
	"context"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"google.golang.org/grpc"
)

// MediaRequestObserverInterceptor records one logical client invocation when
// the caller tagged the context as a media request path. It intentionally runs
// outside retry interceptors, so retries do not multiply the dependency count.
// Management/background traffic remains outside this metric.
func MediaRequestObserverInterceptor(service string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		ctxkeys.ObserveMediaRequestRPC(ctx, service, method)
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}
