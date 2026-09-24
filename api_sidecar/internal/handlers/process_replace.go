package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"frameworks/api_sidecar/internal/control"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	"github.com/gin-gonic/gin"
)

// noProcessReplacement is the PROCESS_REPLACE answer that leaves the failed
// process disabled without starting anything in its place.
const noProcessReplacement = "[]"

// ProcessReplaceEvent is a PROCESS_REPLACE trigger from MistServer: a process
// on a stream buffer exited unrecoverably and Mist asks which configs replace it.
type ProcessReplaceEvent struct {
	StreamName  string
	ProcessType string
	Config      string // failed process config JSON, as Mist supervised it
	ExitCode    int
	ShortReason string
	Reason      string
	// ReplacementCount is the number of configs Helmsman answered with; set
	// after the replacement is decided.
	ReplacementCount int
}

// ParseProcessReplaceTrigger parses the newline-separated PROCESS_REPLACE payload.
// Format: stream_name\nprocess_type\nfailed_config_json\nexit_code\nshort_reason\nlong_reason
func ParseProcessReplaceTrigger(body []byte) (ProcessReplaceEvent, error) {
	lines := strings.Split(strings.TrimRight(string(body), "\r\n"), "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) == "" {
		return ProcessReplaceEvent{}, fmt.Errorf("PROCESS_REPLACE payload too short: %d lines", len(lines))
	}
	evt := ProcessReplaceEvent{
		StreamName:  strings.TrimSpace(lines[0]),
		ProcessType: strings.TrimSpace(lines[1]),
		Config:      strings.TrimSpace(lines[2]),
	}
	if len(lines) > 3 {
		if v, err := strconv.Atoi(strings.TrimSpace(lines[3])); err == nil {
			evt.ExitCode = v
		}
	}
	if len(lines) > 4 {
		evt.ShortReason = strings.TrimSpace(lines[4])
	}
	if len(lines) > 5 {
		evt.Reason = strings.TrimSpace(strings.Join(lines[5:], "\n"))
	}
	return evt, nil
}

// processReplacementFor answers PROCESS_REPLACE. A failed Livepeer transcode
// is replaced by the equivalent local MistProcAV ladder; every other process
// type stays disabled. The replacement is derived from the failed entry alone
// because Mist layers it over the buffer's remaining processes.
func processReplacementFor(evt ProcessReplaceEvent) (string, int) {
	var failed map[string]interface{}
	if err := json.Unmarshal([]byte(evt.Config), &failed); err != nil || failed == nil {
		return noProcessReplacement, 0
	}
	processName := evt.ProcessType
	if name, ok := failed["process"].(string); ok && name != "" {
		processName = name
	}
	if processName != "Livepeer" {
		return noProcessReplacement, 0
	}
	failed["process"] = "Livepeer"
	wrapped, err := json.Marshal([]map[string]interface{}{failed})
	if err != nil {
		return noProcessReplacement, 0
	}
	local := mist.ReplaceLivepeerWithLocal(string(wrapped))
	// ReplaceLivepeerWithLocal returns its input unchanged when it cannot parse
	// it; answering with the failed Livepeer config would restart the failure.
	if mist.HasLivepeerProcesses(local) {
		return noProcessReplacement, 0
	}
	var replacements []map[string]interface{}
	if err := json.Unmarshal([]byte(local), &replacements); err != nil || len(replacements) == 0 {
		return noProcessReplacement, 0
	}
	return local, len(replacements)
}

func processReplaceReason(evt ProcessReplaceEvent) string {
	switch {
	case evt.ShortReason != "" && evt.Reason != "":
		return evt.ShortReason + ": " + evt.Reason
	case evt.Reason != "":
		return evt.Reason
	default:
		return evt.ShortReason
	}
}

// reportStreamTranscodeDegraded is replaceable in tests; production sends the
// control message to Foghorn.
var reportStreamTranscodeDegraded = func(evt ProcessReplaceEvent) error {
	return control.SendStreamTranscodeDegraded(evt.StreamName, evt.ProcessType, processReplaceReason(evt), evt.ReplacementCount)
}

// HandleProcessReplace handles the blocking PROCESS_REPLACE trigger. It answers
// locally so a replacement never waits on the control plane, then reports the
// degradation to Foghorn and to any processing job running on the stream.
func HandleProcessReplace(c *gin.Context) {
	incMistWebhook("PROCESS_REPLACE", "received")

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		incMistWebhook("PROCESS_REPLACE", "read_error")
		logger.WithError(err).Error("Failed to read PROCESS_REPLACE body")
		c.String(http.StatusOK, noProcessReplacement)
		return
	}
	evt, err := ParseProcessReplaceTrigger(body)
	if err != nil {
		incMistWebhook("PROCESS_REPLACE", "parse_error")
		logger.WithError(err).Error("Failed to parse PROCESS_REPLACE trigger")
		c.String(http.StatusOK, noProcessReplacement)
		return
	}

	replacement, count := processReplacementFor(evt)
	evt.ReplacementCount = count
	fields := logging.Fields{
		"stream":            evt.StreamName,
		"process_type":      evt.ProcessType,
		"exit_code":         evt.ExitCode,
		"short_reason":      evt.ShortReason,
		"reason":            evt.Reason,
		"replacement_count": count,
	}
	if count == 0 {
		incMistWebhook("PROCESS_REPLACE", "no_replacement")
		logger.WithFields(fields).Warn("PROCESS_REPLACE: no replacement for failed process; it stays disabled")
		c.String(http.StatusOK, noProcessReplacement)
		return
	}

	incMistWebhook("PROCESS_REPLACE", "replaced")
	logger.WithFields(fields).Warn("PROCESS_REPLACE: replacing failed Livepeer transcode with local MistProcAV renditions")
	RouteProcessReplace(evt)
	go func(evt ProcessReplaceEvent) {
		if err := reportStreamTranscodeDegraded(evt); err != nil {
			logger.WithError(err).WithField("stream", evt.StreamName).Warn("Failed to report transcode degradation to Foghorn")
		}
	}(evt)
	c.String(http.StatusOK, replacement)
}

var (
	processReplaceListeners   = map[string]chan ProcessReplaceEvent{}
	processReplaceListenersMu sync.Mutex
)

// RegisterProcessReplaceListener routes PROCESS_REPLACE answers for a stream to
// the processing job running on it.
func RegisterProcessReplaceListener(streamName string) chan ProcessReplaceEvent {
	processReplaceListenersMu.Lock()
	defer processReplaceListenersMu.Unlock()
	ch := make(chan ProcessReplaceEvent, 4)
	processReplaceListeners[streamName] = ch
	return ch
}

func UnregisterProcessReplaceListener(streamName string) {
	processReplaceListenersMu.Lock()
	defer processReplaceListenersMu.Unlock()
	delete(processReplaceListeners, streamName)
}

// RouteProcessReplace delivers an answered replacement to the stream's
// processing job. Live streams have no listener; the trigger answer alone
// drives their replacement.
func RouteProcessReplace(evt ProcessReplaceEvent) {
	processReplaceListenersMu.Lock()
	ch, ok := processReplaceListeners[evt.StreamName]
	processReplaceListenersMu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- evt:
	default:
		logger.WithField("stream_name", evt.StreamName).Warn("PROCESS_REPLACE listener queue full; event dropped")
	}
}
