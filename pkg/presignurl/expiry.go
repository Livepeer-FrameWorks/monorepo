// Package presignurl reads the validity window that an S3 SigV4 presigned URL
// carries in its own query string. The signature covers X-Amz-Date and
// X-Amz-Expires, so the store rejects the URL after that instant whatever any
// cache that holds the URL assumes.
package presignurl

import (
	"net/url"
	"strconv"
	"strings"
	"time"
)

const amzDateLayout = "20060102T150405Z"

// Expiry returns the instant after which the store refuses rawURL. ok is false
// when rawURL is not a SigV4 presigned URL with a readable X-Amz-Date and
// X-Amz-Expires.
func Expiry(rawURL string) (time.Time, bool) {
	if strings.TrimSpace(rawURL) == "" {
		return time.Time{}, false
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return time.Time{}, false
	}
	q := u.Query()
	signedAt, err := time.Parse(amzDateLayout, q.Get("X-Amz-Date"))
	if err != nil {
		return time.Time{}, false
	}
	seconds, err := strconv.ParseInt(q.Get("X-Amz-Expires"), 10, 64)
	if err != nil || seconds <= 0 {
		return time.Time{}, false
	}
	return signedAt.Add(time.Duration(seconds) * time.Second), true
}

// EarliestExpiry returns the earliest Expiry among rawURLs. Empty entries are
// skipped. ok is false when no entry carries a readable expiry.
func EarliestExpiry(rawURLs ...string) (time.Time, bool) {
	var earliest time.Time
	found := false
	for _, raw := range rawURLs {
		exp, ok := Expiry(raw)
		if !ok {
			continue
		}
		if !found || exp.Before(earliest) {
			earliest = exp
			found = true
		}
	}
	return earliest, found
}
