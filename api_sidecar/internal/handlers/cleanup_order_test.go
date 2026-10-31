package handlers

import (
	"math"
	"testing"
	"time"
)

func TestStorageEvictionPrefersColdContent(t *testing.T) {
	now := time.Now()
	base := ClipCleanupInfo{CreatedAt: now.Add(-48 * time.Hour), LastAccessed: now.Add(-200 * time.Hour), SizeBytes: 100 * 1024 * 1024}
	for _, kind := range []string{"cleanup", "freeze"} {
		score := func(c ClipCleanupInfo) float64 {
			if kind == "cleanup" {
				return newTestCleanupMonitor().calculateCleanupPriority(c)
			}
			return (&StorageManager{}).calculateFreezePriority(FreezeCandidate{CreatedAt: c.CreatedAt, LastAccessed: c.LastAccessed, SizeBytes: c.SizeBytes, AccessCount: c.AccessCount})
		}
		for _, dimension := range []string{"age", "frequency", "recency", "size"} {
			t.Run(kind+"/"+dimension, func(t *testing.T) {
				cold, hot := base, base
				switch dimension {
				case "age":
					cold.CreatedAt = now.Add(-30 * 24 * time.Hour)
				case "frequency":
					hot.AccessCount = 99
				case "recency":
					hot.LastAccessed = now.Add(-time.Hour)
				case "size":
					cold.SizeBytes = 1000 * 1024 * 1024
				}
				if coldScore, hotScore := score(cold), score(hot); coldScore >= hotScore || math.IsNaN(coldScore) || math.IsNaN(hotScore) {
					t.Fatalf("ascending eviction must choose cold content first: cold=%v hot=%v", coldScore, hotScore)
				}
			})
		}
	}
}

func TestStorageEvictionScoreBounds(t *testing.T) {
	now := time.Now()
	for _, accessCount := range []int{-1, 0, int(^uint(0) >> 1)} {
		score := storageEvictionPriority(now, now.Add(time.Hour), now, 0, accessCount)
		if math.IsNaN(score) || math.IsInf(score, 0) || score <= 0 {
			t.Fatalf("invalid eviction score for access count %d: %v", accessCount, score)
		}
	}
}
