package grpc

import (
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// logFoghornCallFailure logs a failed Foghorn artifact RPC at the level its gRPC status class deserves. Foghorn
// answers a request it refuses (a clip with no start, an unknown hash, a precondition the artifact does not meet)
// with a caller-class code, which Commodore propagates to the caller unchanged: that is a decision about the request,
// logged at info. Unavailability that a retry may clear logs at warn. Everything else is a fault in Foghorn or in
// this call and logs at error.
func logFoghornCallFailure(entry *logrus.Entry, err error, msg string) {
	entry = entry.WithError(err).WithField("grpc_code", status.Code(err).String())
	switch status.Code(err) {
	case codes.InvalidArgument, codes.NotFound, codes.AlreadyExists, codes.FailedPrecondition,
		codes.OutOfRange, codes.PermissionDenied, codes.Unauthenticated, codes.Canceled:
		entry.Info(msg)
	case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted, codes.Aborted:
		entry.Warn(msg)
	default:
		entry.Error(msg)
	}
}
