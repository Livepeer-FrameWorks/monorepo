package frameworks

import (
	"encoding/json"
	"fmt"
)

// UnknownMember is a union member this SDK does not know: a type a newer
// server added to the union. Every generated union type accepts it, so a
// response carrying one decodes instead of failing. Typename is the member's
// __typename and Raw its JSON exactly as the server sent it; encoding an
// UnknownMember yields Raw.
type UnknownMember struct {
	Typename string
	Raw      json.RawMessage
}

// newUnknownMember is what the generated union decoders store for a
// __typename they were not generated with. raw is copied because a decoder
// must not keep the buffer it was handed.
func newUnknownMember(typename string, raw []byte) *UnknownMember {
	return &UnknownMember{Typename: typename, Raw: append(json.RawMessage(nil), raw...)}
}

// GetTypename returns the member's __typename.
func (m *UnknownMember) GetTypename() *string { return &m.Typename }

// MarshalJSON returns the member as the server sent it.
func (m *UnknownMember) MarshalJSON() ([]byte, error) {
	if m.Raw == nil {
		return []byte("null"), nil
	}
	return m.Raw, nil
}

// ExpectResult returns a result union member as T when it is one, and a
// *ResultError for any other member (ValidationError, NotFoundError,
// AuthError, RateLimitError, or an *UnknownMember, whose ResultError carries
// its __typename, message, and code):
//
//	stream, err := frameworks.ExpectResult[*frameworks.CreateStreamCreateStream](resp.CreateStream)
func ExpectResult[T any](v any) (T, error) {
	var zero T
	if v == nil {
		return zero, &ProtocolError{ErrorInfo{Message: "result is missing"}}
	}
	if t, ok := v.(T); ok {
		return t, nil
	}
	return zero, newResultError(v)
}

func newResultError(v any) *ResultError {
	fields := map[string]any{}
	if raw, err := json.Marshal(v); err == nil {
		if err := json.Unmarshal(raw, &fields); err != nil {
			fields = map[string]any{}
		}
	}
	str := func(key string) string {
		if s, ok := fields[key].(string); ok {
			return s
		}
		return ""
	}
	typename := str("__typename")
	if typename == "" {
		typename = "unknown"
	}
	message := str("message")
	if message == "" {
		message = fmt.Sprintf("unexpected result %s", typename)
	}
	e := &ResultError{
		ErrorInfo:    ErrorInfo{Message: message, Code: str("code")},
		Typename:     typename,
		Field:        str("field"),
		Constraint:   str("constraint"),
		ResourceType: str("resourceType"),
		ResourceID:   str("resourceId"),
		Result:       fields,
	}
	if n, ok := fields["retryAfter"].(float64); ok {
		e.RetryAfterSeconds = intPtr(int(n))
	}
	return e
}
