// GraphQL enum marshaling for common proto types.

package commonpb

import (
	"fmt"
	"io"
	"strconv"
)

// MarshalGQL implements the graphql.Marshaler interface for SortOrder
func (e SortOrder) MarshalGQL(w io.Writer) {
	var s string
	switch e {
	case SortOrder_SORT_ORDER_ASC:
		s = "ASC"
	case SortOrder_SORT_ORDER_DESC:
		s = "DESC"
	default:
		s = "DESC"
	}
	io.WriteString(w, strconv.Quote(s)) //nolint:errcheck // MarshalGQL has no error return
}

// UnmarshalGQL implements the graphql.Unmarshaler interface for SortOrder
func (e *SortOrder) UnmarshalGQL(v any) error {
	str, ok := v.(string)
	if !ok {
		return fmt.Errorf("enums must be strings")
	}
	switch str {
	case "ASC":
		*e = SortOrder_SORT_ORDER_ASC
	case "DESC":
		*e = SortOrder_SORT_ORDER_DESC
	default:
		return fmt.Errorf("%s is not a valid SortOrder", str)
	}
	return nil
}
