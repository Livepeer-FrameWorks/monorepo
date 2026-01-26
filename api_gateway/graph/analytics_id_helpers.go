package graph

import (
	"strconv"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"
)

func encodeProtoTimestampPart(ts *timestamppb.Timestamp) string {
	if ts == nil {
		return "0"
	}
	return strconv.FormatInt(ts.AsTime().UnixNano(), 10)
}

func encodeTimePart(t time.Time) string {
	if t.IsZero() {
		return "0"
	}
	return strconv.FormatInt(t.UnixNano(), 10)
}
