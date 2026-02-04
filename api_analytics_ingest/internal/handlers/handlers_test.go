package handlers

import (
	"encoding/json"
	"reflect"
	"testing"

	"frameworks/pkg/kafka"
	pb "frameworks/pkg/proto"

	"github.com/google/uuid"
)

func TestMax64(t *testing.T) {
	cases := []struct {
		name     string
		first    int64
		second   int64
		expected int64
	}{
		{name: "first larger", first: 10, second: 2, expected: 10},
		{name: "second larger", first: -4, second: 8, expected: 8},
		{name: "equal", first: 5, second: 5, expected: 5},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := max64(tc.first, tc.second); got != tc.expected {
				t.Fatalf("max64(%d, %d) = %d, want %d", tc.first, tc.second, got, tc.expected)
			}
		})
	}
}

func TestNilIfZeroFloat32(t *testing.T) {
	cases := []struct {
		name     string
		input    float32
		expected *float32
	}{
		{name: "zero", input: 0, expected: nil},
		{name: "non-zero", input: 1.25, expected: float32Ptr(1.25)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nilIfZeroFloat32(tc.input)
			assertInterfaceValue(t, got, tc.expected)
		})
	}
}

func TestNilIfZeroBool(t *testing.T) {
	cases := []struct {
		name     string
		input    bool
		expected *bool
	}{
		{name: "false", input: false, expected: nil},
		{name: "true", input: true, expected: boolPtr(true)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nilIfZeroBool(tc.input)
			assertInterfaceValue(t, got, tc.expected)
		})
	}
}

func TestNilIfZeroUint64(t *testing.T) {
	cases := []struct {
		name     string
		input    uint64
		expected *uint64
	}{
		{name: "zero", input: 0, expected: nil},
		{name: "non-zero", input: 42, expected: uint64Ptr(42)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nilIfZeroUint64(tc.input)
			assertInterfaceValue(t, got, tc.expected)
		})
	}
}

func TestNilIfZeroUint32(t *testing.T) {
	cases := []struct {
		name     string
		input    uint32
		expected *uint32
	}{
		{name: "zero", input: 0, expected: nil},
		{name: "non-zero", input: 7, expected: uint32Ptr(7)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nilIfZeroUint32(tc.input)
			assertInterfaceValue(t, got, tc.expected)
		})
	}
}

func TestNilIfEmptyString(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		expected *string
	}{
		{name: "empty", input: "", expected: nil},
		{name: "non-empty", input: "value", expected: stringPtr("value")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nilIfEmptyString(tc.input)
			assertInterfaceValue(t, got, tc.expected)
		})
	}
}

func TestParseUUID(t *testing.T) {
	valid := uuid.New()
	cases := []struct {
		name     string
		input    string
		expected uuid.UUID
	}{
		{name: "empty", input: "", expected: uuid.Nil},
		{name: "invalid", input: "not-a-uuid", expected: uuid.Nil},
		{name: "valid", input: valid.String(), expected: valid},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseUUID(tc.input); got != tc.expected {
				t.Fatalf("parseUUID(%q) = %s, want %s", tc.input, got, tc.expected)
			}
		})
	}
}

func TestIsValidUUIDString(t *testing.T) {
	valid := uuid.New().String()
	cases := []struct {
		name     string
		input    string
		expected bool
	}{
		{name: "empty", input: "", expected: false},
		{name: "invalid", input: "invalid", expected: false},
		{name: "nil uuid", input: uuid.Nil.String(), expected: false},
		{name: "valid", input: valid, expected: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isValidUUIDString(tc.input); got != tc.expected {
				t.Fatalf("isValidUUIDString(%q) = %t, want %t", tc.input, got, tc.expected)
			}
		})
	}
}

func TestBoolToUint8(t *testing.T) {
	cases := []struct {
		name     string
		input    bool
		expected uint8
	}{
		{name: "false", input: false, expected: 0},
		{name: "true", input: true, expected: 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := boolToUint8(tc.input); got != tc.expected {
				t.Fatalf("boolToUint8(%t) = %d, want %d", tc.input, got, tc.expected)
			}
		})
	}
}

func TestNilIfZeroUint8(t *testing.T) {
	cases := []struct {
		name     string
		input    int32
		expected *uint8
	}{
		{name: "zero", input: 0, expected: nil},
		{name: "non-zero", input: 7, expected: uint8Ptr(7)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nilIfZeroUint8(tc.input)
			assertInterfaceValue(t, got, tc.expected)
		})
	}
}

func TestNilIfZeroUint16(t *testing.T) {
	cases := []struct {
		name     string
		input    int32
		expected *uint16
	}{
		{name: "zero", input: 0, expected: nil},
		{name: "non-zero", input: 42, expected: uint16Ptr(42)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nilIfZeroUint16(tc.input)
			assertInterfaceValue(t, got, tc.expected)
		})
	}
}

func TestNilIfZeroInt64(t *testing.T) {
	cases := []struct {
		name     string
		input    int64
		expected *int64
	}{
		{name: "zero", input: 0, expected: nil},
		{name: "non-zero", input: 128, expected: int64Ptr(128)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nilIfZeroInt64(tc.input)
			assertInterfaceValue(t, got, tc.expected)
		})
	}
}

func TestNilIfZeroInt32ToUint32(t *testing.T) {
	cases := []struct {
		name     string
		input    int32
		expected *uint32
	}{
		{name: "zero", input: 0, expected: nil},
		{name: "non-zero", input: 9, expected: uint32Ptr(9)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nilIfZeroInt32ToUint32(tc.input)
			assertInterfaceValue(t, got, tc.expected)
		})
	}
}

func TestNilIfEmptyStringPtr(t *testing.T) {
	empty := ""
	value := "value"
	cases := []struct {
		name     string
		input    *string
		expected *string
	}{
		{name: "nil", input: nil, expected: nil},
		{name: "empty", input: &empty, expected: nil},
		{name: "non-empty", input: &value, expected: stringPtr("value")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nilIfEmptyStringPtr(tc.input)
			assertInterfaceValue(t, got, tc.expected)
		})
	}
}

func TestNilIfZeroInt32Ptr(t *testing.T) {
	zero := int32(0)
	value := int32(11)
	cases := []struct {
		name     string
		input    *int32
		expected *int32
	}{
		{name: "nil", input: nil, expected: nil},
		{name: "zero", input: &zero, expected: nil},
		{name: "non-zero", input: &value, expected: int32Ptr(11)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nilIfZeroInt32Ptr(tc.input)
			assertInterfaceValue(t, got, tc.expected)
		})
	}
}

func TestNilIfZeroInt64Ptr(t *testing.T) {
	zero := int64(0)
	value := int64(12)
	cases := []struct {
		name     string
		input    *int64
		expected *int64
	}{
		{name: "nil", input: nil, expected: nil},
		{name: "zero", input: &zero, expected: nil},
		{name: "non-zero", input: &value, expected: int64Ptr(12)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nilIfZeroInt64Ptr(tc.input)
			assertInterfaceValue(t, got, tc.expected)
		})
	}
}

func TestNilIfZeroUint64Ptr(t *testing.T) {
	zero := uint64(0)
	value := uint64(13)
	cases := []struct {
		name     string
		input    *uint64
		expected *uint64
	}{
		{name: "nil", input: nil, expected: nil},
		{name: "zero", input: &zero, expected: nil},
		{name: "non-zero", input: &value, expected: uint64Ptr(13)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nilIfZeroUint64Ptr(tc.input)
			assertInterfaceValue(t, got, tc.expected)
		})
	}
}

type testStringer struct {
	value string
}

func (t testStringer) String() string {
	return t.value
}

func TestGetStringFromMap(t *testing.T) {
	cases := []struct {
		name     string
		data     map[string]interface{}
		key      string
		expected string
	}{
		{name: "nil map", data: nil, key: "value", expected: ""},
		{name: "missing key", data: map[string]interface{}{}, key: "value", expected: ""},
		{name: "string value", data: map[string]interface{}{"value": "hello"}, key: "value", expected: "hello"},
		{name: "stringer value", data: map[string]interface{}{"value": testStringer{value: "stringer"}}, key: "value", expected: "stringer"},
		{name: "wrong type", data: map[string]interface{}{"value": 123}, key: "value", expected: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := getStringFromMap(tc.data, tc.key); got != tc.expected {
				t.Fatalf("getStringFromMap(%v, %q) = %q, want %q", tc.data, tc.key, got, tc.expected)
			}
		})
	}
}

func TestGetInt64FromMap(t *testing.T) {
	validNumber := json.Number("42")
	invalidNumber := json.Number("invalid")
	cases := []struct {
		name         string
		data         map[string]interface{}
		key          string
		wantValue    int64
		wantHasValue bool
	}{
		{name: "nil map", data: nil, key: "value", wantValue: 0, wantHasValue: false},
		{name: "missing key", data: map[string]interface{}{}, key: "value", wantValue: 0, wantHasValue: false},
		{name: "int64", data: map[string]interface{}{"value": int64(5)}, key: "value", wantValue: 5, wantHasValue: true},
		{name: "int32", data: map[string]interface{}{"value": int32(6)}, key: "value", wantValue: 6, wantHasValue: true},
		{name: "int", data: map[string]interface{}{"value": int(7)}, key: "value", wantValue: 7, wantHasValue: true},
		{name: "float64", data: map[string]interface{}{"value": float64(8)}, key: "value", wantValue: 8, wantHasValue: true},
		{name: "float32", data: map[string]interface{}{"value": float32(9)}, key: "value", wantValue: 9, wantHasValue: true},
		{name: "json number", data: map[string]interface{}{"value": validNumber}, key: "value", wantValue: 42, wantHasValue: true},
		{name: "json number invalid", data: map[string]interface{}{"value": invalidNumber}, key: "value", wantValue: 0, wantHasValue: false},
		{name: "wrong type", data: map[string]interface{}{"value": "nope"}, key: "value", wantValue: 0, wantHasValue: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotValue, gotOk := getInt64FromMap(tc.data, tc.key)
			if gotValue != tc.wantValue || gotOk != tc.wantHasValue {
				t.Fatalf("getInt64FromMap(%v, %q) = (%d, %t), want (%d, %t)", tc.data, tc.key, gotValue, gotOk, tc.wantValue, tc.wantHasValue)
			}
		})
	}
}

func TestGetUint64FromMap(t *testing.T) {
	validNumber := json.Number("64")
	negativeNumber := json.Number("-10")
	invalidNumber := json.Number("invalid")
	cases := []struct {
		name     string
		data     map[string]interface{}
		key      string
		expected uint64
	}{
		{name: "nil map", data: nil, key: "value", expected: 0},
		{name: "missing key", data: map[string]interface{}{}, key: "value", expected: 0},
		{name: "uint64", data: map[string]interface{}{"value": uint64(10)}, key: "value", expected: 10},
		{name: "uint32", data: map[string]interface{}{"value": uint32(11)}, key: "value", expected: 11},
		{name: "int64", data: map[string]interface{}{"value": int64(12)}, key: "value", expected: 12},
		{name: "int64 negative", data: map[string]interface{}{"value": int64(-12)}, key: "value", expected: 0},
		{name: "int", data: map[string]interface{}{"value": int(13)}, key: "value", expected: 13},
		{name: "int negative", data: map[string]interface{}{"value": int(-13)}, key: "value", expected: 0},
		{name: "float64", data: map[string]interface{}{"value": float64(14)}, key: "value", expected: 14},
		{name: "float64 negative", data: map[string]interface{}{"value": float64(-14)}, key: "value", expected: 0},
		{name: "float32", data: map[string]interface{}{"value": float32(15)}, key: "value", expected: 15},
		{name: "float32 negative", data: map[string]interface{}{"value": float32(-15)}, key: "value", expected: 0},
		{name: "json number", data: map[string]interface{}{"value": validNumber}, key: "value", expected: 64},
		{name: "json number negative", data: map[string]interface{}{"value": negativeNumber}, key: "value", expected: 0},
		{name: "json number invalid", data: map[string]interface{}{"value": invalidNumber}, key: "value", expected: 0},
		{name: "wrong type", data: map[string]interface{}{"value": "nope"}, key: "value", expected: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := getUint64FromMap(tc.data, tc.key); got != tc.expected {
				t.Fatalf("getUint64FromMap(%v, %q) = %d, want %d", tc.data, tc.key, got, tc.expected)
			}
		})
	}
}

func TestGetUint64SliceFromMap(t *testing.T) {
	validNumber := json.Number("4")
	negativeNumber := json.Number("-5")
	cases := []struct {
		name     string
		data     map[string]interface{}
		key      string
		expected []uint64
	}{
		{name: "nil map", data: nil, key: "value", expected: nil},
		{name: "missing key", data: map[string]interface{}{}, key: "value", expected: nil},
		{name: "nil value", data: map[string]interface{}{"value": nil}, key: "value", expected: nil},
		{name: "uint64 slice", data: map[string]interface{}{"value": []uint64{1, 2}}, key: "value", expected: []uint64{1, 2}},
		{
			name: "mixed slice",
			data: map[string]interface{}{
				"value": []interface{}{uint64(1), int64(-1), int(2), float64(3), validNumber, negativeNumber, "skip"},
			},
			key:      "value",
			expected: []uint64{1, 2, 3, 4},
		},
		{name: "wrong type", data: map[string]interface{}{"value": "nope"}, key: "value", expected: nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := getUint64SliceFromMap(tc.data, tc.key)
			if !reflect.DeepEqual(got, tc.expected) {
				t.Fatalf("getUint64SliceFromMap(%v, %q) = %#v, want %#v", tc.data, tc.key, got, tc.expected)
			}
		})
	}
}

func TestNormalizeVodStage(t *testing.T) {
	cases := []struct {
		name     string
		status   pb.VodLifecycleData_Status
		expected string
	}{
		{name: "requested", status: pb.VodLifecycleData_STATUS_REQUESTED, expected: "requested"},
		{name: "uploading", status: pb.VodLifecycleData_STATUS_UPLOADING, expected: "uploading"},
		{name: "processing", status: pb.VodLifecycleData_STATUS_PROCESSING, expected: "processing"},
		{name: "completed", status: pb.VodLifecycleData_STATUS_COMPLETED, expected: "completed"},
		{name: "failed", status: pb.VodLifecycleData_STATUS_FAILED, expected: "failed"},
		{name: "deleted", status: pb.VodLifecycleData_STATUS_DELETED, expected: "deleted"},
		{name: "unknown", status: pb.VodLifecycleData_STATUS_UNSPECIFIED, expected: "unknown"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeVodStage(tc.status); got != tc.expected {
				t.Fatalf("normalizeVodStage(%v) = %q, want %q", tc.status, got, tc.expected)
			}
		})
	}
}

func TestNormalizeDVRStage(t *testing.T) {
	cases := []struct {
		name     string
		status   pb.DVRLifecycleData_Status
		expected string
	}{
		{name: "started", status: pb.DVRLifecycleData_STATUS_STARTED, expected: "started"},
		{name: "recording", status: pb.DVRLifecycleData_STATUS_RECORDING, expected: "recording"},
		{name: "stopped", status: pb.DVRLifecycleData_STATUS_STOPPED, expected: "stopped"},
		{name: "failed", status: pb.DVRLifecycleData_STATUS_FAILED, expected: "failed"},
		{name: "deleted", status: pb.DVRLifecycleData_STATUS_DELETED, expected: "deleted"},
		{name: "unknown", status: pb.DVRLifecycleData_STATUS_UNSPECIFIED, expected: "unknown"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeDVRStage(tc.status); got != tc.expected {
				t.Fatalf("normalizeDVRStage(%v) = %q, want %q", tc.status, got, tc.expected)
			}
		})
	}
}

func TestExtractPrimaryTracks(t *testing.T) {
	videoPrimary := &pb.StreamTrack{TrackType: "video", TrackName: "video-primary"}
	videoSecondary := &pb.StreamTrack{TrackType: "video", TrackName: "video-secondary"}
	audioPrimary := &pb.StreamTrack{TrackType: "audio", TrackName: "audio-primary"}

	cases := []struct {
		name      string
		tracks    []*pb.StreamTrack
		wantVideo *pb.StreamTrack
		wantAudio *pb.StreamTrack
	}{
		{name: "empty", tracks: nil, wantVideo: nil, wantAudio: nil},
		{name: "audio only", tracks: []*pb.StreamTrack{audioPrimary}, wantVideo: nil, wantAudio: audioPrimary},
		{name: "video then audio", tracks: []*pb.StreamTrack{videoPrimary, audioPrimary}, wantVideo: videoPrimary, wantAudio: audioPrimary},
		{name: "multiple videos", tracks: []*pb.StreamTrack{videoPrimary, videoSecondary, audioPrimary}, wantVideo: videoPrimary, wantAudio: audioPrimary},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotVideo, gotAudio := extractPrimaryTracks(tc.tracks)
			if gotVideo != tc.wantVideo || gotAudio != tc.wantAudio {
				t.Fatalf("extractPrimaryTracks(%v) = (%v, %v), want (%v, %v)", tc.tracks, gotVideo, gotAudio, tc.wantVideo, tc.wantAudio)
			}
		})
	}
}

func TestAllowlistEventData(t *testing.T) {
	data := map[string]interface{}{
		"conversation_id": "c1",
		"message_id":      "m1",
		"sender":          "user",
		"timestamp":       int64(123),
		"extra":           "skip",
	}
	allowed := []string{"conversation_id", "message_id", "sender", "timestamp"}

	got := allowlistEventData(data, allowed)
	expected := map[string]interface{}{
		"conversation_id": "c1",
		"message_id":      "m1",
		"sender":          "user",
		"timestamp":       int64(123),
	}

	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("allowlistEventData(%v, %v) = %#v, want %#v", data, allowed, got, expected)
	}

	if got := allowlistEventData(nil, allowed); got != nil {
		t.Fatalf("allowlistEventData(nil, %v) = %#v, want nil", allowed, got)
	}
}

func TestSanitizeServiceEventData(t *testing.T) {
	baseData := map[string]interface{}{
		"conversation_id": "c1",
		"message_id":      "m1",
		"sender":          "user",
		"status":          "open",
		"subject":         "hello",
		"timestamp":       int64(123),
		"extra":           "skip",
	}
	cases := []struct {
		name     string
		event    kafka.ServiceEvent
		expected map[string]interface{}
	}{
		{
			name:  "message received allowlist",
			event: kafka.ServiceEvent{EventType: "message_received", Data: baseData},
			expected: map[string]interface{}{
				"conversation_id": "c1",
				"message_id":      "m1",
				"sender":          "user",
				"timestamp":       int64(123),
			},
		},
		{
			name:  "conversation created allowlist",
			event: kafka.ServiceEvent{EventType: "conversation_created", Data: baseData},
			expected: map[string]interface{}{
				"conversation_id": "c1",
				"status":          "open",
				"subject":         "hello",
				"timestamp":       int64(123),
			},
		},
		{
			name:     "default passthrough",
			event:    kafka.ServiceEvent{EventType: "other_event", Data: baseData},
			expected: baseData,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeServiceEventData(tc.event)
			if !reflect.DeepEqual(got, tc.expected) {
				t.Fatalf("sanitizeServiceEventData(%v) = %#v, want %#v", tc.event, got, tc.expected)
			}
		})
	}
}

func assertInterfaceValue[T comparable](t *testing.T, got interface{}, expected *T) {
	t.Helper()
	if expected == nil {
		if got != nil {
			t.Fatalf("expected nil, got %#v", got)
		}
		return
	}
	if got == nil {
		t.Fatalf("expected %#v, got nil", *expected)
	}
	value, ok := got.(T)
	if !ok {
		t.Fatalf("expected type %T, got %T", *expected, got)
	}
	if value != *expected {
		t.Fatalf("expected %#v, got %#v", *expected, value)
	}
}

func float32Ptr(v float32) *float32 {
	return &v
}

func boolPtr(v bool) *bool {
	return &v
}

func uint64Ptr(v uint64) *uint64 {
	return &v
}

func uint32Ptr(v uint32) *uint32 {
	return &v
}

func stringPtr(v string) *string {
	return &v
}

func int32Ptr(v int32) *int32 {
	return &v
}

func int64Ptr(v int64) *int64 {
	return &v
}

func uint8Ptr(v uint8) *uint8 {
	return &v
}

func uint16Ptr(v uint16) *uint16 {
	return &v
}
