package handlers

import (
	"context"
	"os"
	"path/filepath"
	"reflect"

	"frameworks/api_sidecar/internal/appconfig/appconfigtest"
	"frameworks/api_sidecar/internal/control"
	logtest "github.com/sirupsen/logrus/hooks/test"
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

func TestCleanupPassVisitsColdContentFirst(t *testing.T) {
	if control.IsConnected() {
		t.Fatal("fixture must exercise the disconnected safety gate")
	}
	appconfigtest.Setenv(t, "HELMSMAN_STORAGE_CAPACITY_BYTES", "200")
	oldMonitor := prometheusMonitor
	prometheusMonitor = nil
	t.Cleanup(func() { prometheusMonitor = oldMonitor })
	base := t.TempDir()
	const hot, cold = "20260927110000aaaaaa", "20260927120000bbbbbb"
	writeAged(t, filepath.Join(base, "clips", hot+".mp4"), 100, 48*time.Hour)
	writeAged(t, filepath.Join(base, "clips", cold+".mp4"), 100, 30*24*time.Hour)
	log, hook := logtest.NewNullLogger()
	cm := &CleanupMonitor{logger: log, basePath: base, cleanupThreshold: 0.9, targetThreshold: 0.5, minRetentionHours: 1}
	if err := cm.checkAndCleanup(); err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, entry := range hook.AllEntries() {
		if entry.Message == "Cleanup skipped: Foghorn disconnected, cannot verify sync status" {
			order = append(order, entry.Data["artifact_hash"].(string))
		}
	}
	if !reflect.DeepEqual(order, []string{cold, hot}) {
		t.Fatalf("cleanup pass order=%v want cold then hot", order)
	}
}

func TestReclaimToTargetEvictsColdContentFirst(t *testing.T) {
	sm := newTestStorageManager(t)
	sm.presignedClient = &fakePresignedClient{}
	sm.minRetentionHours = 1
	const hot, cold = "20260927110000aaaaaa", "20260927120000bbbbbb"
	hotPath, coldPath := filepath.Join(sm.basePath, "clips", hot+".mp4"), filepath.Join(sm.basePath, "clips", cold+".mp4")
	writeAged(t, hotPath, 100, 48*time.Hour)
	writeAged(t, coldPath, 100, 30*24*time.Hour)
	var consulted []string
	sm.requestCanDelete = func(_ context.Context, hash string) (bool, string, int64, error) {
		consulted = append(consulted, hash)
		return true, "remote_synced", 0, nil
	}
	result := sm.reclaimToTarget(100)
	if result.freedBytes != 100 || result.deletedCount != 1 || !reflect.DeepEqual(consulted, []string{cold}) {
		t.Fatalf("pressure pass result=%+v consulted=%v want only cold file", result, consulted)
	}
	if _, err := os.Stat(coldPath); !os.IsNotExist(err) {
		t.Fatalf("cold content retained: %v", err)
	}
	if _, err := os.Stat(hotPath); err != nil {
		t.Fatalf("hot content evicted: %v", err)
	}
}
