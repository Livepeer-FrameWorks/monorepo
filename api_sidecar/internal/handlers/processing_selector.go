package handlers

import (
	"sort"
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"

	"github.com/sirupsen/logrus"
)

// Shared processing-track selection for live-artifact outputs (DVR chapter
// finalize and clips). These artifacts never run a fresh transcode: they
// publish the complete transcoded rendition set already present in the source
// material, otherwise the source passthrough — never a partial rendition set.
// Whether the produced output actually covers the requested span is enforced
// by the caller's post-push duration gate, not here.

// processingVideoSelector inspects the booted processing+ stream's current
// tracks and returns the `video=` selector to push: the complete requested
// rendition set when present, otherwise the source passthrough. spanMs is the
// authoritative source span used to reject truncated renditions.
func (h *ProcessingJobHandler) processingVideoSelector(log *logrus.Entry, mistClient *mist.Client, streamName, processesJSON string, readinessOutputs map[string]string, spanMs float64) string {
	presence := processingTrackPresence{outputs: readinessOutputs}
	if mistClient != nil {
		streamData, err := h.getActiveProcessingStreamData(mistClient, streamName)
		if err != nil {
			log.WithError(err).Warn("Track selection: could not inspect tracks before push; using source video")
		} else {
			presence = inspectProcessingActiveStream(streamData)
			if len(presence.outputs) == 0 {
				presence.outputs = readinessOutputs
			}
		}
	}
	source, _ := sourceFromReadinessOutputs(presence.outputs)
	if source.Height <= 0 {
		source, _ = sourceFromReadinessOutputs(readinessOutputs)
	}
	return chooseProcessingVideoSelector(log, processesJSON, presence.videoTracks, source, spanMs)
}

// chooseProcessingVideoSelector is the pure selection decision: the complete
// requested rendition set if every requested height maps to a distinct track
// that covers the span, otherwise the source passthrough. It never returns a
// partial rendition set.
func chooseProcessingVideoSelector(log *logrus.Entry, processesJSON string, videoTracks []processingMetaVideoTrack, source mist.SourceMediaInfo, spanMs float64) string {
	sourceSelector := processingSourceVideoSelector(videoTracks, source)
	expectedHeights, err := mist.RequestedRenditionHeights(processesJSON, source)
	if err != nil {
		log.WithError(err).Warn("Track selection: cannot determine requested rendition ladder; using source video")
		return sourceSelector
	}
	if len(expectedHeights) == 0 {
		return sourceSelector
	}
	if spanMs <= 0 {
		log.Warn("Track selection: source span unavailable before push; using source video")
		return sourceSelector
	}
	selected, ok := completeRenditionTracks(expectedHeights, videoTracks, source, spanMs)
	if !ok {
		log.WithFields(logrus.Fields{
			"requested_heights": expectedHeights,
			"available_heights": videoTrackHeights(videoTracks),
		}).Info("Track selection: complete rendition set unavailable; using source video")
		return sourceSelector
	}
	sort.Slice(selected, func(i, j int) bool {
		if selected[i].height == selected[j].height {
			return selected[i].selector() < selected[j].selector()
		}
		return selected[i].height > selected[j].height
	})
	parts := make([]string, 0, len(selected))
	for _, t := range selected {
		sel := t.selector()
		if sel == "" {
			log.WithField("height", t.height).Warn("Track selection: complete rendition lacks track identity; using source video")
			return sourceSelector
		}
		parts = append(parts, sel)
	}
	selector := strings.Join(parts, ",")
	log.WithFields(logrus.Fields{
		"video_selector":     selector,
		"requested_heights":  expectedHeights,
		"source_span_ms":     int64(spanMs),
		"selected_rendition": true,
	}).Info("Track selection: selecting complete rendition tracks")
	return selector
}

// processingSourceVideoSelector returns the selector for the source passthrough
// video track, falling back to the bare "source" keyword when the track has no
// usable identity.
func processingSourceVideoSelector(videoTracks []processingMetaVideoTrack, source mist.SourceMediaInfo) string {
	if idx := sourceVideoTrackIndex(videoTracks, source); idx >= 0 {
		if sel := videoTracks[idx].selector(); sel != "" {
			return sel
		}
	}
	return "source"
}

// completeRenditionTracks returns the tracks satisfying every requested height
// (each by a distinct track covering the span within tolerance), and false if
// the set is not fully satisfiable — so the caller never publishes a partial
// ladder. The source passthrough track is excluded from the candidate pool.
func completeRenditionTracks(expectedHeights []int, videoTracks []processingMetaVideoTrack, source mist.SourceMediaInfo, spanMs float64) ([]processingMetaVideoTrack, bool) {
	if len(videoTracks) == 0 {
		return nil, false
	}
	sourceIdx := sourceVideoTrackIndex(videoTracks, source)
	pool := make([]processingMetaVideoTrack, 0, len(videoTracks))
	for i, track := range videoTracks {
		if i == sourceIdx {
			continue
		}
		pool = append(pool, track)
	}

	consumed := make([]bool, len(pool))
	selected := make([]processingMetaVideoTrack, 0, len(expectedHeights))
	for _, expected := range expectedHeights {
		if expected <= 0 {
			return nil, false
		}
		matchIdx := -1
		for i, track := range pool {
			if consumed[i] || !renditionHeightsClose(track.height, expected) {
				continue
			}
			if spanMs-track.spanMs() > maxRenditionSpanShortfallMs {
				continue
			}
			if track.selector() == "" {
				continue
			}
			if matchIdx < 0 || track.spanMs() > pool[matchIdx].spanMs() {
				matchIdx = i
			}
		}
		if matchIdx < 0 {
			return nil, false
		}
		consumed[matchIdx] = true
		selected = append(selected, pool[matchIdx])
	}
	return selected, true
}

// startProcessingSelectorPush starts the local-disk push for a selection-based
// output (clip / DVR chapter) using the chosen video selector.
func (h *ProcessingJobHandler) startProcessingSelectorPush(log *logrus.Entry, mistClient *mist.Client, streamName, outputPath, videoSelector string) (int, error) {
	targetURI := processingMuxTargetURIWithVideo(outputPath, videoSelector)
	if err := mistClient.PushStart(streamName, targetURI); err != nil {
		return 0, err
	}
	pushID := findProcessingPushID(log, mistClient, streamName, targetURI)
	log.WithFields(logrus.Fields{
		"output_path": outputPath,
		"target_uri":  targetURI,
		"push_id":     pushID,
	}).Info("Selection push started")
	return pushID, nil
}
