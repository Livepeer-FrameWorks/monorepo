package handlers

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	"github.com/sirupsen/logrus"
)

// processingSourceVideoTracksOutput is the readiness output holding the
// stream's continuous-video tracks as a JSON []mist.TrackInfo. Memory admission
// reads it to leave retained rungs unreserved; it is internal to Helmsman and
// stripped from the completed result.
const processingSourceVideoTracksOutput = "source_video_tracks"

// healthTrackBps converts the kbits Mist's stream health reports (bps*8/1024)
// back to Mist's bytes-per-second track bps.
func healthTrackBps(track map[string]interface{}) uint64 {
	if kbits, ok := track["kbits"].(float64); ok && kbits > 0 {
		return uint64(kbits) * 1024 / 8
	}
	return 0
}

// retentionTracks describes video tracks to the rendition retention rule.
// TrackInfo.Index is the track's rank in Mist track order (index, then id,
// then name), the order the rule breaks ties in; positions maps a rank back
// to videoTracks. A track is original by the same lineage rule as
// sourceVideoTrackIndexes. Thumbnail tracks are not in the input lists.
func retentionTracks(videoTracks []processingMetaVideoTrack, sourceIDs map[int64]bool) ([]mist.TrackInfo, []int) {
	positions := make([]int, len(videoTracks))
	for i := range positions {
		positions[i] = i
	}
	sort.SliceStable(positions, func(a, b int) bool {
		return processingVideoTrackBefore(videoTracks[positions[a]], videoTracks[positions[b]])
	})
	original := map[int]bool{}
	for _, idx := range sourceVideoTrackIndexes(videoTracks, sourceIDs) {
		original[idx] = true
	}
	infos := make([]mist.TrackInfo, len(positions))
	for rank, pos := range positions {
		t := videoTracks[pos]
		infos[rank] = mist.TrackInfo{
			Index: rank, Codec: t.codec, Width: t.width, Height: t.height, Bps: t.bps,
			Original: original[pos], Image: mist.IsImageVideoCodec(t.codec),
			Rotation: t.rotation, Mirror: t.mirror,
			HasBframes: t.hasBframes,
		}
	}
	return infos, positions
}

// readinessVideoTracks returns the track list extractActiveStreamMetadata
// recorded; nil when the outputs predate it, which retains nothing.
func readinessVideoTracks(outputs map[string]string) []mist.TrackInfo {
	raw := outputs[processingSourceVideoTracksOutput]
	if raw == "" {
		return nil
	}
	var snapshot []struct {
		mist.TrackInfo
		Rotation   *int  `json:"rotation"`
		Mirror     *bool `json:"mirror"`
		HasBframes *bool `json:"has_bframes"`
	}
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return nil
	}
	tracks := make([]mist.TrackInfo, len(snapshot))
	for i, track := range snapshot {
		// A cache without orientation or frame reordering cannot prove a
		// compatible original. Reserve its encoder until readiness refreshes it.
		if track.Rotation == nil || track.Mirror == nil || track.HasBframes == nil {
			return nil
		}
		tracks[i] = track.TrackInfo
		tracks[i].Rotation, tracks[i].Mirror = *track.Rotation, *track.Mirror
		tracks[i].HasBframes = *track.HasBframes
	}
	return tracks
}

// splitRetainedRenditions separates the rungs a process must render, by short
// side, from the videoTracks positions of the originals that retain a rung.
func splitRetainedRenditions(expected []mist.ExpectedRendition, positions []int) (shortSides, retained []int) {
	for _, rung := range expected {
		if rung.Retained && rung.TrackIndex >= 0 && rung.TrackIndex < len(positions) {
			retained = append(retained, positions[rung.TrackIndex])
			continue
		}
		shortSides = append(shortSides, rung.ShortSide)
	}
	return shortSides, retained
}

// processesMaskReplacedOriginals reports whether the policy hides the
// original tracks its transcoded rungs replace (mask_replaced_originals).
func processesMaskReplacedOriginals(processesJSON string) bool {
	var processes []map[string]interface{}
	if err := json.Unmarshal([]byte(processesJSON), &processes); err != nil {
		return false
	}
	for _, proc := range processes {
		if mask, ok := proc["mask_replaced_originals"].(bool); ok && mask {
			return true
		}
	}
	return false
}

// processingRecordingVideoSelector is the video selector of the stored VOD:
// every video track except the originals the ladder masks. Under
// mask_replaced_originals, once any rung is transcoded, an original that
// retains no rung is replaced by the ladder (mist.PlanRetention), the
// transcode source included; retained originals stay as renditions. Mist
// re-applies the selector as process outputs appear, so it is fixed at push
// start from the processing stream's original tracks.
func processingRecordingVideoSelector(log *logrus.Entry, processesJSON string, videoTracks []processingMetaVideoTrack, source mist.SourceMediaInfo) string {
	if !processesMaskReplacedOriginals(processesJSON) {
		return "all"
	}
	infos, positions := retentionTracks(videoTracks, nil)
	expected, err := mist.ExpectedVideoRenditions(processesJSON, source, infos...)
	if err != nil {
		log.WithError(err).Warn("Recording selector: cannot determine requested rendition ladder; recording every video track")
		return "all"
	}
	retained := map[int]bool{}
	transcodes := false
	for _, rung := range expected {
		if rung.Retained {
			retained[rung.TrackIndex] = true
		} else {
			transcodes = true
		}
	}
	if !transcodes {
		return "all"
	}
	parts := []string{"all"}
	for _, info := range infos {
		if !info.Original || info.Image || retained[info.Index] {
			continue
		}
		track := videoTracks[positions[info.Index]]
		if !track.hasTrackIndex {
			log.WithField("height", info.Height).Warn("Recording selector: masked original lacks track index; relying on process visibility masks")
			return "all"
		}
		// ID exclusions can also remove future encoder outputs with the same ID.
		parts = append(parts, "!"+strconv.Itoa(track.trackIndex))
	}
	return strings.Join(parts, ",")
}

// completedResultOutputs drops Helmsman-internal readiness outputs before a
// result reaches Foghorn.
func completedResultOutputs(outputs map[string]string) map[string]string {
	if _, ok := outputs[processingSourceVideoTracksOutput]; !ok {
		return outputs
	}
	out := make(map[string]string, len(outputs))
	for k, v := range outputs {
		if k != processingSourceVideoTracksOutput {
			out[k] = v
		}
	}
	return out
}
