// GraphQL enum marshaling for periscope proto types.

package periscopepb

import (
	"fmt"
	"io"
	"strconv"
)

// MarshalGQL implements the graphql.Marshaler interface for StreamSummarySortField
func (e StreamSummarySortField) MarshalGQL(w io.Writer) {
	var s string
	switch e {
	case StreamSummarySortField_STREAM_SUMMARY_SORT_FIELD_EGRESS_GB:
		s = "EGRESS_GB"
	case StreamSummarySortField_STREAM_SUMMARY_SORT_FIELD_UNIQUE_VIEWERS:
		s = "UNIQUE_VIEWERS"
	case StreamSummarySortField_STREAM_SUMMARY_SORT_FIELD_TOTAL_VIEWS:
		s = "TOTAL_VIEWS"
	case StreamSummarySortField_STREAM_SUMMARY_SORT_FIELD_VIEWER_HOURS:
		s = "VIEWER_HOURS"
	default:
		s = "EGRESS_GB"
	}
	io.WriteString(w, strconv.Quote(s)) //nolint:errcheck // MarshalGQL has no error return
}

// UnmarshalGQL implements the graphql.Unmarshaler interface for StreamSummarySortField
func (e *StreamSummarySortField) UnmarshalGQL(v any) error {
	str, ok := v.(string)
	if !ok {
		return fmt.Errorf("enums must be strings")
	}
	switch str {
	case "EGRESS_GB":
		*e = StreamSummarySortField_STREAM_SUMMARY_SORT_FIELD_EGRESS_GB
	case "UNIQUE_VIEWERS":
		*e = StreamSummarySortField_STREAM_SUMMARY_SORT_FIELD_UNIQUE_VIEWERS
	case "TOTAL_VIEWS":
		*e = StreamSummarySortField_STREAM_SUMMARY_SORT_FIELD_TOTAL_VIEWS
	case "VIEWER_HOURS":
		*e = StreamSummarySortField_STREAM_SUMMARY_SORT_FIELD_VIEWER_HOURS
	default:
		return fmt.Errorf("%s is not a valid StreamSummarySortField", str)
	}
	return nil
}
