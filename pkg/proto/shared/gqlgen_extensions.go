// GraphQL union interface markers for shared proto types.

package sharedpb

import (
	"fmt"
	"io"
	"strconv"
)

// ClipInfo implements union interfaces (GraphQL type: Clip)
func (*ClipInfo) IsCreateClipResult()        {}
func (*ClipInfo) IsSetPlaybackPolicyResult() {}

// DVRInfo implements union interfaces (GraphQL type: DVRRequest)
func (*DVRInfo) IsStartDVRResult() {}

// MarshalGQL implements GraphQL enum serialization for ingest endpoint kinds.
func (e IngestEndpointKind) MarshalGQL(w io.Writer) {
	name, ok := IngestEndpointKind_name[int32(e)]
	if !ok {
		name = IngestEndpointKind_INGEST_ENDPOINT_KIND_UNSPECIFIED.String()
	}
	io.WriteString(w, strconv.Quote(name)) //nolint:errcheck // MarshalGQL has no error return
}

// UnmarshalGQL implements GraphQL enum parsing for ingest endpoint kinds.
func (e *IngestEndpointKind) UnmarshalGQL(v any) error {
	name, ok := v.(string)
	if !ok {
		return fmt.Errorf("ingest endpoint kind must be a string")
	}
	value, ok := IngestEndpointKind_value[name]
	if !ok {
		return fmt.Errorf("%q is not a valid IngestEndpointKind", name)
	}
	*e = IngestEndpointKind(value)
	return nil
}
