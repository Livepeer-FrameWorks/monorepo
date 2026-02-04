package grpcutil

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var grpcCodeMessages = map[codes.Code]string{
	codes.InvalidArgument:    "invalid request",
	codes.NotFound:           "resource not found",
	codes.PermissionDenied:   "permission denied",
	codes.Unauthenticated:    "authentication required",
	codes.Unavailable:        "service temporarily unavailable",
	codes.DeadlineExceeded:   "request timed out",
	codes.AlreadyExists:      "resource already exists",
	codes.FailedPrecondition: "precondition failed",
	codes.ResourceExhausted:  "resource exhausted",
	codes.Aborted:            "request aborted",
	codes.OutOfRange:         "out of range",
	codes.Internal:           "internal error",
}

func SanitizeError(err error) error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		return status.Error(codes.Internal, grpcCodeMessages[codes.Internal])
	}
	return status.Error(st.Code(), messageForCode(st.Code()))
}

func SanitizeUnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		resp, err := handler(ctx, req)
		return resp, SanitizeError(err)
	}
}

func messageForCode(code codes.Code) string {
	if message, ok := grpcCodeMessages[code]; ok {
		return message
	}
	return grpcCodeMessages[codes.Internal]
}
