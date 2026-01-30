package graph

import (
	"strings"
	"time"
)

func parsePeriodRange(period string) (*time.Time, *time.Time) {
	parts := strings.Split(period, "/")
	if len(parts) < 2 {
		return nil, nil
	}
	start, err := time.Parse(time.RFC3339, parts[0])
	if err != nil {
		return nil, nil
	}
	end, err := time.Parse(time.RFC3339, parts[1])
	if err != nil {
		return nil, nil
	}
	return &start, &end
}
