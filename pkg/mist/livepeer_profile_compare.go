package mist

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// LivepeerProfileMismatch compares the transcode profiles a Livepeer gateway
// reports with the profiles a job spec authorises and describes the first
// difference, or returns "" when they would transcode identically.
//
// The gateway does not forward Mist's profile JSON verbatim: it decodes it into
// lpms' ffmpeg.JsonProfile and re-encodes that struct, which drops every key
// lpms does not know and adds the ones it does (encoder, colorDepth,
// chromaFormat, quality) with zero values. The comparison therefore covers
// exactly the JsonProfile fields, treats their zero values as "unset" the way
// lpms does, and compares frame rates as rationals and GOPs numerically.
//
// A profile that follows the source frame rate is filled from two independent
// measurements: Mist's own track rate on the edge and the gateway's probe of
// the segment (source). Such a rate matches when the observed one is within
// 1 fps of the source; an explicitly configured rate must match exactly.
func LivepeerProfileMismatch(observed, expected []LivepeerJSONProfile, source SourceMediaInfo) string {
	if len(observed) != len(expected) {
		return fmt.Sprintf("profile count: observed %d, expected %d (observed %s, expected %s)",
			len(observed), len(expected), livepeerProfileNames(observed), livepeerProfileNames(expected))
	}
	for i := range observed {
		if diff := livepeerProfileFieldMismatch(observed[i], expected[i], source); diff != "" {
			return fmt.Sprintf("profile %d (%s): %s", i, livepeerProfileString(expected[i], "name"), diff)
		}
	}
	return ""
}

func livepeerProfileFieldMismatch(observed, expected LivepeerJSONProfile, source SourceMediaInfo) string {
	for _, key := range []string{"name", "profile", "encoder"} {
		o, e := livepeerProfileString(observed, key), livepeerProfileString(expected, key)
		if o != e {
			return fmt.Sprintf("%s: observed %q, expected %q", key, o, e)
		}
	}
	for _, key := range []string{"width", "height", "bitrate", "colorDepth", "chromaFormat", "quality"} {
		o, _ := livepeerProfileFloat(observed, key)
		e, _ := livepeerProfileFloat(expected, key)
		if o != e {
			return fmt.Sprintf("%s: observed %v, expected %v", key, o, e)
		}
	}
	oNum, oDen := livepeerProfileFrameRate(observed)
	eNum, eDen := livepeerProfileFrameRate(expected)
	if oNum*eDen != eNum*oDen && !livepeerSourceRateMatches(oNum/oDen, eNum, eDen, source) {
		return fmt.Sprintf("fps: observed %v/%v, expected %v/%v (source %v fps)", oNum, oDen, eNum, eDen, source.FPS)
	}
	if o, e := livepeerProfileGOP(observed), livepeerProfileGOP(expected); o != e {
		return fmt.Sprintf("gop: observed %q, expected %q", o, e)
	}
	return ""
}

func livepeerProfileString(profile LivepeerJSONProfile, key string) string {
	s, ok := profile[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

// livepeerProfileFrameRate returns fps/fpsDen with lpms' convention that a
// zero denominator means 1.
func livepeerProfileFrameRate(profile LivepeerJSONProfile) (float64, float64) {
	num, _ := livepeerProfileFloat(profile, "fps")
	den, _ := livepeerProfileFloat(profile, "fpsDen")
	if den == 0 {
		den = 1
	}
	return math.Round(num), math.Round(den)
}

// livepeerSourceRateMatches reports whether an expected rate is the
// source-derived one NormalizeLivepeerProfiles fills in (source fps in fpks, or
// its 25 fps default when the source rate is unknown) and the observed rate is
// an equivalent measurement of that source.
func livepeerSourceRateMatches(observedFPS, expectedNum, expectedDen float64, source SourceMediaInfo) bool {
	sourceFpks := math.Round(source.FPS * 1000)
	if sourceFpks == 0 {
		return expectedNum == 25000 && expectedDen == 1000 && observedFPS == 25
	}
	if expectedNum != sourceFpks || expectedDen != 1000 {
		return false
	}
	return math.Abs(observedFPS-source.FPS) <= 1
}

// livepeerProfileGOP canonicalises the GOP string: a numeric GOP compares by
// value, and an empty or zero GOP both mean the encoder default.
func livepeerProfileGOP(profile LivepeerJSONProfile) string {
	raw := livepeerProfileString(profile, "gop")
	if raw == "" {
		return "0"
	}
	if v, err := strconv.ParseFloat(raw, 64); err == nil {
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return raw
}

func livepeerProfileNames(profiles []LivepeerJSONProfile) string {
	names := make([]string, 0, len(profiles))
	for _, p := range profiles {
		names = append(names, livepeerProfileString(p, "name"))
	}
	return "[" + strings.Join(names, ",") + "]"
}
