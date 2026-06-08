package grpc

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func decodeStoredStreamTracks(trackListJSON string) []*ipcpb.StreamTrack {
	if strings.TrimSpace(trackListJSON) == "" {
		return nil
	}

	var raw any
	if err := json.Unmarshal([]byte(trackListJSON), &raw); err != nil {
		return nil
	}

	switch v := raw.(type) {
	case []any:
		tracks := make([]*ipcpb.StreamTrack, 0, len(v))
		for i, item := range v {
			if m, ok := item.(map[string]any); ok {
				tracks = append(tracks, streamTrackFromMap("", i, m))
			}
		}
		return tracks
	case map[string]any:
		tracks := make([]*ipcpb.StreamTrack, 0, len(v))
		index := 0
		for name, item := range v {
			if m, ok := item.(map[string]any); ok {
				tracks = append(tracks, streamTrackFromMap(name, index, m))
				index++
			}
		}
		return tracks
	default:
		return nil
	}
}

func streamTrackFromMap(name string, index int, m map[string]any) *ipcpb.StreamTrack {
	track := &ipcpb.StreamTrack{}
	if v := stringFromAny(m, "track_name", "trackName", "name"); v != "" {
		track.TrackName = v
	} else if name != "" {
		track.TrackName = name
	}
	if v := stringFromAny(m, "track_type", "trackType", "type"); v != "" {
		track.TrackType = strings.ToLower(v)
	}
	if v := stringFromAny(m, "codec"); v != "" {
		track.Codec = v
	}
	if v, ok := int64FromMap(m, "track_index", "trackIndex", "idx"); ok {
		n := int32(v)
		track.TrackIndex = &n
	}
	if v, ok := int64FromMap(m, "track_id", "trackId", "id"); ok {
		track.TrackId = &v
	}
	if v, ok := int64FromMap(m, "first_ms", "firstMs", "firstms"); ok {
		track.FirstMs = &v
	}
	if v, ok := int64FromMap(m, "last_ms", "lastMs", "lastms"); ok {
		track.LastMs = &v
	}
	if v, ok := int64FromMap(m, "bitrate_kbps", "bitrateKbps", "kbits"); ok {
		n := int32(v)
		track.BitrateKbps = &n
		bps := int64(n) * 1000
		track.BitrateBps = &bps
	}
	if v, ok := int64FromMap(m, "bitrate_bps", "bitrateBps", "bps"); ok {
		track.BitrateBps = &v
		if track.BitrateKbps == nil {
			kbps := int32(v / 1000)
			track.BitrateKbps = &kbps
		}
	}
	if v, ok := int64FromMap(m, "buffer"); ok {
		n := int32(v)
		track.Buffer = &n
	}
	if v, ok := int64FromMap(m, "jitter"); ok {
		n := int32(v)
		track.Jitter = &n
	}
	if v, ok := int64FromMap(m, "width"); ok {
		n := int32(v)
		track.Width = &n
	}
	if v, ok := int64FromMap(m, "height"); ok {
		n := int32(v)
		track.Height = &n
	}
	if v, ok := float64FromMap(m, "fps"); ok {
		track.Fps = &v
	} else if v, ok := float64FromMap(m, "fpks"); ok {
		fps := v / 1000
		track.Fps = &fps
	}
	if v := stringFromAny(m, "resolution"); v != "" {
		track.Resolution = &v
	} else if track.Width != nil && track.Height != nil {
		resolution := strings.Join([]string{intString(*track.Width), intString(*track.Height)}, "x")
		track.Resolution = &resolution
	}
	if v, ok := boolFromMap(m, "has_bframes", "hasBFrames", "bframes"); ok {
		track.HasBframes = &v
	}
	if v, ok := int64FromMap(m, "channels"); ok {
		n := int32(v)
		track.Channels = &n
	}
	if v, ok := int64FromMap(m, "sample_rate", "sampleRate", "rate"); ok {
		n := int32(v)
		track.SampleRate = &n
	}
	if v, ok := boolFromMap(m, "selected"); ok {
		track.Selected = &v
	}

	if track.TrackType == "" {
		track.TrackType = inferStoredTrackType(track)
	}

	return track
}

func inferStoredTrackType(track *ipcpb.StreamTrack) string {
	name := strings.ToLower(track.TrackName)
	codec := strings.ToUpper(track.Codec)
	switch {
	case strings.Contains(name, "audio") || codec == "AAC" || codec == "OPUS" || codec == "MP3":
		return "audio"
	case strings.Contains(name, "meta") || codec == "JSON":
		return "meta"
	case strings.Contains(name, "thumb") || strings.Contains(name, "jpeg") || strings.Contains(name, "jpg") || codec == "JPEG" || codec == "JPG" || codec == "THUMBVTT":
		return "unknown"
	case track.Width != nil || track.Height != nil || codec == "H264" || codec == "H265" || codec == "HEVC" || codec == "AV1" || strings.Contains(name, "video"):
		return "video"
	default:
		return "unknown"
	}
}

func stringFromAny(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key].(string); ok {
			if trimmed := strings.TrimSpace(v); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func int64FromMap(m map[string]any, keys ...string) (int64, bool) {
	for _, key := range keys {
		switch v := m[key].(type) {
		case float64:
			if math.IsNaN(v) || math.IsInf(v, 0) {
				continue
			}
			return int64(v), true
		case int64:
			return v, true
		case int:
			return int64(v), true
		case json.Number:
			if i, err := v.Int64(); err == nil {
				return i, true
			}
		}
	}
	return 0, false
}

func float64FromMap(m map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		switch v := m[key].(type) {
		case float64:
			if math.IsNaN(v) || math.IsInf(v, 0) {
				continue
			}
			return v, true
		case int64:
			return float64(v), true
		case int:
			return float64(v), true
		case json.Number:
			if f, err := v.Float64(); err == nil {
				return f, true
			}
		}
	}
	return 0, false
}

func boolFromMap(m map[string]any, keys ...string) (bool, bool) {
	for _, key := range keys {
		if v, ok := m[key].(bool); ok {
			return v, true
		}
	}
	return false, false
}

func intString(v int32) string { return strconv.FormatInt(int64(v), 10) }
