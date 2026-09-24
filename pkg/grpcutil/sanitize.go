package grpcutil

import (
	"context"
	"errors"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
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

var preserveMessageCodes = map[codes.Code]struct{}{
	codes.InvalidArgument:    {},
	codes.NotFound:           {},
	codes.AlreadyExists:      {},
	codes.FailedPrecondition: {},
	codes.ResourceExhausted:  {},
	codes.Aborted:            {},
	codes.OutOfRange:         {},
}

const (
	botCheckErrorDomain    = "auth.frameworks.network"
	botCheckErrorReason    = "BOT_CHECK_FAILED"
	emailNotVerifiedReason = "EMAIL_NOT_VERIFIED"
)

func BotCheckFailedError() error {
	st := status.New(codes.PermissionDenied, "bot verification failed")
	withDetails, err := st.WithDetails(&errdetails.ErrorInfo{Domain: botCheckErrorDomain, Reason: botCheckErrorReason})
	if err != nil {
		return st.Err()
	}
	return withDetails.Err()
}

func IsBotCheckFailed(err error) bool {
	return hasAuthReason(err, codes.PermissionDenied, botCheckErrorReason)
}

func EmailNotVerifiedError() error {
	st := status.New(codes.Unauthenticated, "email not verified")
	withDetails, err := st.WithDetails(&errdetails.ErrorInfo{Domain: botCheckErrorDomain, Reason: emailNotVerifiedReason})
	if err != nil {
		return st.Err()
	}
	return withDetails.Err()
}

func IsEmailNotVerified(err error) bool {
	return hasAuthReason(err, codes.Unauthenticated, emailNotVerifiedReason)
}

func hasAuthReason(err error, code codes.Code, reason string) bool {
	st, ok := status.FromError(err)
	if !ok || st.Code() != code {
		return false
	}
	for _, detail := range st.Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok && info.GetDomain() == botCheckErrorDomain && info.GetReason() == reason {
			return true
		}
	}
	return false
}

func SanitizeError(err error) error {
	if err == nil {
		return nil
	}
	st, ok := statusFromError(err)
	if !ok {
		return status.Error(codes.Internal, grpcCodeMessages[codes.Internal])
	}
	if shouldPreserveMessage(st.Code()) {
		return st.Err()
	}
	if IsBotCheckFailed(st.Err()) {
		withDetails, err := status.New(codes.PermissionDenied, messageForCode(codes.PermissionDenied)).WithDetails(&errdetails.ErrorInfo{Domain: botCheckErrorDomain, Reason: botCheckErrorReason})
		if err == nil {
			return withDetails.Err()
		}
	}
	if IsEmailNotVerified(st.Err()) {
		withDetails, err := status.New(codes.Unauthenticated, messageForCode(codes.Unauthenticated)).WithDetails(&errdetails.ErrorInfo{Domain: botCheckErrorDomain, Reason: emailNotVerifiedReason})
		if err == nil {
			return withDetails.Err()
		}
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

func shouldPreserveMessage(code codes.Code) bool {
	_, ok := preserveMessageCodes[code]
	return ok
}

type grpcStatusError interface {
	GRPCStatus() *status.Status
}

func statusFromError(err error) (*status.Status, bool) {
	st, ok := status.FromError(err)
	if !ok {
		return nil, false
	}
	var grpcErr grpcStatusError
	if errors.As(err, &grpcErr) && grpcErr.GRPCStatus() != nil {
		return grpcErr.GRPCStatus(), true
	}
	return st, true
}
