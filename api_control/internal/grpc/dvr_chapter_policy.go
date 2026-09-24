package grpc

import (
	"database/sql"
	"strings"
)

// DVR chapter modes as stored on commodore.streams and sent to Foghorn.
// commodore.streams stores NONE as NULL, so a stream that keeps nothing is
// sent explicitly as "none": Foghorn treats an empty mode as window-sized.
const (
	dvrChapterModeWindowSized   = "window_sized_chapters"
	dvrChapterModeFixedInterval = "fixed_interval"
	dvrChapterModeNone          = "none"
)

// streamDVRChapterPolicy converts a stream row's chapter columns into the
// mode and interval a recording snapshots. The interval is returned only for
// fixed_interval; any other mode derives its chapter length from the
// recording's own live window.
func streamDVRChapterPolicy(mode sql.NullString, interval sql.NullInt32) (string, int32) {
	normalized := strings.ToLower(strings.TrimSpace(mode.String))
	switch {
	case !mode.Valid || normalized == "" || normalized == dvrChapterModeNone:
		return dvrChapterModeNone, 0
	case normalized == dvrChapterModeFixedInterval:
		if interval.Valid && interval.Int32 > 0 {
			return dvrChapterModeFixedInterval, interval.Int32
		}
		return dvrChapterModeFixedInterval, 0
	default:
		return normalized, 0
	}
}
