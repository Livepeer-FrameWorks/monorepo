package handlers

import (
	"testing"
	"time"
)

// A thumbnail job may complete only after the thumbnails Mist last announced during the job were confirmed
// uploaded and no newer announcement followed within the quiet window; Foghorn refuses a mint once the job ended.
func TestThumbnailUploadSettled(t *testing.T) {
	start := time.Unix(1000, 0)
	update := start.Add(time.Minute)
	confirmed := update.Add(2 * time.Second)
	settledAt := update.Add(thumbnailSettleQuiet)
	cases := []struct {
		name                    string
		lastUpdate, lastConfirm time.Time
		now                     time.Time
		want                    bool
	}{
		{"confirmed after the last announcement and quiet", update, confirmed, settledAt, true},
		{"no announcement during the job", start.Add(-time.Second), confirmed, settledAt, false},
		{"announcement not yet confirmed", update, update.Add(-time.Second), settledAt, false},
		{"never confirmed", update, time.Time{}, settledAt, false},
		{"announcement still within the quiet window", update, confirmed, update.Add(thumbnailSettleQuiet - time.Second), false},
	}
	for _, tc := range cases {
		if got := thumbnailUploadSettled(start, tc.lastUpdate, tc.lastConfirm, tc.now); got != tc.want {
			t.Errorf("%s: settled = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// THUMBNAIL_UPDATED for a stream is what the thumbnail job's settle check reads.
func TestNoteThumbnailUpdatedIsPerStream(t *testing.T) {
	at := time.Unix(2000, 0)
	noteThumbnailUpdated("processing+noteupdatedhash", at)
	if got := lastThumbnailUpdate("processing+noteupdatedhash"); !got.Equal(at) {
		t.Fatalf("last update = %v, want %v", got, at)
	}
	if got := lastThumbnailUpdate("processing+otherhash"); !got.IsZero() {
		t.Fatalf("unannounced stream reports %v", got)
	}
}
