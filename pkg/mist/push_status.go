package mist

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// PushEndStatus is the bounded, credential-free subset of Mist's PUSH_END
// status value used for restream accounting and retry classification.
type PushEndStatus struct {
	Succeeded     bool
	DurationMS    int64
	BytesSent     uint64
	BytesObserved bool
}

// ParsePushEndStatus accepts Mist's legacy numeric status and its successful
// JSON statistics object. Unknown or malformed non-zero forms fail closed.
func ParsePushEndStatus(raw string) PushEndStatus {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "0" {
		return PushEndStatus{Succeeded: true}
	}
	if !strings.HasPrefix(raw, "{") {
		return PushEndStatus{}
	}
	var values map[string]any
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return PushEndStatus{}
	}
	durationMS, durationObserved := firstDurationMS(values)
	bytesSent, bytesObserved := firstUint(values, "bytes_sent", "sent_bytes", "bytes", "up")
	return PushEndStatus{
		Succeeded:     durationObserved || bytesObserved,
		DurationMS:    durationMS,
		BytesSent:     bytesSent,
		BytesObserved: bytesObserved,
	}
}

func firstDurationMS(values map[string]any) (int64, bool) {
	if value, ok := firstNumber(values, "active_ms", "duration_ms", "media_duration_ms"); ok {
		return clampInt64(value), true
	}
	if value, ok := firstNumber(values, "active_seconds", "duration", "duration_sec", "seconds", "time"); ok {
		return clampInt64(value * 1000), true
	}
	return 0, false
}

func firstUint(values map[string]any, keys ...string) (uint64, bool) {
	value, ok := firstNumber(values, keys...)
	if !ok || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	if value >= math.MaxUint64 {
		return math.MaxUint64, true
	}
	return uint64(value), true
}

func firstNumber(values map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		value, ok := values[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case float64:
			return typed, true
		case string:
			parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
			if err == nil {
				return parsed, true
			}
		}
	}
	return 0, false
}

func clampInt64(value float64) int64 {
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	if value >= math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(value)
}
