package errors

import (
	"context"
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/99designs/gqlgen/graphql"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const defaultPublicMessage = "request failed"

var grpcCodeMessages = map[codes.Code]string{
	codes.InvalidArgument:  "invalid request",
	codes.NotFound:         "resource not found",
	codes.PermissionDenied: "permission denied",
	codes.Unauthenticated:  "authentication required",
	codes.Unavailable:      "service temporarily unavailable",
	codes.DeadlineExceeded: "request timed out",
	codes.AlreadyExists:    "resource already exists",
	codes.Internal:         "internal error",
}

func ErrorPresenter(logger logging.Logger) graphql.ErrorPresenterFunc {
	return func(ctx context.Context, err error) *gqlerror.Error {
		if err != nil {
			logger.WithError(err).Error("GraphQL request failed")
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
		return presented
	}
}

func publicGRPCError(err error) (string, map[string]any, bool) {
	st, ok := status.FromError(err)
	if !ok {
		return "", nil, false
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
