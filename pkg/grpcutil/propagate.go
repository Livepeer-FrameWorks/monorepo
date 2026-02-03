package grpcutil

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// PropagateError forwards downstream gRPC status and trailers to callers.
// Trailer propagation is best-effort; if SetTrailer fails, the primary error is still returned.
func PropagateError(ctx context.Context, err error, trailers metadata.MD) error {
	if len(trailers) > 0 {
		// SetTrailer can fail if ctx is not a server context, but trailer propagation
		// is best-effort - we prioritize returning the correct error status.
		//nolint:errcheck // trailer propagation is best-effort
		grpc.SetTrailer(ctx, trailers)
	}
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		return status.Errorf(codes.Internal, "downstream error: %v", err)
	}
	return st.Err()
}
