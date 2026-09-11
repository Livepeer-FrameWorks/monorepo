package control

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"golang.org/x/sys/unix"
	"google.golang.org/protobuf/proto"
)

type managedPlacementFence struct {
	Schema         int    `json:"schema"`
	NodeID         string `json:"node_id"`
	ClusterID      string `json:"cluster_id"`
	TenantID       string `json:"tenant_id"`
	StreamID       string `json:"stream_id"`
	Name           string `json:"name"`
	ObjectVersion  int64  `json:"object_version"`
	TenantVersion  int64  `json:"tenant_version"`
	PolicyRevision uint64 `json:"policy_revision"`
	ParentRevision uint64 `json:"parent_revision"`
	PolicyDigest   string `json:"policy_digest"`
	CommandDigest  []byte `json:"command_digest"`
	Retired        bool   `json:"retired,omitempty"`
	Admission      []byte `json:"admission,omitempty"`
	ConfigDigest   []byte `json:"config_digest,omitempty"`
}

// acquireManagedPlacement persists the high water before media work. Its lock
// spans dispatch so overlapping handlers cannot send an older command last.
// Failure retains the fence; no unfenced rollback or source deletion is safe.
func acquireManagedPlacement(stateDir, nodeID string, command *ipcpb.ApplyManagedStream, now time.Time) (func(), error) {
	if command == nil || command.GetName() == "" {
		return nil, errors.New("managed command identity is missing")
	}
	a := command.GetPlacementAdmission()
	if a != nil {
		if err := placement.ValidateManagedCommand(command, nodeID, now); err != nil {
			return nil, err
		}
		for _, tag := range command.GetTags() {
			if (strings.HasPrefix(tag, managedStreamIDTagPrefix) && tag != managedStreamIDTagPrefix+command.GetStreamId()) ||
				(strings.HasPrefix(tag, "ingest:") && tag != "ingest:"+command.GetIngestMode()) {
				return nil, errors.New("managed command tags contradict recovery identity")
			}
		}
	}
	if stateDir == "" {
		if a != nil {
			return nil, errors.New("managed placement state directory is unavailable")
		}
		return func() {}, nil
	}
	locked, err := lockManagedPlacementFence(stateDir, command.GetName())
	if err != nil {
		return nil, err
	}
	retained := false
	defer func() {
		if !retained {
			locked.release()
		}
	}()
	if previous := locked.previous; previous != nil {
		if a == nil {
			return nil, errors.New("managed placement cannot downgrade to an unbound command")
		}
		if previous.NodeID != nodeID || previous.ClusterID != a.GetTargetClusterId() || previous.Name != command.GetName() || previous.TenantID != command.GetTenantId() || previous.StreamID != command.GetStreamId() {
			return nil, errors.New("managed placement fence identity differs")
		}
		if a.GetObjectAuthorityVersion() < previous.ObjectVersion || a.GetTenantAuthorityVersion() < previous.TenantVersion || a.GetPolicyRevision() < previous.PolicyRevision || a.GetParentRevision() < previous.ParentRevision {
			return nil, errors.New("managed placement authority regressed")
		}
		if previous.Retired && a.GetObjectAuthorityVersion() == previous.ObjectVersion {
			return nil, errors.New("managed placement authority has been retired")
		}
		if a.GetObjectAuthorityVersion() == previous.ObjectVersion && (!bytes.Equal(a.GetCommandSha256(), previous.CommandDigest) || a.GetPolicyDigest() != previous.PolicyDigest || a.GetPolicyRevision() != previous.PolicyRevision || a.GetParentRevision() != previous.ParentRevision) {
			return nil, errors.New("managed placement changed within an object authority version")
		}
	}
	if a != nil {
		encodedAdmission, encodeErr := proto.Marshal(a)
		if encodeErr != nil {
			return nil, encodeErr
		}
		configurationDigest, digestErr := managedConfigDigest(managedSnapshotFromCommand(command))
		if digestErr != nil {
			return nil, digestErr
		}
		fence := managedPlacementFence{Schema: 1, NodeID: nodeID, ClusterID: a.GetTargetClusterId(), TenantID: command.GetTenantId(), StreamID: command.GetStreamId(), Name: command.GetName(),
			ObjectVersion: a.GetObjectAuthorityVersion(), TenantVersion: a.GetTenantAuthorityVersion(), PolicyRevision: a.GetPolicyRevision(), ParentRevision: a.GetParentRevision(),
			PolicyDigest: a.GetPolicyDigest(), CommandDigest: a.GetCommandSha256(), Admission: encodedAdmission, ConfigDigest: configurationDigest}
		body, err := json.Marshal(fence)
		if err != nil {
			return nil, err
		}
		if err := writeManagedPlacementFence(locked.directory, locked.path, body); err != nil {
			return nil, err
		}
	}
	retained = true
	return locked.release, nil
}

type lockedManagedPlacementFence struct {
	directory, path string
	previous        *managedPlacementFence
	release         func()
}

func lockManagedPlacementFence(stateDir, name string) (*lockedManagedPlacementFence, error) {
	directory := filepath.Join(stateDir, "managed-placement")
	if _, err := mkdirAllDurable(directory); err != nil {
		return nil, err
	}
	key := sha256.Sum256([]byte(name))
	path := filepath.Join(directory, hex.EncodeToString(key[:])+".json")
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, errors.New("managed command is already in progress")
	}
	locked := &lockedManagedPlacementFence{directory: directory, path: path, release: func() { _ = lock.Close() }}
	retained := false
	defer func() {
		if !retained {
			locked.release()
		}
	}()
	file, readErr := os.Open(path)
	if errors.Is(readErr, os.ErrNotExist) {
		retained = true
		return locked, nil
	}
	if readErr != nil {
		return nil, readErr
	}
	defer file.Close()
	encoded, readErr := io.ReadAll(io.LimitReader(file, 8193))
	if readErr != nil {
		return nil, readErr
	}
	var previous managedPlacementFence
	if len(encoded) > 8192 || json.Unmarshal(encoded, &previous) != nil || previous.ObjectVersion <= 0 || previous.TenantVersion <= 0 || len(previous.CommandDigest) != sha256.Size || previous.PolicyDigest == "" {
		return nil, errors.New("managed placement fence is corrupt")
	}
	validFormat := (previous.Schema == 1 && !previous.Retired) || (previous.Schema == 2 && previous.Retired)
	if !validFormat {
		return nil, errors.New("managed placement fence format is unsupported")
	}
	locked.previous = &previous
	retained = true
	return locked, nil
}

func acquireManagedRetraction(stateDir, nodeID string, command *ipcpb.RetractManagedStream, now time.Time) (func(), error) {
	if command == nil || command.GetName() == "" {
		return nil, errors.New("managed retraction identity is missing")
	}
	r := command.GetPlacementRetraction()
	if r != nil {
		if err := placement.ValidateManagedRetraction(command, nodeID, now); err != nil {
			return nil, err
		}
	}
	if stateDir == "" {
		if r != nil {
			return nil, errors.New("managed placement state directory is unavailable")
		}
		return func() {}, nil
	}
	locked, err := lockManagedPlacementFence(stateDir, command.GetName())
	if err != nil {
		return nil, err
	}
	retained := false
	defer func() {
		if !retained {
			locked.release()
		}
	}()
	if previous := locked.previous; previous != nil {
		if r == nil || previous.NodeID != nodeID || previous.ClusterID != r.GetTargetClusterId() || previous.TenantID != r.GetTenantId() || previous.StreamID != command.GetStreamId() || previous.Name != command.GetName() ||
			previous.ObjectVersion != r.GetObjectAuthorityVersion() || previous.TenantVersion != r.GetTenantAuthorityVersion() || previous.PolicyRevision != r.GetPolicyRevision() || previous.ParentRevision != r.GetParentRevision() ||
			previous.PolicyDigest != r.GetPolicyDigest() || !bytes.Equal(previous.CommandDigest, r.GetCommandSha256()) {
			return nil, errors.New("managed retraction does not match current command")
		}
	}
	if r != nil {
		fence := managedPlacementFence{Schema: 2, NodeID: nodeID, ClusterID: r.GetTargetClusterId(), TenantID: r.GetTenantId(), StreamID: command.GetStreamId(), Name: command.GetName(),
			ObjectVersion: r.GetObjectAuthorityVersion(), TenantVersion: r.GetTenantAuthorityVersion(), PolicyRevision: r.GetPolicyRevision(), ParentRevision: r.GetParentRevision(),
			PolicyDigest: r.GetPolicyDigest(), CommandDigest: r.GetCommandSha256(), Retired: true}
		if previous := locked.previous; previous != nil {
			fence.Admission, fence.ConfigDigest = previous.Admission, previous.ConfigDigest
		}
		body, err := json.Marshal(fence)
		if err != nil {
			return nil, err
		}
		if err := writeManagedPlacementFence(locked.directory, locked.path, body); err != nil {
			return nil, err
		}
	}
	retained = true
	return locked.release, nil
}

func writeManagedPlacementFence(directory, path string, body []byte) error {
	if len(body) > 8192 {
		return errors.New("managed placement fence exceeds read bound")
	}
	temporary, createErr := os.CreateTemp(directory, ".fence-*")
	if createErr != nil {
		return createErr
	}
	defer func() { _ = temporary.Close(); _ = os.Remove(temporary.Name()) }()
	if _, err := temporary.Write(body); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary.Name(), path); err != nil {
		return err
	}
	folder, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer folder.Close()
	return folder.Sync()
}

// The digest covers the concrete fields written to Mist, including normalized
// ownership tags. It records no source text and is not a first-media receipt.
func managedConfigDigest(s managedStreamLocalSnapshot) ([]byte, error) {
	encoded, err := json.Marshal([]any{s.source, s.alwaysOn, s.realtime, s.stopSessions, s.tags, s.ingestMode, s.streamID})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(append([]byte("frameworks/managed-config/v1\x00"), encoded...))
	return digest[:], nil
}

func recoverManagedPlacement(stateDir, nodeID, name string, snapshot managedStreamLocalSnapshot) (managedStreamLocalSnapshot, error) {
	marked := slices.Contains(snapshot.tags, managedStreamPlacementTag)
	if stateDir == "" {
		if marked {
			return managedStreamLocalSnapshot{}, errors.New("managed placement recovery state is unavailable")
		}
		return snapshot, nil
	}
	locked, err := lockManagedPlacementFence(stateDir, name)
	if err != nil {
		return managedStreamLocalSnapshot{}, err
	}
	defer locked.release()
	fence := locked.previous
	if fence == nil && !marked {
		return snapshot, nil
	}
	configurationDigest, digestErr := managedConfigDigest(snapshot)
	if digestErr != nil {
		return managedStreamLocalSnapshot{}, digestErr
	}
	if fence == nil || fence.NodeID != nodeID || fence.Name != name || fence.StreamID != snapshot.streamID || len(fence.Admission) == 0 || len(fence.ConfigDigest) != sha256.Size || !bytes.Equal(fence.ConfigDigest, configurationDigest) {
		return managedStreamLocalSnapshot{}, errors.New("managed placement recovery differs from durable configuration")
	}
	var admission ipcpb.ManagedStreamAdmission
	if proto.Unmarshal(fence.Admission, &admission) != nil || admission.GetSchemaVersion() != 1 ||
		admission.GetTargetNodeId() != fence.NodeID || admission.GetTargetClusterId() != fence.ClusterID ||
		admission.GetObjectAuthorityVersion() != fence.ObjectVersion || admission.GetTenantAuthorityVersion() != fence.TenantVersion ||
		admission.GetPolicyRevision() != fence.PolicyRevision || admission.GetParentRevision() != fence.ParentRevision ||
		admission.GetPolicyDigest() != fence.PolicyDigest || !bytes.Equal(admission.GetCommandSha256(), fence.CommandDigest) {
		return managedStreamLocalSnapshot{}, errors.New("managed placement recovery admission differs from durable authority")
	}
	issued := admission.GetIssuedAt().AsTime()
	retirement := &ipcpb.RetractManagedStream{Name: name, StreamId: fence.StreamID, PlacementRetraction: &ipcpb.ManagedStreamRetraction{
		TargetNodeId: fence.NodeID, TargetClusterId: fence.ClusterID, TenantId: fence.TenantID, ObjectAuthorityVersion: fence.ObjectVersion, TenantAuthorityVersion: fence.TenantVersion,
		PolicyRevision: fence.PolicyRevision, ParentRevision: fence.ParentRevision, PolicyDigest: fence.PolicyDigest, CommandSha256: fence.CommandDigest,
		IssuedAt: admission.GetIssuedAt(), ExpiresAt: admission.GetExpiresAt(),
	}}
	if issued.After(time.Now().Add(time.Second)) || placement.ValidateManagedRetraction(retirement, nodeID, issued) != nil {
		return managedStreamLocalSnapshot{}, errors.New("managed placement recovery authority is malformed")
	}
	// Expired admissions remain cleanup identity, not permission to dispatch.
	snapshot.admission, snapshot.tenantID, snapshot.retired = &admission, fence.TenantID, fence.Retired
	return snapshot, nil
}
