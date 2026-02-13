package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"frameworks/pkg/kafka"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/ClickHouse/clickhouse-go/v2/lib/column"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protojson"
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

func TestParseUUIDOrNil(t *testing.T) {
	valid := uuid.New()
	cases := []struct {
		name     string
		input    string
		expected *uuid.UUID
	}{
		{name: "empty", input: "", expected: nil},
		{name: "invalid", input: "nope", expected: nil},
		{name: "nil uuid", input: uuid.Nil.String(), expected: nil},
		{name: "valid", input: valid.String(), expected: uuidPtr(valid)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseUUIDOrNil(tc.input)
			assertInterfaceValue(t, got, tc.expected)
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

func TestValueOrNilUint64Ptr(t *testing.T) {
	zero := uint64(0)
	value := uint64(21)
	cases := []struct {
		name     string
		input    *uint64
		expected *uint64
	}{
		{name: "nil", input: nil, expected: nil},
		{name: "zero", input: &zero, expected: uint64Ptr(0)},
		{name: "non-zero", input: &value, expected: uint64Ptr(21)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := valueOrNilUint64Ptr(tc.input)
			assertInterfaceValue(t, got, tc.expected)
		})
	}
}

func TestNilIfZeroFloat64Ptr(t *testing.T) {
	zero := float64(0)
	value := float64(1.75)
	cases := []struct {
		name     string
		input    *float64
		expected *float64
	}{
		{name: "nil", input: nil, expected: nil},
		{name: "zero", input: &zero, expected: nil},
		{name: "non-zero", input: &value, expected: float64Ptr(1.75)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nilIfZeroFloat64Ptr(tc.input)
			assertInterfaceValue(t, got, tc.expected)
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

func TestGetMap(t *testing.T) {
	nested := map[string]interface{}{"stream_id": "stream-1", "count": float64(2)}
	cases := []struct {
		name     string
		data     map[string]interface{}
		key      string
		expected map[string]interface{}
	}{
		{name: "nil map", data: nil, key: "value", expected: nil},
		{name: "missing key", data: map[string]interface{}{}, key: "value", expected: nil},
		{name: "wrong type", data: map[string]interface{}{"value": "not-a-map"}, key: "value", expected: nil},
		{name: "valid nested map", data: map[string]interface{}{"value": nested}, key: "value", expected: nested},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := getMap(tc.data, tc.key)
			if !reflect.DeepEqual(got, tc.expected) {
				t.Fatalf("getMap(%v, %q) = %#v, want %#v", tc.data, tc.key, got, tc.expected)
			}
		})
	}
}

func TestGetString(t *testing.T) {
	cases := []struct {
		name     string
		data     map[string]interface{}
		key      string
		expected string
	}{
		{name: "nil map", data: nil, key: "value", expected: ""},
		{name: "missing key", data: map[string]interface{}{}, key: "value", expected: ""},
		{name: "wrong type", data: map[string]interface{}{"value": 123}, key: "value", expected: ""},
		{name: "valid string", data: map[string]interface{}{"value": "hello"}, key: "value", expected: "hello"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := getString(tc.data, tc.key); got != tc.expected {
				t.Fatalf("getString(%v, %q) = %q, want %q", tc.data, tc.key, got, tc.expected)
			}
		})
	}
}

func TestGetBool(t *testing.T) {
	cases := []struct {
		name     string
		data     map[string]interface{}
		key      string
		expected bool
	}{
		{name: "nil map", data: nil, key: "value", expected: false},
		{name: "missing key", data: map[string]interface{}{}, key: "value", expected: false},
		{name: "wrong type", data: map[string]interface{}{"value": "true"}, key: "value", expected: false},
		{name: "false value", data: map[string]interface{}{"value": false}, key: "value", expected: false},
		{name: "true value", data: map[string]interface{}{"value": true}, key: "value", expected: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := getBool(tc.data, tc.key); got != tc.expected {
				t.Fatalf("getBool(%v, %q) = %t, want %t", tc.data, tc.key, got, tc.expected)
			}
		})
	}
}

func TestBoolToUInt8(t *testing.T) {
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
			if got := boolToUInt8(tc.input); got != tc.expected {
				t.Fatalf("boolToUInt8(%t) = %d, want %d", tc.input, got, tc.expected)
			}
		})
	}
}

func TestBoolToNullableUInt8(t *testing.T) {
	trueValue := true
	falseValue := false
	cases := []struct {
		name     string
		input    *bool
		expected interface{}
	}{
		{name: "nil", input: nil, expected: nil},
		{name: "false", input: &falseValue, expected: uint8(0)},
		{name: "true", input: &trueValue, expected: uint8(1)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := boolToNullableUInt8(tc.input); !reflect.DeepEqual(got, tc.expected) {
				t.Fatalf("boolToNullableUInt8(%v) = %#v, want %#v", tc.input, got, tc.expected)
			}
		})
	}
}

func TestParseProtobufData(t *testing.T) {
	handler := &AnalyticsHandler{}

	t.Run("valid viewer connect data", func(t *testing.T) {
		event := kafka.AnalyticsEvent{
			Data: map[string]interface{}{
				"streamName": "stream-1",
				"host":       "1.2.3.4",
			},
		}
		var payload pb.ViewerConnectTrigger
		if err := handler.parseProtobufData(event, &payload); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if payload.GetStreamName() != "stream-1" {
			t.Fatalf("expected stream name stream-1, got %q", payload.GetStreamName())
		}
		if payload.GetHost() != "1.2.3.4" {
			t.Fatalf("expected host 1.2.3.4, got %q", payload.GetHost())
		}
	})

	t.Run("invalid json payload", func(t *testing.T) {
		event := kafka.AnalyticsEvent{
			Data: map[string]interface{}{
				"streamName": map[string]interface{}{"nested": true},
			},
		}
		var payload pb.ViewerConnectTrigger
		if err := handler.parseProtobufData(event, &payload); err == nil {
			t.Fatal("expected error for invalid payload")
		}
	})

	t.Run("unknown fields are ignored for forward compatibility", func(t *testing.T) {
		event := kafka.AnalyticsEvent{
			Data: map[string]interface{}{
				"streamName":      "stream-1",
				"host":            "1.2.3.4",
				"futureOnlyField": "newer-producer",
			},
		}
		var payload pb.ViewerConnectTrigger
		if err := handler.parseProtobufData(event, &payload); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if payload.GetStreamName() != "stream-1" {
			t.Fatalf("expected stream name stream-1, got %q", payload.GetStreamName())
		}
	})

	t.Run("marshal error", func(t *testing.T) {
		event := kafka.AnalyticsEvent{
			Data: map[string]interface{}{
				"bad": func() {},
			},
		}
		var payload pb.ViewerConnectTrigger
		if err := handler.parseProtobufData(event, &payload); err == nil {
			t.Fatal("expected error for marshal failure")
		}
	})
}

func TestHandleAnalyticsEventMissingTenantIDWritesIngestError(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	event := kafka.AnalyticsEvent{
		EventID:   uuid.NewString(),
		EventType: "viewer_connect",
		Timestamp: time.Now(),
		Source:    "decklog",
		TenantID:  "",
		Data:      map[string]interface{}{"ignored": true},
	}

	err := handler.HandleAnalyticsEvent(event)
	if !errors.Is(err, errMissingTenantID) {
		t.Fatalf("expected errMissingTenantID, got %v", err)
	}

	batch := conn.batches["ingest_errors"]
	if batch == nil || len(batch.rows) != 1 {
		t.Fatalf("expected 1 ingest_errors row, got %#v", batch)
	}
	row := batch.rows[0]
	if row[2] != event.EventType {
		t.Fatalf("expected event_type %q, got %#v", event.EventType, row[2])
	}
	if row[4] != event.TenantID {
		t.Fatalf("expected tenant_id %q, got %#v", event.TenantID, row[4])
	}
	if reason, ok := row[6].(string); !ok || !strings.Contains(reason, "missing_or_invalid_tenant_id") {
		t.Fatalf("expected missing tenant reason, got %#v", row[6])
	}
}

func TestHandleServiceEventMissingTenantIDWritesIngestError(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	event := kafka.ServiceEvent{
		EventID:   uuid.NewString(),
		EventType: "tenant_created",
		Timestamp: time.Now(),
		Source:    "api_gateway",
		TenantID:  "",
		Data:      map[string]interface{}{"tenant_name": "acme"},
	}

	err := handler.HandleServiceEvent(event)
	if err != nil {
		t.Fatalf("expected nil error for dropped service event, got %v", err)
	}

	ingestErrors := conn.batches["ingest_errors"]
	if ingestErrors == nil || len(ingestErrors.rows) != 1 {
		t.Fatalf("expected 1 ingest_errors row, got %#v", ingestErrors)
	}
	if reason, ok := ingestErrors.rows[0][6].(string); !ok || !strings.Contains(reason, "missing_or_invalid_tenant_id_service_event") {
		t.Fatalf("expected service-event missing tenant reason, got %#v", ingestErrors.rows[0][6])
	}

	if apiEvents := conn.batches["api_events"]; apiEvents != nil && len(apiEvents.rows) > 0 {
		t.Fatalf("expected no api_events rows for dropped service event, got %#v", apiEvents.rows)
	}
}

func TestHandleAnalyticsEventMalformedPayloadWritesIngestError(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	event := kafka.AnalyticsEvent{
		EventID:   uuid.NewString(),
		EventType: "viewer_connect",
		Timestamp: time.Now(),
		Source:    "decklog",
		TenantID:  uuid.NewString(),
		Data: map[string]interface{}{
			"streamId": map[string]interface{}{"nested": true},
		},
	}

	err := handler.HandleAnalyticsEvent(event)
	if err == nil {
		t.Fatal("expected error for malformed payload")
	}

	batch := conn.batches["ingest_errors"]
	if batch == nil || len(batch.rows) != 1 {
		t.Fatalf("expected 1 ingest_errors row, got %#v", batch)
	}
	if reason, ok := batch.rows[0][6].(string); !ok || !strings.Contains(reason, "handler_error") {
		t.Fatalf("expected handler_error reason, got %#v", batch.rows[0][6])
	}
}

func TestViewerConnectionPayloadMismatch(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	streamID := uuid.NewString()
	data := mustMistTriggerData(t, &pb.MistTrigger{
		StreamId: &streamID,
		TriggerPayload: &pb.MistTrigger_ViewerDisconnect{
			ViewerDisconnect: &pb.ViewerDisconnectTrigger{
				StreamName: "live+demo",
				SessionId:  "sess-1",
				Connector:  "hls",
				Host:       "1.2.3.4",
			},
		},
	})
	event := kafka.AnalyticsEvent{
		EventID:   uuid.NewString(),
		EventType: "viewer_connect",
		Timestamp: time.Now(),
		Source:    "decklog",
		TenantID:  uuid.NewString(),
		Data:      data,
	}

	err := handler.HandleAnalyticsEvent(event)
	if err == nil || !strings.Contains(err.Error(), "payload mismatch") {
		t.Fatalf("expected payload mismatch error, got %v", err)
	}

	batch := conn.batches["ingest_errors"]
	if batch == nil || len(batch.rows) != 1 {
		t.Fatalf("expected ingest_errors row, got %#v", batch)
	}
}

func TestViewerConnectTenantAttribution(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	tenantID := uuid.NewString()
	streamID := uuid.NewString()
	data := mustMistTriggerData(t, &pb.MistTrigger{
		StreamId: &streamID,
		TriggerPayload: &pb.MistTrigger_ViewerConnect{
			ViewerConnect: &pb.ViewerConnectTrigger{
				StreamName: "live+demo",
				SessionId:  "sess-1",
				Connector:  "hls",
				Host:       "1.2.3.4",
				RequestUrl: "https://example.com/stream",
			},
		},
	})
	event := kafka.AnalyticsEvent{
		EventID:   uuid.NewString(),
		EventType: "viewer_connect",
		Timestamp: time.Now(),
		Source:    "decklog",
		TenantID:  tenantID,
		Data:      data,
	}

	if err := handler.HandleAnalyticsEvent(event); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	batch := conn.batches["viewer_connection_events"]
	if batch == nil || len(batch.rows) != 1 {
		t.Fatalf("expected viewer_connection_events row, got %#v", batch)
	}
	row := batch.rows[0]
	if row[2] != tenantID {
		t.Fatalf("expected tenant_id %q, got %#v", tenantID, row[2])
	}
	if row[4] != "demo" {
		t.Fatalf("expected internal_name demo, got %#v", row[4])
	}
	if row[20] != "connect" {
		t.Fatalf("expected event_type connect, got %#v", row[20])
	}
}

func TestViewerDisconnectOutOfOrderStillRecorded(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	tenantID := uuid.NewString()
	streamID := uuid.NewString()
	secondsConnected := uint64(42)
	data := mustMistTriggerData(t, &pb.MistTrigger{
		StreamId: &streamID,
		TriggerPayload: &pb.MistTrigger_ViewerDisconnect{
			ViewerDisconnect: &pb.ViewerDisconnectTrigger{
				StreamName:       "live+demo",
				SessionId:        "sess-2",
				Connector:        "hls",
				Host:             "1.2.3.4",
				SecondsConnected: &secondsConnected,
			},
		},
	})
	event := kafka.AnalyticsEvent{
		EventID:   uuid.NewString(),
		EventType: "viewer_disconnect",
		Timestamp: time.Now(),
		Source:    "decklog",
		TenantID:  tenantID,
		Data:      data,
	}

	if err := handler.HandleAnalyticsEvent(event); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	batch := conn.batches["viewer_connection_events"]
	if batch == nil || len(batch.rows) != 1 {
		t.Fatalf("expected viewer_connection_events row, got %#v", batch)
	}
	row := batch.rows[0]
	if row[20] != "disconnect" {
		t.Fatalf("expected event_type disconnect, got %#v", row[20])
	}
	if row[21] != uint32(42) {
		t.Fatalf("expected session_duration 42, got %#v", row[21])
	}
}

func TestViewerConnectionDuplicateEventSkipped(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	streamID := uuid.NewString()
	eventID := uuid.New()
	conn.addDuplicate("viewer_connection_events", eventID)
	data := mustMistTriggerData(t, &pb.MistTrigger{
		StreamId: &streamID,
		TriggerPayload: &pb.MistTrigger_ViewerConnect{
			ViewerConnect: &pb.ViewerConnectTrigger{
				StreamName: "live+demo",
				SessionId:  "sess-3",
				Connector:  "hls",
				Host:       "1.2.3.4",
			},
		},
	})
	event := kafka.AnalyticsEvent{
		EventID:   eventID.String(),
		EventType: "viewer_connect",
		Timestamp: time.Now(),
		Source:    "decklog",
		TenantID:  uuid.NewString(),
		Data:      data,
	}

	if err := handler.HandleAnalyticsEvent(event); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if batch := conn.batches["viewer_connection_events"]; batch != nil {
		t.Fatalf("expected duplicate event to skip insert, got %#v", batch.rows)
	}
}

func TestViewerConnectionClusterContextFallback(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	tenantID := uuid.NewString()
	streamID := uuid.NewString()

	t.Run("cluster inherits origin when missing", func(t *testing.T) {
		clusterFromOrigin := "origin-cluster-a"
		data := mustMistTriggerData(t, &pb.MistTrigger{
			StreamId:        &streamID,
			OriginClusterId: &clusterFromOrigin,
			TriggerPayload: &pb.MistTrigger_ViewerConnect{
				ViewerConnect: &pb.ViewerConnectTrigger{
					StreamName: "live+demo",
					SessionId:  "sess-origin",
					Connector:  "hls",
					Host:       "1.2.3.4",
				},
			},
		})
		event := kafka.AnalyticsEvent{
			EventID:   uuid.NewString(),
			EventType: "viewer_connect",
			Timestamp: time.Now(),
			Source:    "decklog",
			TenantID:  tenantID,
			Data:      data,
		}

		if err := handler.HandleAnalyticsEvent(event); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		batch := conn.batches["viewer_connection_events"]
		if batch == nil || len(batch.rows) == 0 {
			t.Fatalf("expected viewer_connection_events row, got %#v", batch)
		}
		row := batch.rows[len(batch.rows)-1]
		if row[9] != clusterFromOrigin {
			t.Fatalf("expected cluster_id fallback %q, got %#v", clusterFromOrigin, row[9])
		}
		if row[10] != clusterFromOrigin {
			t.Fatalf("expected origin_cluster_id %q, got %#v", clusterFromOrigin, row[10])
		}
	})

	t.Run("origin inherits cluster when missing", func(t *testing.T) {
		clusterID := "local-cluster-b"
		data := mustMistTriggerData(t, &pb.MistTrigger{
			StreamId:  &streamID,
			ClusterId: &clusterID,
			TriggerPayload: &pb.MistTrigger_ViewerConnect{
				ViewerConnect: &pb.ViewerConnectTrigger{
					StreamName: "live+demo",
					SessionId:  "sess-cluster",
					Connector:  "webrtc",
					Host:       "4.3.2.1",
				},
			},
		})
		event := kafka.AnalyticsEvent{
			EventID:   uuid.NewString(),
			EventType: "viewer_connect",
			Timestamp: time.Now(),
			Source:    "decklog",
			TenantID:  tenantID,
			Data:      data,
		}

		if err := handler.HandleAnalyticsEvent(event); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		batch := conn.batches["viewer_connection_events"]
		if batch == nil || len(batch.rows) == 0 {
			t.Fatalf("expected viewer_connection_events row, got %#v", batch)
		}
		row := batch.rows[len(batch.rows)-1]
		if row[9] != clusterID {
			t.Fatalf("expected cluster_id %q, got %#v", clusterID, row[9])
		}
		if row[10] != clusterID {
			t.Fatalf("expected origin_cluster_id fallback %q, got %#v", clusterID, row[10])
		}
	})
}

func TestHandleAnalyticsEventUnknownTypeSkipped(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	event := kafka.AnalyticsEvent{
		EventID:   uuid.NewString(),
		EventType: "totally_unknown",
		Timestamp: time.Now(),
		Source:    "decklog",
		TenantID:  uuid.NewString(),
		Data:      map[string]interface{}{"ignored": true},
	}

	if err := handler.HandleAnalyticsEvent(event); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(conn.batches) != 0 {
		t.Fatalf("expected no batches, got %#v", conn.batches)
	}
}

func TestClipLifecyclePersistsServingAndOriginClusterAttribution(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	tenantID := uuid.NewString()
	streamID := uuid.NewString()
	clusterID := "cluster-serving"
	originClusterID := "cluster-origin"

	data := mustMistTriggerData(t, &pb.MistTrigger{
		StreamId:        &streamID,
		ClusterId:       &clusterID,
		OriginClusterId: &originClusterID,
		TriggerPayload: &pb.MistTrigger_ClipLifecycleData{
			ClipLifecycleData: &pb.ClipLifecycleData{
				InternalName: stringPtr("live+demo-stream"),
				RequestId:    stringPtr("clip-request"),
				ClipHash:     "clip-hash-1",
				Stage:        pb.ClipLifecycleData_STAGE_DONE,
			},
		},
	})

	event := kafka.AnalyticsEvent{
		EventID:   uuid.NewString(),
		EventType: "clip_lifecycle",
		Timestamp: time.Now(),
		Source:    "decklog",
		TenantID:  tenantID,
		Data:      data,
	}

	if err := handler.HandleAnalyticsEvent(event); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	batch := conn.batches["artifact_events"]
	if batch == nil || len(batch.rows) != 1 {
		t.Fatalf("expected artifact_events row, got %#v", batch)
	}
	row := batch.rows[0]
	if row[4] != clusterID {
		t.Fatalf("expected cluster_id %q, got %#v", clusterID, row[4])
	}
	if row[5] != originClusterID {
		t.Fatalf("expected origin_cluster_id %q, got %#v", originClusterID, row[5])
	}
}

func TestHandleAnalyticsEventMissingStreamIDDropped(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	data := mustMistTriggerData(t, &pb.MistTrigger{
		TriggerPayload: &pb.MistTrigger_ViewerConnect{
			ViewerConnect: &pb.ViewerConnectTrigger{
				StreamName: "live+demo",
				SessionId:  "sess-4",
				Connector:  "hls",
				Host:       "1.2.3.4",
			},
		},
	})
	event := kafka.AnalyticsEvent{
		EventID:   uuid.NewString(),
		EventType: "viewer_connect",
		Timestamp: time.Now(),
		Source:    "decklog",
		TenantID:  uuid.NewString(),
		Data:      data,
	}

	if err := handler.HandleAnalyticsEvent(event); err != nil {
		t.Fatalf("expected drop without error, got %v", err)
	}

	batch := conn.batches["ingest_errors"]
	if batch == nil || len(batch.rows) != 1 {
		t.Fatalf("expected ingest_errors row, got %#v", batch)
	}
	if reason, ok := batch.rows[0][6].(string); !ok || !strings.Contains(reason, "missing_or_invalid_stream_id") {
		t.Fatalf("expected missing stream id reason, got %#v", batch.rows[0][6])
	}
}

func TestHandleAnalyticsEventFederationEventPreservesOptionalZeroValues(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	streamID := uuid.NewString()
	event := kafka.AnalyticsEvent{
		EventID:   uuid.NewString(),
		EventType: "federation_event",
		Timestamp: time.Now(),
		Source:    "decklog",
		TenantID:  uuid.NewString(),
		Data: mustMistTriggerData(t, &pb.MistTrigger{
			TriggerPayload: &pb.MistTrigger_FederationEventData{
				FederationEventData: &pb.FederationEventData{
					EventType:                  pb.FederationEventType_REPLICATION_LOOP_PREVENTED,
					LocalCluster:               "central-primary",
					RemoteCluster:              "us-east-edge",
					StreamName:                 stringPtr("demo_live_stream_001"),
					StreamId:                   stringPtr(streamID),
					LatencyMs:                  float32Ptr(0),
					TimeToLiveMs:               float32Ptr(0),
					QueriedClusters:            uint32Ptr(0),
					RespondingClusters:         uint32Ptr(0),
					TotalCandidates:            uint32Ptr(0),
					BestRemoteScore:            uint64Ptr(0),
					Role:                       stringPtr("leader"),
					BlockedCluster:             stringPtr("apac-edge"),
					ExistingReplicationCluster: stringPtr("us-east-edge"),
					LocalLat:                   float64Ptr(0),
					LocalLon:                   float64Ptr(0),
					RemoteLat:                  float64Ptr(40.7128),
					RemoteLon:                  float64Ptr(-74.0060),
				},
			},
		}),
	}

	if err := handler.HandleAnalyticsEvent(event); err != nil {
		t.Fatalf("HandleAnalyticsEvent() error = %v", err)
	}

	batch := conn.batches["federation_events"]
	if batch == nil || len(batch.rows) != 1 {
		t.Fatalf("expected federation_events row, got %#v", batch)
	}
	row := batch.rows[0]

	if row[10] != float32(0) {
		t.Fatalf("expected latency_ms 0.0, got %#v", row[10])
	}
	if row[11] != float32(0) {
		t.Fatalf("expected time_to_live_ms 0.0, got %#v", row[11])
	}
	if row[13] != uint32(0) || row[14] != uint32(0) || row[15] != uint32(0) {
		t.Fatalf("expected queried/responding/total candidates zeros, got %#v %#v %#v", row[13], row[14], row[15])
	}
	if row[16] != uint64(0) {
		t.Fatalf("expected best_remote_score 0, got %#v", row[16])
	}
	if row[20] != "apac-edge" {
		t.Fatalf("expected blocked_cluster apac-edge, got %#v", row[20])
	}
	if row[21] != "us-east-edge" {
		t.Fatalf("expected existing_replication_cluster us-east-edge, got %#v", row[21])
	}
	if row[22] != float64(0) || row[23] != float64(0) {
		t.Fatalf("expected local coordinates 0,0, got %#v %#v", row[22], row[23])
	}
}

func TestWriteIngestErrorPayloadMarshalFailure(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := &AnalyticsHandler{clickhouse: clickhouseNativeConn{conn: conn}, logger: logging.NewLogger()}
	event := kafka.AnalyticsEvent{
		EventID:   uuid.NewString(),
		EventType: "viewer_connect",
		Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		Source:    "decklog",
		TenantID:  uuid.NewString(),
		Data: map[string]interface{}{
			"bad": func() {},
		},
	}

	handler.writeIngestError(context.Background(), event, "stream-1", "handler_error", errors.New("boom"))

	batch := conn.batches["ingest_errors"]
	if batch == nil || len(batch.rows) != 1 {
		t.Fatalf("expected ingest_errors row, got %#v", batch)
	}
	row := batch.rows[0]
	if row[5] != "stream-1" {
		t.Fatalf("expected stream_id stream-1, got %#v", row[5])
	}
	if reason, ok := row[6].(string); !ok || !strings.Contains(reason, "payload_marshal_error") || !strings.Contains(reason, "boom") {
		t.Fatalf("expected marshal error and cause in reason, got %#v", row[6])
	}
	if row[7] != "{}" {
		t.Fatalf("expected payload_json {}, got %#v", row[7])
	}
}

func mustMistTriggerData(t *testing.T, mt *pb.MistTrigger) map[string]interface{} {
	t.Helper()
	bytes, err := protojson.Marshal(mt)
	if err != nil {
		t.Fatalf("failed to marshal MistTrigger: %v", err)
	}
	var data map[string]interface{}
	if err := json.Unmarshal(bytes, &data); err != nil {
		t.Fatalf("failed to unmarshal MistTrigger JSON: %v", err)
	}
	return data
}

type fakeClickhouseConn struct {
	batches    map[string]*fakeBatch
	duplicates map[string]map[uuid.UUID]bool
}

func newFakeClickhouseConn() *fakeClickhouseConn {
	return &fakeClickhouseConn{
		batches:    make(map[string]*fakeBatch),
		duplicates: make(map[string]map[uuid.UUID]bool),
	}
}

func (f *fakeClickhouseConn) addDuplicate(table string, eventID uuid.UUID) {
	if f.duplicates[table] == nil {
		f.duplicates[table] = make(map[uuid.UUID]bool)
	}
	f.duplicates[table][eventID] = true
}

func (f *fakeClickhouseConn) Contributors() []string { return nil }
func (f *fakeClickhouseConn) ServerVersion() (*driver.ServerVersion, error) {
	return nil, nil
}
func (f *fakeClickhouseConn) Select(ctx context.Context, dest any, query string, args ...any) error {
	return nil
}
func (f *fakeClickhouseConn) Query(ctx context.Context, query string, args ...any) (driver.Rows, error) {
	table := tableFromQuery(query, "from")
	var eventID uuid.UUID
	if len(args) > 0 {
		if parsed, ok := args[0].(uuid.UUID); ok {
			eventID = parsed
		}
	}
	dup := f.duplicates[table] != nil && f.duplicates[table][eventID]
	return &fakeRows{next: dup}, nil
}
func (f *fakeClickhouseConn) QueryRow(ctx context.Context, query string, args ...any) driver.Row {
	return &fakeRow{}
}
func (f *fakeClickhouseConn) PrepareBatch(ctx context.Context, query string, opts ...driver.PrepareBatchOption) (driver.Batch, error) {
	table := tableFromQuery(query, "into")
	batch := &fakeBatch{table: table}
	f.batches[table] = batch
	return batch, nil
}
func (f *fakeClickhouseConn) Exec(ctx context.Context, query string, args ...any) error {
	return nil
}
func (f *fakeClickhouseConn) AsyncInsert(ctx context.Context, query string, wait bool, args ...any) error {
	return nil
}
func (f *fakeClickhouseConn) Ping(ctx context.Context) error { return nil }
func (f *fakeClickhouseConn) Stats() driver.Stats            { return driver.Stats{} }
func (f *fakeClickhouseConn) Close() error                   { return nil }

type fakeBatch struct {
	table string
	rows  [][]any
	sent  bool
}

func (f *fakeBatch) Abort() error                  { return nil }
func (f *fakeBatch) Append(v ...any) error         { f.rows = append(f.rows, v); return nil }
func (f *fakeBatch) AppendStruct(v any) error      { return nil }
func (f *fakeBatch) Column(int) driver.BatchColumn { return &fakeBatchColumn{} }
func (f *fakeBatch) Flush() error                  { return nil }
func (f *fakeBatch) Send() error                   { f.sent = true; return nil }
func (f *fakeBatch) IsSent() bool                  { return f.sent }
func (f *fakeBatch) Rows() int                     { return len(f.rows) }
func (f *fakeBatch) Columns() []column.Interface   { return nil }
func (f *fakeBatch) Close() error                  { return nil }

type fakeBatchColumn struct{}

func (f *fakeBatchColumn) Append(any) error    { return nil }
func (f *fakeBatchColumn) AppendRow(any) error { return nil }

type fakeRows struct {
	next bool
}

func (f *fakeRows) Next() bool {
	if f.next {
		f.next = false
		return true
	}
	return false
}
func (f *fakeRows) Scan(dest ...any) error           { return nil }
func (f *fakeRows) ScanStruct(dest any) error        { return nil }
func (f *fakeRows) ColumnTypes() []driver.ColumnType { return nil }
func (f *fakeRows) Totals(dest ...any) error         { return nil }
func (f *fakeRows) Columns() []string                { return nil }
func (f *fakeRows) Close() error                     { return nil }
func (f *fakeRows) Err() error                       { return nil }

type fakeRow struct{}

func (f *fakeRow) Err() error                { return nil }
func (f *fakeRow) Scan(dest ...any) error    { return nil }
func (f *fakeRow) ScanStruct(dest any) error { return nil }

func tableFromQuery(query string, keyword string) string {
	fields := strings.Fields(query)
	for i, field := range fields {
		if strings.EqualFold(field, keyword) && i+1 < len(fields) {
			return strings.Trim(fields[i+1], "`")
		}
	}
	return ""
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

func float64Ptr(v float64) *float64 {
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

func uuidPtr(v uuid.UUID) *uuid.UUID {
	return &v
}
