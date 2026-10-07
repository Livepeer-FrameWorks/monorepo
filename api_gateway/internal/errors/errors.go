package errors

import (
	"context"
	"errors"
	"strings"

	"frameworks/api_gateway/internal/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/errcode"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const defaultPublicMessage = "request failed"

var grpcCodeMessages = map[codes.Code]string{
	codes.InvalidArgument:    "invalid request",
	codes.NotFound:           "resource not found",
	codes.PermissionDenied:   "permission denied",
	codes.Unauthenticated:    "authentication required",
	codes.Unavailable:        "service temporarily unavailable",
	codes.DeadlineExceeded:   "request timed out",
	codes.AlreadyExists:      "resource already exists",
	codes.FailedPrecondition: "request not allowed in the current state",
	codes.ResourceExhausted:  "rate limit exceeded",
	codes.Internal:           "internal error",
}

// clientFault reports errors caused by the request rather than by the
// platform: an invalid document, a resolver's own refusal (missing identity,
// authorization, invalid arguments; see middleware.Forbidden,
// middleware.Unauthenticated and middleware.InvalidInput), or a gRPC status
// that describes the caller's input or state.
func clientFault(err error) bool {
	var gqlErr *gqlerror.Error
	if errors.As(err, &gqlErr) {
		if code, ok := gqlErr.Extensions["code"].(string); ok && (code == errcode.ValidationFailed || code == errcode.ParseFailed) {
			return true
		}
	}
	if errors.Is(err, auth.ErrUnauthenticated) || errors.Is(err, middleware.ErrForbidden) || errors.Is(err, middleware.ErrInvalidInput) ||
		errors.Is(err, middleware.ErrNotFound) {
		return true
	}
	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case codes.InvalidArgument, codes.OutOfRange, codes.NotFound, codes.AlreadyExists, codes.Aborted,
			codes.PermissionDenied, codes.Unauthenticated, codes.FailedPrecondition, codes.ResourceExhausted:
			return true
		}
	}
	return false
}

// LogLevel is the level Bridge logs err at: info for a client fault, error
// for a platform fault. The presenter logs every error a resolver returns;
// a resolver logs an error itself only when it handles the error instead of
// returning it, and then at this level.
func LogLevel(err error) logging.Level {
	if clientFault(err) {
		return logging.InfoLevel
	}
	return logging.ErrorLevel
}

func ErrorPresenter(logger logging.Logger) graphql.ErrorPresenterFunc {
	return func(ctx context.Context, err error) *gqlerror.Error {
		if err != nil {
			if clientFault(err) {
				logger.WithError(err).Info("GraphQL request rejected")
			} else {
				logger.WithError(err).Error("GraphQL request failed")
			}
		}
		presented := graphql.DefaultErrorPresenter(ctx, err)
		if message, extensions, ok := publicGRPCError(err); ok {
			presented.Message = message
			if presented.Extensions == nil {
				presented.Extensions = map[string]any{}
			}
			for key, value := range extensions {
				presented.Extensions[key] = value
			}
			return presented
		}
		presented.Message = SanitizeErrorMessage(err, presented.Message)
		localCode := ""
		switch {
		case errors.Is(err, auth.ErrUnauthenticated):
			localCode = PublicCode(codes.Unauthenticated)
		case errors.Is(err, middleware.ErrForbidden):
			localCode = PublicCode(codes.PermissionDenied)
		case errors.Is(err, middleware.ErrInvalidInput):
			localCode = PublicCode(codes.InvalidArgument)
		case errors.Is(err, middleware.ErrNotFound):
			localCode = PublicCode(codes.NotFound)
		}
		if localCode != "" {
			if presented.Extensions == nil {
				presented.Extensions = map[string]any{}
			}
			if _, hasCode := presented.Extensions["code"]; !hasCode {
				presented.Extensions["code"] = localCode
			}
		}
		if st, ok := status.FromError(err); ok && err != nil {
			if presented.Extensions == nil {
				presented.Extensions = map[string]any{}
			}
			if _, hasCode := presented.Extensions["code"]; !hasCode {
				presented.Extensions["code"] = PublicCode(st.Code())
			}
		}
		return presented
	}
}

// PublicCode is the extensions.code a gRPC status code is presented as. The
// set is part of the public API contract documented in api-reference.mdx.
func PublicCode(code codes.Code) string {
	switch code {
	case codes.Unauthenticated:
		return "UNAUTHORIZED"
	case codes.PermissionDenied:
		return "FORBIDDEN"
	case codes.NotFound:
		return "NOT_FOUND"
	case codes.InvalidArgument, codes.OutOfRange:
		return "VALIDATION_ERROR"
	case codes.AlreadyExists, codes.Aborted:
		return "CONFLICT"
	case codes.FailedPrecondition:
		return "FAILED_PRECONDITION"
	case codes.ResourceExhausted:
		return "RATE_LIMITED"
	case codes.Unavailable, codes.DeadlineExceeded:
		return "UNAVAILABLE"
	default:
		return "INTERNAL_ERROR"
	}
}

func publicGRPCError(err error) (string, map[string]any, bool) {
	st, ok := status.FromError(err)
	if !ok {
		return "", nil, false
	}
	if reason, retryAfter, isStreamState := grpcutil.PlaybackStreamState(err); isStreamState {
		switch {
		case reason == grpcutil.StreamStartingReason && st.Code() == codes.Unavailable:
			extensions := map[string]any{"code": reason}
			if retryAfter > 0 {
				extensions["retry_after_ms"] = retryAfter.Milliseconds()
			}
			return "stream is starting", extensions, true
		case reason == grpcutil.StreamOfflineReason && st.Code() == codes.FailedPrecondition:
			return "stream is offline", map[string]any{"code": reason}, true
		}
	}
	for _, detail := range st.Details() {
		info, isErrorInfo := detail.(*errdetails.ErrorInfo)
		if !isErrorInfo || info.GetDomain() != "billing.frameworks.network" {
			continue
		}
		switch info.GetReason() {
		case "BILLING_PROFILE_REQUIRED":
			fields := splitMetadataList(info.GetMetadata()["required_fields"])
			message := strings.TrimSpace(info.GetMetadata()["public_message"])
			if message == "" {
				message = "Complete the listed billing fields before requesting this payment."
			}
			return message, map[string]any{
				"code":            info.GetReason(),
				"required_fields": fields,
			}, true
		}
	}
	return "", nil, false
}

func splitMetadataList(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if field := strings.TrimSpace(part); field != "" {
			result = append(result, field)
		}
	}
	return result
}

func SanitizeErrorMessage(err error, fallback string) string {
	if err == nil {
		return fallback
	}
	if st, ok := status.FromError(err); ok {
		return messageForCode(st.Code())
	}
	message := err.Error()
	if strings.HasPrefix(message, "rpc error:") {
		return fallbackMessage(fallback)
	}
	return message
}

func SanitizeGRPCError(err error, fallback string, allowed []string) string {
	if err == nil {
		return fallbackMessage(fallback)
	}
	if st, ok := status.FromError(err); ok {
		return SanitizeMessage(st.Message(), fallback, allowed)
	}
	return fallbackMessage(fallback)
}

func SanitizeMessage(message, fallback string, allowed []string) string {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return fallbackMessage(fallback)
	}
	lowered := strings.ToLower(trimmed)
	for _, allow := range allowed {
		if allow == "" {
			continue
		}
		if strings.Contains(lowered, strings.ToLower(allow)) {
			return trimmed
		}
	}
	return fallbackMessage(fallback)
}

func messageForCode(code codes.Code) string {
	if message, ok := grpcCodeMessages[code]; ok {
		return message
	}
	return grpcCodeMessages[codes.Internal]
}

func fallbackMessage(fallback string) string {
	if fallback == "" {
		return defaultPublicMessage
	}
	return fallback
}
