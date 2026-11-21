package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

type processingProtection struct {
	JobID   string `json:"job_id"`
	Bytes   uint64 `json:"bytes"`
	Version uint64 `json:"version"`
}

var processingProtectionState struct {
	sync.RWMutex
	path      string
	value     processingProtection
	unknown   bool
	persisted bool
}

// LoadProcessingMemoryReservation restores protection before source work starts.
// Invalid state stays conservative until an authoritative control snapshot arrives.
func LoadProcessingMemoryReservation(stateDir string) error {
	if stateDir == "" {
		return errors.New("processing reservation requires a state directory")
	}
	path := filepath.Join(stateDir, "processing-reservation", "node.json")
	processingProtectionState.Lock()
	defer processingProtectionState.Unlock()
	if processingProtectionState.path == path {
		return nil
	}
	processingProtectionState.path = path
	processingProtectionState.value = processingProtection{}
	processingProtectionState.unknown = false
	processingProtectionState.persisted = true
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err == nil {
		err = json.Unmarshal(data, &processingProtectionState.value)
	}
	if err != nil {
		processingProtectionState.unknown = true
		processingProtectionState.persisted = false
		return err
	}
	if processingProtectionState.value.Bytes > 0 && processingProtectionState.value.JobID == "" {
		processingProtectionState.unknown = true
		return errors.New("processing reservation lacks its job identity")
	}
	return nil
}

// ApplyProcessingMemoryReservation fences delayed updates within a connection.
// Its first authoritative snapshot may reset a cursor restored from an older DB.
func ApplyProcessingMemoryReservation(stateDir string, update *ipcpb.ProcessingMemoryReservation, initial bool) (bool, error) {
	if update == nil {
		return false, nil
	}
	if stateDir == "" {
		return false, errors.New("processing reservation requires a state directory")
	}
	// The authoritative value repairs any unreadable prior file; persistence
	// below still reports a write failure while keeping runtime protection.
	if err := LoadProcessingMemoryReservation(stateDir); err != nil && !initial {
		return false, err
	}

	if update.GetBytes() > 0 && update.GetJobId() == "" {
		return false, fmt.Errorf("processing reservation lacks its job identity")
	}
	value := processingProtection{JobID: update.GetJobId(), Bytes: update.GetBytes(), Version: update.GetVersion()}
	processingProtectionState.Lock()
	defer processingProtectionState.Unlock()
	current := processingProtectionState.value
	if !initial && value.Version < current.Version {
		return false, nil
	}
	if !initial && value.Version == current.Version && value != current {
		return false, fmt.Errorf("conflicting processing reservation revision")
	}
	if value == current && processingProtectionState.persisted && !processingProtectionState.unknown {
		return true, nil
	}
	processingProtectionState.value = value
	processingProtectionState.unknown = false
	processingProtectionState.persisted = false
	if err := persistProcessingProtection(processingProtectionState.path, value); err != nil {
		return true, err
	}
	processingProtectionState.persisted = true
	return true, nil
}

func ProcessingProtectedMemory(jobID string) uint64 {
	processingProtectionState.RLock()
	defer processingProtectionState.RUnlock()
	if processingProtectionState.unknown {
		return math.MaxUint64
	}
	if jobID != "" && processingProtectionState.value.JobID == jobID {
		return 0
	}
	return processingProtectionState.value.Bytes
}

func persistProcessingProtection(path string, value processingProtection) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".protection-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if err = file.Chmod(0o600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return err
	}
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}
