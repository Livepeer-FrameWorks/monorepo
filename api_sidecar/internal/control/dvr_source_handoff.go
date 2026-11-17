package control

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/streamident"
	"github.com/google/uuid"
)

type dvrPushInventoryClient interface {
	GetPushSnapshot(context.Context) (mist.PushSnapshot, error)
}

// This descriptor is fsynced before either stopping the old writer or opening
// the replacement. It survives a lost command acknowledgement or sidecar restart.
type dvrSourceHandoff struct {
	OldRuntime           string `json:"old_runtime"`
	OldURL               string `json:"old_url,omitempty"`
	OldTarget            string `json:"old_target"`
	OldWriterID          int    `json:"old_writer_id"`
	ControllerID         string `json:"controller_id"`
	OldEnded             bool   `json:"old_ended"`
	OldConfirmedAtMillis int64  `json:"old_confirmed_at_millis"`
	NewIssued            bool   `json:"new_issued"`
	NewIssuerID          string `json:"new_issuer_id"`
	NewOutcomeUnknown    bool   `json:"new_outcome_unknown,omitempty"`
	NewIssuedAtMillis    int64  `json:"new_issued_at_millis"`
	NewEnded             bool   `json:"new_ended"`
	Cancelled            bool   `json:"cancelled"`
	Interrupted          bool   `json:"interrupted"`
	NewRuntime           string `json:"new_runtime"`
	NewURL               string `json:"new_url,omitempty"`
	SegmentDuration      int32  `json:"segment_duration"`
	WindowSeconds        int32  `json:"window_seconds"`
	MaxEntries           int32  `json:"max_entries"`
}

func dvrMetaForJob(job *DVRJob) dvrJobMeta {
	return dvrJobMeta{InternalName: job.InternalName, SourceRuntimeName: job.SourceRuntimeName, SourceURL: job.SourceURL,
		SegmentDuration: job.Config.GetSegmentDuration(), DvrWindowSeconds: job.Config.GetDvrWindowSeconds(), MaxEntries: job.Config.GetMaxEntries(),
		WriterID: job.PushID, WriterTarget: job.TargetURI, WriterControllerID: job.writerControllerID, WriterConfirmedAtMillis: job.writerConfirmedAtMillis, Handoff: job.handoff}
}

// Called with the job unpublished or dm.mutex held. Failure retains the writer;
// a handoff can obtain its proof later while the writer remains observable.
func (dm *DVRManager) persistDVRWriterProof(job *DVRJob) {
	client, ok := dm.mistClient.(dvrPushInventoryClient)
	if !ok || job.PushID <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), dvrWriterExitWait)
	defer cancel()
	snapshot, err := client.GetPushSnapshot(ctx)
	if err != nil {
		job.Logger.WithError(err).Warn("Recording writer identity is not yet durable")
		return
	}
	for _, push := range snapshot.Pushes {
		if push.ID == job.PushID && push.StreamName == job.StreamName && push.TargetURI == job.TargetURI {
			job.writerControllerID = snapshot.ControllerID
			job.writerConfirmedAtMillis = time.Now().UnixMilli()
			if err := dm.persistJobMeta(job.OutputDir, dvrMetaForJob(job)); err != nil {
				job.Logger.WithError(err).Warn("Recording writer identity could not be persisted")
			}
			return
		}
	}
}

func (dm *DVRManager) RebindRecording(req *ipcpb.DVRStartRequest, send func(*ipcpb.ControlMessage)) error {
	old, oldOK := streamident.ParseRuntimeBinding(req.GetPreviousSourceRuntimeName())
	next, nextOK := streamident.ParseRuntimeBinding(req.GetSourceRuntimeName())
	if !oldOK || !nextOK || old.InternalName != req.GetInternalName() || next.InternalName != old.InternalName || old.Role != streamident.RuntimePublisher || next.Role != old.Role || old.ExecutionID == next.ExecutionID {
		return errors.New("recording handoff requires two distinct publisher bindings for one stream")
	}
	dm.mutex.Lock()
	if dm.handoffIssuerID == "" {
		dm.handoffIssuerID = uuid.NewString()
	}
	job := dm.jobs[req.GetDvrHash()]
	dm.mutex.Unlock()
	if job == nil {
		dir := localDVRDirectory{streamID: req.GetStreamId(), dvrHash: req.GetDvrHash()}
		dir.outputDir = filepath.Join(dm.storagePath, "dvr", dir.streamID, dir.dvrHash)
		dir.manifestPath = filepath.Join(dir.outputDir, dir.dvrHash+".m3u8")
		meta, ok := readDVRJobMeta(dir.outputDir)
		if !ok || meta.InternalName != req.GetInternalName() {
			return errors.New("recording handoff has no durable local identity")
		}
		if meta.Handoff != nil {
			dm.restoreDVRHandoffJob(dir, meta)
		} else if meta.SourceRuntimeName == req.GetPreviousSourceRuntimeName() {
			dm.restoreDVRHandoffJob(dir, meta)
		} else if meta.SourceRuntimeName == req.GetSourceRuntimeName() {
			pushes, err := dm.mistClient.PushList()
			if err != nil {
				return err
			}
			dm.recoverOneDVRDir(dir, pushes, dm.logger)
		}
		dm.mutex.RLock()
		job = dm.jobs[req.GetDvrHash()]
		dm.mutex.RUnlock()
		if job == nil {
			return errors.New("recording handoff cannot recover its writer")
		}
	}
	job.handoffMu.Lock()
	defer job.handoffMu.Unlock()
	dm.mutex.Lock()
	if dm.jobs[job.DVRHash] == job && job.handoff != nil && job.handoff.Interrupted && job.Status == "finalizing" {
		job.SendFunc = send
		dm.mutex.Unlock()
		dm.sendCompletion(job, "failed", "Recording source handoff was interrupted before its replacement writer was confirmed", true)
		return nil
	}
	if dm.jobs[job.DVRHash] != job || job.InternalName != req.GetInternalName() || job.Status == "finalizing" || job.Status == "failed" {
		dm.mutex.Unlock()
		return nil
	}
	job.SendFunc = send
	if job.handoff == nil && job.SourceRuntimeName == req.GetSourceRuntimeName() {
		dm.mutex.Unlock()
		return nil
	}
	if job.handoff != nil && (job.handoff.OldRuntime != req.GetPreviousSourceRuntimeName() || job.handoff.NewRuntime != req.GetSourceRuntimeName()) {
		dm.mutex.Unlock()
		return errors.New("recording already has a different pending handoff")
	}
	if job.handoff == nil {
		if job.SourceRuntimeName != req.GetPreviousSourceRuntimeName() {
			dm.mutex.Unlock()
			return nil
		}
		meta, ok := readDVRJobMeta(job.OutputDir)
		if !ok {
			dm.mutex.Unlock()
			return errors.New("recording handoff lost its durable writer identity")
		}
		job.handoff = &dvrSourceHandoff{OldRuntime: job.SourceRuntimeName, OldURL: job.SourceURL, OldTarget: job.TargetURI,
			OldWriterID: job.PushID, ControllerID: meta.WriterControllerID, OldEnded: meta.WriterEnded,
			OldConfirmedAtMillis: meta.WriterConfirmedAtMillis,
			NewRuntime:           req.GetSourceRuntimeName(), NewURL: req.GetSourceBaseUrl(), SegmentDuration: req.GetConfig().GetSegmentDuration(),
			WindowSeconds: req.GetConfig().GetDvrWindowSeconds(), MaxEntries: req.GetConfig().GetMaxEntries()}
		job.Status = "rebinding"
		job.pushGeneration++
		if err := dm.persistJobMeta(job.OutputDir, dvrMetaForJob(job)); err != nil {
			job.handoff = nil
			job.Status = "recording"
			dm.mutex.Unlock()
			return err
		}
	}
	dm.mutex.Unlock()
	return dm.advanceDVRSourceHandoff(job)
}

func (dm *DVRManager) reconcileDVRSourceHandoff(job *DVRJob) {
	if !job.handoffMu.TryLock() {
		return
	}
	defer job.handoffMu.Unlock()
	if err := dm.advanceDVRSourceHandoff(job); err != nil {
		job.Logger.WithError(err).WithField("dvr_hash", job.DVRHash).Warn("Recording source transition remains pending")
	}
}

// A completed stop request is insufficient: the observed writer must disappear
// from the same controller's complete inventory, or report its exact end.
func dvrOldWriterExited(handoff dvrSourceHandoff, snapshot mist.PushSnapshot) bool {
	if handoff.OldEnded {
		return true
	}
	if handoff.ControllerID == "" || snapshot.ControllerID != handoff.ControllerID || handoff.OldWriterID <= 0 {
		return false
	}
	for _, push := range snapshot.Pushes {
		if push.ID == handoff.OldWriterID || (push.StreamName == handoff.OldRuntime && push.TargetURI == handoff.OldTarget) {
			return false
		}
	}
	return true
}

func (dm *DVRManager) advanceDVRSourceHandoff(job *DVRJob) error {
	client, ok := dm.mistClient.(dvrPushInventoryClient)
	if !ok {
		return errors.New("native writer inventory does not support recording handoff")
	}
	ctx, cancel := context.WithTimeout(context.Background(), dvrWriterExitWait)
	defer cancel()
	for {
		dm.mutex.RLock()
		if dm.jobs[job.DVRHash] != job || job.handoff == nil || job.handoff.Cancelled || job.Status != "rebinding" {
			dm.mutex.RUnlock()
			return nil
		}
		handoff := *job.handoff
		dm.mutex.RUnlock()
		handoff.OldEnded = handoff.OldEnded || dvrWriterEndedOnDisk(job.OutputDir, handoff.OldRuntime, handoff.OldConfirmedAtMillis)
		handoff.NewEnded = handoff.NewEnded || dvrWriterEndedOnDisk(job.OutputDir, handoff.NewRuntime, handoff.NewIssuedAtMillis)
		snapshot, err := client.GetPushSnapshot(ctx)
		if err != nil {
			return err
		}
		if !handoff.OldEnded {
			for _, push := range snapshot.Pushes {
				if push.StreamName != handoff.OldRuntime || push.TargetURI != handoff.OldTarget {
					continue
				}
				handoff.OldWriterID, handoff.ControllerID = push.ID, snapshot.ControllerID
				if handoff.OldConfirmedAtMillis == 0 {
					handoff.OldConfirmedAtMillis = time.Now().UnixMilli()
				}
				dm.mutex.Lock()
				job.handoff = &handoff
				err = dm.persistJobMeta(job.OutputDir, dvrMetaForJob(job))
				dm.mutex.Unlock()
				if err != nil {
					return err
				}
				if err = dm.mistClient.PushStop(push.ID); err != nil {
					return err
				}
			}
			if !dvrOldWriterExited(handoff, snapshot) {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(dvrWriterExitPoll):
					continue
				}
			}
			handoff.OldEnded = true
			dm.mutex.Lock()
			job.handoff = &handoff
			err = dm.persistJobMeta(job.OutputDir, dvrMetaForJob(job))
			dm.mutex.Unlock()
			if err != nil {
				return err
			}
		}
		config := &ipcpb.DVRConfig{SegmentDuration: handoff.SegmentDuration, DvrWindowSeconds: handoff.WindowSeconds, MaxEntries: handoff.MaxEntries}
		target := dvrTargetURI(job.OutputDir, job.DVRHash, config, handoff.NewRuntime)
		if handoff.NewEnded {
			dm.mutex.Lock()
			job.SourceRuntimeName, job.SourceURL, job.StreamName, job.TargetURI = handoff.NewRuntime, handoff.NewURL, handoff.NewRuntime, target
			meta := dvrMetaForJob(job)
			meta.Handoff = nil
			meta.WriterEnded = true
			err = dm.persistJobMeta(job.OutputDir, meta)
			if err == nil {
				job.handoff = nil
				job.Status = "completed"
				delete(dm.jobs, job.DVRHash)
			}
			dm.mutex.Unlock()
			if err == nil {
				dm.syncNewSegments(job)
				dm.sendCompletion(job, "completed", "")
			}
			return err
		}
		if handoff.NewIssued {
			push, found := findExactDVRPush(snapshot.Pushes, handoff.NewRuntime, target, job.DVRHash)
			if !found {
				dm.mutex.Lock()
				if dm.handoffIssuerID == "" {
					dm.handoffIssuerID = uuid.NewString()
				}
				uncertain := handoff.NewIssuerID != dm.handoffIssuerID || handoff.NewOutcomeUnknown
				if uncertain {
					handoff.Cancelled = true
					handoff.Interrupted = true
					meta := dvrMetaForJob(job)
					meta.Handoff = &handoff
					err = dm.persistJobMeta(job.OutputDir, meta)
					if err == nil {
						job.handoff = &handoff
						job.Status = "finalizing"
					}
				}
				dm.mutex.Unlock()
				if uncertain && err == nil {
					dm.syncNewSegments(job)
					dm.sendCompletion(job, "failed", "Recording source handoff was interrupted before its replacement writer was confirmed", true)
					return nil
				}
				if err != nil {
					return err
				}
				return errors.New("replacement recording writer accepted but not yet confirmed")
			}
			dm.mutex.Lock()
			job.SourceRuntimeName, job.SourceURL, job.StreamName, job.TargetURI, job.PushID = handoff.NewRuntime, handoff.NewURL, handoff.NewRuntime, target, push.ID
			job.writerControllerID = snapshot.ControllerID
			job.writerConfirmedAtMillis = time.Now().UnixMilli()
			meta := dvrMetaForJob(job)
			meta.Handoff = nil
			err = dm.persistJobMeta(job.OutputDir, meta)
			if err == nil {
				job.handoff = nil
				job.Status = "recording"
				job.pushGeneration++
			}
			dm.mutex.Unlock()
			if err == nil {
				ClearDVRSourceOverride(handoff.OldRuntime)
				dm.updateProgress(job)
			}
			return err
		}
		handoff.NewIssued = true
		handoff.NewIssuedAtMillis = time.Now().UnixMilli()
		dm.mutex.Lock()
		if dm.handoffIssuerID == "" {
			dm.handoffIssuerID = uuid.NewString()
		}
		handoff.NewIssuerID = dm.handoffIssuerID
		meta := dvrMetaForJob(job)
		meta.Handoff = &handoff
		err = dm.persistJobMeta(job.OutputDir, meta)
		if err == nil {
			job.handoff = &handoff
		}
		dm.mutex.Unlock()
		if err != nil {
			return err
		}
		if handoff.NewURL != "" {
			RegisterDVRSourceOverride(handoff.NewRuntime, handoff.NewURL)
		}
		// Exactly one issuance. A lost response is reconciled by the persisted
		// identity; it never opens a second writer against the manifest.
		if err := dm.mistClient.PushStart(handoff.NewRuntime, target); err != nil {
			if errors.Is(err, mist.ErrMistAmbiguous) {
				handoff.NewOutcomeUnknown = true
				dm.mutex.Lock()
				job.handoff = &handoff
				meta := dvrMetaForJob(job)
				persistErr := dm.persistJobMeta(job.OutputDir, meta)
				dm.mutex.Unlock()
				if persistErr != nil {
					return errors.Join(err, persistErr)
				}
			} else {
				handoff.NewIssued = false
				dm.mutex.Lock()
				meta := dvrMetaForJob(job)
				meta.Handoff = &handoff
				if persistErr := dm.persistJobMeta(job.OutputDir, meta); persistErr == nil {
					job.handoff = &handoff
				}
				dm.mutex.Unlock()
			}
			return fmt.Errorf("replacement recording writer awaiting reconciliation: %w", err)
		}
	}
}

func (dm *DVRManager) restoreDVRHandoffJob(dir localDVRDirectory, meta dvrJobMeta) {
	dm.mutex.Lock()
	defer dm.mutex.Unlock()
	if dm.jobs[dir.dvrHash] != nil {
		return
	}
	job := &DVRJob{DVRHash: dir.dvrHash, InternalName: meta.InternalName, SourceRuntimeName: meta.SourceRuntimeName,
		SourceURL: meta.SourceURL, StreamName: meta.SourceRuntimeName, PushID: meta.WriterID, TargetURI: meta.WriterTarget,
		writerControllerID: meta.WriterControllerID, writerConfirmedAtMillis: meta.WriterConfirmedAtMillis, handoff: meta.Handoff, Config: meta.dvrConfig(), OutputDir: dir.outputDir,
		ManifestPath: dir.manifestPath, StartTime: time.Now().Add(-dvrManifestDuration(dir.manifestPath)), Status: "rebinding",
		SendFunc: func(msg *ipcpb.ControlMessage) {
			if sendErr := sendControlMessage(msg); sendErr != nil && !durableControlWasPersisted(sendErr) {
				dm.logger.WithError(sendErr).WithField("dvr_hash", dir.dvrHash).Warn("Failed to persist recovered recording report")
			}
		}, Logger: dm.logger,
		MaxRetries: MaxDVRRetries, SegmentCount: dvrManifestSegmentCount(dir.manifestPath), TotalSizeBytes: dvrDirectorySize(dir.outputDir),
		SyncedSegments: make(map[string]bool), UnmarkedSegments: make(map[string]uint64)}
	if job.TargetURI == "" {
		job.TargetURI = dvrTargetURI(job.OutputDir, job.DVRHash, job.Config, job.SourceRuntimeName)
	}
	dm.jobs[dir.dvrHash] = job
	if job.handoff != nil && job.handoff.Cancelled {
		job.Status = "finalizing"
	}
	dm.startMonitor(job)
}

// Writer-end callbacks can arrive while StartRecording holds the manager lock
// and waits for Mist. Persist their proof independently of the in-memory job.
func (dm *DVRManager) persistEndedDVRWriter(stream string, at time.Time, targets []string) {
	if _, bound := streamident.ParseRuntimeBinding(stream); !bound || at.UnixMilli() <= 0 {
		return
	}
	seen := make(map[string]bool)
	for _, target := range targets {
		path := filepath.Clean(strings.SplitN(target, "?", 2)[0])
		relative, err := filepath.Rel(filepath.Clean(dm.storagePath), path)
		if err != nil {
			continue
		}
		parts := strings.Split(relative, string(filepath.Separator))
		if len(parts) < 5 || parts[0] != "dvr" || parts[1] == "" || parts[2] == "" || parts[3] != "segments" {
			continue
		}
		dir := filepath.Join(dm.storagePath, "dvr", parts[1], parts[2])
		if seen[dir] {
			continue
		}
		seen[dir] = true
		meta, ok := readDVRJobMeta(dir)
		if !ok || (meta.SourceRuntimeName != stream && (meta.Handoff == nil || meta.Handoff.NewRuntime != stream)) {
			continue
		}
		if err := writeDVRMetadataFile(dir, dvrWriterEndFile(stream), []byte(strconv.FormatInt(at.UnixMilli(), 10))); err != nil {
			dm.logger.WithError(err).Warn("Recording writer end proof could not be persisted")
		}
	}
}

func dvrWriterEndFile(runtime string) string {
	return fmt.Sprintf(".writer-ended-%x", sha256.Sum256([]byte(runtime)))
}

func dvrWriterEndedOnDisk(dir, runtime string, afterMillis int64) bool {
	if afterMillis <= 0 {
		return false
	}
	raw, err := os.ReadFile(filepath.Join(dir, dvrWriterEndFile(runtime)))
	if err != nil {
		return false
	}
	ended, err := strconv.ParseInt(string(raw), 10, 64)
	return err == nil && ended >= afterMillis
}
