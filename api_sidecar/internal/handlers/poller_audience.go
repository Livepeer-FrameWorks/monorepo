package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

const audienceSnapshotKey = "frameworks_audience_viewers"

func (pm *PrometheusMonitor) fetchNodeAudience(ctx context.Context, runtime *mistNodeRuntime, jsonData map[string]any) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	runtime.audienceMu.Lock()
	client := runtime.audienceClient
	if client == nil {
		client = runtime.client
	}
	clients, err := client.GetClientsContext(ctx)
	runtime.audienceMu.Unlock()
	if err != nil {
		return nil, err
	}
	counts, untagged, err := runtime.audienceViewers(clients)
	if err != nil {
		return nil, err
	}
	if len(untagged) > 0 {
		runtime.launch(func(ctx context.Context) {
			ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			if err := runtime.tagProcessingSessions(ctx, untagged); err != nil {
				monitorLogger.WithError(err).Warn("Could not persist processing session tags in Mist")
			}
		})
	}
	jsonData[audienceSnapshotKey] = counts
	return clients, nil
}

func (runtime *mistNodeRuntime) audienceViewers(response map[string]any) (map[string]uint32, []string, error) {
	clients, ok := response["clients"].(map[string]any)
	if !ok {
		return nil, nil, errors.New("mist audience response has no clients")
	}
	fields, ok := clients["fields"].([]any)
	if !ok {
		return nil, nil, errors.New("mist audience response has no fields")
	}
	indexes := map[string]int{}
	for i, field := range fields {
		if name, ok := field.(string); ok {
			indexes[name] = i
		}
	}
	for _, field := range []string{"stream", "sessid", "tags"} {
		if _, ok := indexes[field]; !ok {
			return nil, nil, fmt.Errorf("mist audience response lacks %s", field)
		}
	}
	rows, validRows := clients["data"].([]any)
	if clients["data"] != nil && !validRows {
		return nil, nil, errors.New("mist audience response has invalid rows")
	}
	runtime.processingMu.Lock()
	defer runtime.processingMu.Unlock()
	present := make(map[string]struct{})
	counts := make(map[string]uint32)
	var untagged []string
	for _, raw := range rows {
		row, ok := raw.([]any)
		if !ok {
			return nil, nil, errors.New("mist audience response has invalid client")
		}
		for _, field := range []string{"stream", "sessid", "tags"} {
			if indexes[field] >= len(row) {
				return nil, nil, errors.New("mist audience response has truncated client")
			}
		}
		name, ok := row[indexes["stream"]].(string)
		if !ok {
			return nil, nil, errors.New("mist audience client has invalid stream")
		}
		sid, ok := row[indexes["sessid"]].(string)
		if !ok {
			return nil, nil, errors.New("mist audience client has invalid session")
		}
		if sid == "" {
			continue
		}
		present[sid] = struct{}{}
		if strings.ContainsAny(sid[:1], "IOU") {
			continue
		}
		tagged := false
		if tags, ok := row[indexes["tags"]].([]any); ok {
			for _, tag := range tags {
				if tag == "fwproc" {
					tagged = true
				}
			}
		}
		if _, tracked := runtime.processingSessions[sid]; tracked {
			runtime.processingSessions[sid] = 0
			if !tagged {
				untagged = append(untagged, sid)
			}
			continue
		}
		if tagged || isInternalMistRuntimeStream(name) {
			continue
		}
		counts[name]++
	}
	for sid, misses := range runtime.processingSessions {
		if _, ok := present[sid]; ok {
			runtime.processingSessions[sid] = 0
			continue
		}
		if misses >= 1 {
			delete(runtime.processingSessions, sid)
		} else {
			runtime.processingSessions[sid] = misses + 1
		}
	}
	return counts, untagged, nil
}

func (runtime *mistNodeRuntime) tagProcessingSessions(ctx context.Context, sessions []string) error {
	runtime.acceleratorMu.Lock()
	defer runtime.acceleratorMu.Unlock()
	return runtime.acceleratorClient.TagSessionIDs(ctx, sessions, "fwproc")
}

func rememberProcessingRead(viewer *ipcpb.ViewerConnectTrigger) {
	if viewer == nil || !strings.HasPrefix(viewer.GetViewerToken(), mist.ProcessingReadSessionPrefix) || viewer.GetSessionId() == "" || prometheusMonitor == nil {
		return
	}
	runtime := prometheusMonitor.currentNodeRuntime()
	if runtime == nil {
		return
	}
	runtime.processingMu.Lock()
	if runtime.processingSessions == nil {
		runtime.processingSessions = make(map[string]uint8)
	}
	runtime.processingSessions[viewer.GetSessionId()] = 0
	runtime.processingMu.Unlock()
	session := viewer.GetSessionId()
	runtime.launch(func(ctx context.Context) {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if err := runtime.tagProcessingSessions(ctx, []string{session}); err != nil {
			monitorLogger.WithError(err).Warn("Could not tag processing session in Mist; the next census retries")
		}
	})
}

func forgetProcessingRead(disconnect *ipcpb.ViewerDisconnectTrigger) {
	if disconnect == nil || !mist.IsProcessingReadDisconnect(disconnect) || prometheusMonitor == nil {
		return
	}
	runtime := prometheusMonitor.currentNodeRuntime()
	if runtime == nil {
		return
	}
	runtime.processingMu.Lock()
	delete(runtime.processingSessions, disconnect.GetSessionId())
	runtime.processingMu.Unlock()
}
