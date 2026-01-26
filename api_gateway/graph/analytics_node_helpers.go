package graph

import (
	"math"
	"strconv"
	"time"

	"frameworks/api_gateway/graph/model"
)

func parseUnixNanoPart(part string) (time.Time, error) {
	nanos, err := strconv.ParseInt(part, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(0, nanos).UTC(), nil
}

func timeRangeAround(ts time.Time, window time.Duration) *model.TimeRangeInput {
	if ts.IsZero() {
		return nil
	}
	start := ts.Add(-window)
	end := ts.Add(window)
	return &model.TimeRangeInput{Start: start, End: end}
}

func timesClose(a, b time.Time) bool {
	if a.IsZero() || b.IsZero() {
		return a.Equal(b)
	}
	diff := math.Abs(float64(a.Sub(b)))
	return diff <= float64(time.Second)
}
