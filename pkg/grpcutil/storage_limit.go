package grpcutil

import (
	"strconv"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const StorageErrorDomain = "storage.frameworks.network"
const StorageLimitReachedReason = "STORAGE_LIMIT_REACHED"

// StorageLimitError carries only tenant-visible quota quantities through service forwarding.
func StorageLimitError(used, additional, limit int64) error {
	st := status.New(codes.ResourceExhausted, "storage limit reached")
	detailed, err := st.WithDetails(&errdetails.ErrorInfo{
		Domain: StorageErrorDomain, Reason: StorageLimitReachedReason,
		Metadata: map[string]string{"used_bytes": strconv.FormatInt(used, 10), "additional_bytes": strconv.FormatInt(additional, 10), "limit_bytes": strconv.FormatInt(limit, 10)},
	})
	if err != nil {
		return st.Err()
	}
	return detailed.Err()
}

// StorageLimitState accepts only the storage quota contract, never arbitrary backend text.
func StorageLimitState(err error) (used, additional, limit int64, ok bool) {
	st, valid := statusFromError(err)
	if !valid || st.Code() != codes.ResourceExhausted {
		return 0, 0, 0, false
	}
	for _, detail := range st.Details() {
		info, isInfo := detail.(*errdetails.ErrorInfo)
		if !isInfo || info.GetDomain() != StorageErrorDomain || info.GetReason() != StorageLimitReachedReason {
			continue
		}
		u, ue := strconv.ParseInt(info.GetMetadata()["used_bytes"], 10, 64)
		a, ae := strconv.ParseInt(info.GetMetadata()["additional_bytes"], 10, 64)
		l, le := strconv.ParseInt(info.GetMetadata()["limit_bytes"], 10, 64)
		if ue == nil && ae == nil && le == nil && u >= 0 && a >= 0 && l > 0 {
			return u, a, l, true
		}
	}
	return 0, 0, 0, false
}
