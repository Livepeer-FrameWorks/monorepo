package control

import (
	"strings"
	"sync"
)

var dvrSourceOverrides = struct {
	sync.RWMutex
	byStream map[string]string
}{
	byStream: map[string]string{},
}

func RegisterDVRSourceOverride(streamName, sourceURL string) {
	streamName = strings.TrimSpace(streamName)
	sourceURL = strings.TrimSpace(sourceURL)
	if streamName == "" || sourceURL == "" {
		return
	}
	dvrSourceOverrides.Lock()
	dvrSourceOverrides.byStream[streamName] = sourceURL
	dvrSourceOverrides.Unlock()
}

func GetDVRSourceOverride(streamName string) (string, bool) {
	streamName = strings.TrimSpace(streamName)
	if streamName == "" {
		return "", false
	}
	dvrSourceOverrides.RLock()
	sourceURL, ok := dvrSourceOverrides.byStream[streamName]
	dvrSourceOverrides.RUnlock()
	return sourceURL, ok
}

func ClearDVRSourceOverride(streamName string) {
	streamName = strings.TrimSpace(streamName)
	if streamName == "" {
		return
	}
	dvrSourceOverrides.Lock()
	delete(dvrSourceOverrides.byStream, streamName)
	dvrSourceOverrides.Unlock()
}
