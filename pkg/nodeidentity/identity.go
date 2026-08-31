package nodeidentity

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	identityDirectory  = "node-identity"
	identityFile       = "identity.json"
	legacyDirectory    = ".frameworks"
	legacyIdentityFile = "node-identity-ed25519.seed"
	proofDomain        = "frameworks-node-registration-proof-v2"
	proofNonceSize     = 32
	MaxProofAge        = 2 * time.Minute
)

type LoadStatus string

const (
	LoadStatusLoaded          LoadStatus = "loaded"
	LoadStatusMigrated        LoadStatus = "migrated"
	LoadStatusCreated         LoadStatus = "created"
	LoadStatusRotated         LoadStatus = "rotated"
	LoadStatusRotationPending LoadStatus = "rotation_pending"
)

type identityRecord struct {
	Version             int    `json:"version"`
	NodeID              string `json:"node_id"`
	Seed                string `json:"seed_ed25519_base64"`
	RotationPending     bool   `json:"rotation_pending,omitempty"`
	RotationRequestHash string `json:"rotation_request_sha256,omitempty"`
}

// LoadOrCreatePrivateKey returns the node identity rooted in Helmsman's durable
// local storage. A missing or malformed storage path cannot provide a durable
// outage identity and is therefore rejected.
func LoadOrCreatePrivateKey(stateRoot, nodeID, legacyStorageRoot string, rotate bool, rotationRequest string) (ed25519.PrivateKey, LoadStatus, error) {
	stateRoot = strings.TrimSpace(stateRoot)
	nodeID = strings.TrimSpace(nodeID)
	if stateRoot == "" {
		return nil, "", errors.New("node identity requires HELMSMAN_STATE_DIR")
	}
	if nodeID == "" {
		return nil, "", errors.New("node identity requires NODE_ID")
	}
	directory := filepath.Join(stateRoot, identityDirectory)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, "", fmt.Errorf("create node identity directory: %w", err)
	}
	path := filepath.Join(directory, identityFile)
	requestHash, err := requestedRotationHash(rotate, rotationRequest)
	if err != nil {
		return nil, "", err
	}
	if key, record, err := readIdentityRecord(path, nodeID); err == nil {
		if cleanupErr := removeMatchingLegacyIdentity(legacyStorageRoot, key); cleanupErr != nil {
			return nil, "", cleanupErr
		}
		if record.RotationPending {
			if !rotate || record.RotationRequestHash == requestHash {
				return key, LoadStatusRotationPending, nil
			}
			return rotateIdentityRecord(path, nodeID, requestHash)
		}
		if rotate {
			if record.RotationRequestHash == requestHash {
				return key, LoadStatusLoaded, nil
			}
			return rotateIdentityRecord(path, nodeID, requestHash)
		}
		return key, LoadStatusLoaded, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, "", err
	}

	legacyPath := filepath.Join(strings.TrimSpace(legacyStorageRoot), legacyDirectory, legacyIdentityFile)
	if strings.TrimSpace(legacyStorageRoot) != "" {
		if key, legacyErr := readLegacyPrivateKey(legacyPath); legacyErr == nil {
			if installErr := installIdentityRecord(path, nodeID, key.Seed(), false, "", false); installErr != nil {
				return nil, "", installErr
			}
			if cleanupErr := removeLegacyIdentity(legacyPath); cleanupErr != nil {
				return nil, "", cleanupErr
			}
			if rotate {
				return rotateIdentityRecord(path, nodeID, requestHash)
			}
			return key, LoadStatusMigrated, nil
		} else if !errors.Is(legacyErr, os.ErrNotExist) {
			return nil, "", fmt.Errorf("read legacy node identity: %w", legacyErr)
		}
	}

	seed := make([]byte, ed25519.SeedSize)
	if _, err := io.ReadFull(rand.Reader, seed); err != nil {
		return nil, "", fmt.Errorf("generate node identity: %w", err)
	}
	if err := installIdentityRecord(path, nodeID, seed, rotate, requestHash, false); err != nil {
		if errors.Is(err, os.ErrExist) {
			key, _, readErr := readIdentityRecord(path, nodeID)
			return key, LoadStatusLoaded, readErr
		}
		return nil, "", err
	}
	if rotate {
		return ed25519.NewKeyFromSeed(seed), LoadStatusRotated, nil
	}
	return ed25519.NewKeyFromSeed(seed), LoadStatusCreated, nil
}

func rotateIdentityRecord(path, nodeID, requestHash string) (ed25519.PrivateKey, LoadStatus, error) {
	seed := make([]byte, ed25519.SeedSize)
	if _, err := io.ReadFull(rand.Reader, seed); err != nil {
		return nil, "", fmt.Errorf("generate replacement node identity: %w", err)
	}
	if err := installIdentityRecord(path, nodeID, seed, true, requestHash, true); err != nil {
		return nil, "", err
	}
	return ed25519.NewKeyFromSeed(seed), LoadStatusRotated, nil
}

func installIdentityRecord(path, nodeID string, seed []byte, rotationPending bool, rotationRequestHash string, replace bool) error {
	record := identityRecord{
		Version: 2, NodeID: nodeID, Seed: base64.StdEncoding.EncodeToString(seed),
		RotationPending: rotationPending, RotationRequestHash: rotationRequestHash,
	}
	encoded, marshalErr := json.Marshal(record)
	if marshalErr != nil {
		return fmt.Errorf("encode node identity: %w", marshalErr)
	}
	directory := filepath.Dir(path)
	temp, createErr := os.CreateTemp(directory, ".node-identity-*")
	if createErr != nil {
		return fmt.Errorf("create node identity staging file: %w", createErr)
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if chmodErr := temp.Chmod(0o600); chmodErr != nil {
		_ = temp.Close()
		return fmt.Errorf("protect node identity staging file: %w", chmodErr)
	}
	if _, writeErr := temp.Write(encoded); writeErr != nil {
		_ = temp.Close()
		return fmt.Errorf("write node identity staging file: %w", writeErr)
	}
	if syncErr := temp.Sync(); syncErr != nil {
		_ = temp.Close()
		return fmt.Errorf("sync node identity staging file: %w", syncErr)
	}
	if closeErr := temp.Close(); closeErr != nil {
		return fmt.Errorf("close node identity staging file: %w", closeErr)
	}
	if replace {
		if renameErr := os.Rename(tempPath, path); renameErr != nil {
			return fmt.Errorf("replace node identity: %w", renameErr)
		}
	} else if linkErr := os.Link(tempPath, path); linkErr != nil {
		return fmt.Errorf("install node identity: %w", linkErr)
	}
	dir, openErr := os.Open(directory)
	if openErr != nil {
		return fmt.Errorf("open node identity directory: %w", openErr)
	}
	if syncErr := dir.Sync(); syncErr != nil {
		_ = dir.Close()
		return fmt.Errorf("sync node identity directory: %w", syncErr)
	}
	return dir.Close()
}

// CompleteRotation records that Foghorn accepted the replacement identity.
// The request hash is retained so a provisioned one-shot rotation flag cannot
// rotate the key again on every process restart.
func CompleteRotation(stateRoot, nodeID string) error {
	path := filepath.Join(strings.TrimSpace(stateRoot), identityDirectory, identityFile)
	key, record, err := readIdentityRecord(path, strings.TrimSpace(nodeID))
	if err != nil {
		return err
	}
	if !record.RotationPending {
		return nil
	}
	if err := installIdentityRecord(path, strings.TrimSpace(nodeID), key.Seed(), false, record.RotationRequestHash, true); err != nil {
		return fmt.Errorf("complete node identity rotation: %w", err)
	}
	return nil
}

func requestedRotationHash(rotate bool, request string) (string, error) {
	if !rotate {
		return "", nil
	}
	request = strings.TrimSpace(request)
	if request == "" {
		return "", errors.New("node identity rotation requires a non-empty request identity")
	}
	sum := sha256.Sum256([]byte(request))
	return hex.EncodeToString(sum[:]), nil
}

func readIdentityRecord(path, nodeID string) (ed25519.PrivateKey, identityRecord, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, identityRecord{}, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, identityRecord{}, errors.New("node identity seed permissions must not allow group or other access")
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return nil, identityRecord{}, fmt.Errorf("read node identity: %w", err)
	}
	var record identityRecord
	if unmarshalErr := json.Unmarshal(encoded, &record); unmarshalErr != nil {
		return nil, identityRecord{}, fmt.Errorf("decode node identity: %w", unmarshalErr)
	}
	if (record.Version != 1 && record.Version != 2) || strings.TrimSpace(record.NodeID) != nodeID {
		return nil, identityRecord{}, fmt.Errorf("node identity belongs to %q, expected %q", record.NodeID, nodeID)
	}
	seed, err := base64.StdEncoding.DecodeString(record.Seed)
	if err != nil {
		return nil, identityRecord{}, errors.New("node identity seed encoding is invalid")
	}
	if len(seed) != ed25519.SeedSize {
		return nil, identityRecord{}, errors.New("node identity seed has invalid length")
	}
	return ed25519.NewKeyFromSeed(seed), record, nil
}

func removeMatchingLegacyIdentity(legacyStorageRoot string, key ed25519.PrivateKey) error {
	if strings.TrimSpace(legacyStorageRoot) == "" {
		return nil
	}
	legacyPath := filepath.Join(strings.TrimSpace(legacyStorageRoot), legacyDirectory, legacyIdentityFile)
	legacyKey, err := readLegacyPrivateKey(legacyPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read legacy node identity for cleanup: %w", err)
	}
	if !bytes.Equal(legacyKey, key) {
		return errors.New("legacy node identity differs from the installed identity")
	}
	return removeLegacyIdentity(legacyPath)
}

func removeLegacyIdentity(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove migrated legacy node identity: %w", err)
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open legacy node identity directory: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync legacy node identity directory: %w", err)
	}
	return nil
}

func readLegacyPrivateKey(path string) (ed25519.PrivateKey, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("legacy node identity seed permissions must not allow group or other access")
	}
	seed, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(seed) != ed25519.SeedSize {
		return nil, errors.New("legacy node identity seed has invalid length")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// SignRegistration fills the proof fields that bind this connection's node id
// and stable fingerprint to possession of the persisted node private key.
func SignRegistration(register *ipcpb.Register, privateKey ed25519.PrivateKey, now time.Time) error {
	if register == nil {
		return errors.New("registration is missing")
	}
	if len(privateKey) != ed25519.PrivateKeySize {
		return errors.New("node identity private key has invalid length")
	}
	nonce := make([]byte, proofNonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return fmt.Errorf("generate node registration nonce: %w", err)
	}
	issuedAt := now.UTC()
	publicKey, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok {
		return errors.New("node identity private key did not yield an Ed25519 public key")
	}
	message, err := proofMessage(register.GetNodeId(), register.GetFingerprint(), publicKey, nonce, issuedAt, register.GetNodeIdentityRotationRequested())
	if err != nil {
		return err
	}
	register.NodeIdentityPublicKeyEd25519 = append([]byte(nil), publicKey...)
	register.NodeIdentityProofNonce = nonce
	register.NodeIdentityProofIssuedAt = timestamppb.New(issuedAt)
	register.NodeIdentityProofEd25519 = ed25519.Sign(privateKey, message)
	return nil
}

// VerifyRegistration verifies proof freshness and signature before any remote
// identity lookup or durable-admission database work is performed.
func VerifyRegistration(register *ipcpb.Register, now time.Time) error {
	if register == nil {
		return errors.New("registration is missing")
	}
	publicKey := register.GetNodeIdentityPublicKeyEd25519()
	nonce := register.GetNodeIdentityProofNonce()
	signature := register.GetNodeIdentityProofEd25519()
	issued := register.GetNodeIdentityProofIssuedAt()
	if len(publicKey) != ed25519.PublicKeySize || len(nonce) != proofNonceSize || len(signature) != ed25519.SignatureSize || issued == nil {
		return errors.New("node identity proof is incomplete")
	}
	if err := issued.CheckValid(); err != nil {
		return fmt.Errorf("node identity proof timestamp: %w", err)
	}
	issuedAt := issued.AsTime().UTC()
	if issuedAt.Before(now.UTC().Add(-MaxProofAge)) || issuedAt.After(now.UTC().Add(MaxProofAge)) {
		return errors.New("node identity proof is outside the freshness window")
	}
	message, err := proofMessage(register.GetNodeId(), register.GetFingerprint(), publicKey, nonce, issuedAt, register.GetNodeIdentityRotationRequested())
	if err != nil {
		return err
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKey), message, signature) {
		return errors.New("node identity proof signature is invalid")
	}
	return nil
}

func proofMessage(nodeID string, fingerprint *ipcpb.NodeFingerprint, publicKey, nonce []byte, issuedAt time.Time, rotate bool) ([]byte, error) {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return nil, errors.New("node identity proof requires a node id")
	}
	if fingerprint == nil {
		return nil, errors.New("node identity proof requires a fingerprint")
	}
	machineID, err := normalizedDigest(fingerprint.GetMachineIdSha256())
	if err != nil {
		return nil, fmt.Errorf("machine-id fingerprint: %w", err)
	}
	macs, err := normalizedDigest(fingerprint.GetMacsSha256())
	if err != nil {
		return nil, fmt.Errorf("MAC fingerprint: %w", err)
	}
	if machineID == "" && macs == "" {
		return nil, errors.New("node identity proof requires a stable fingerprint")
	}
	if len(publicKey) != ed25519.PublicKeySize || len(nonce) != proofNonceSize {
		return nil, errors.New("node identity proof key or nonce has invalid length")
	}
	var out bytes.Buffer
	for _, field := range [][]byte{
		[]byte(proofDomain), []byte(nodeID), []byte(machineID), []byte(macs), publicKey, nonce,
	} {
		if err := binary.Write(&out, binary.BigEndian, uint32(len(field))); err != nil {
			return nil, err
		}
		_, _ = out.Write(field)
	}
	if rotate {
		out.WriteByte(1)
	} else {
		out.WriteByte(0)
	}
	if err := binary.Write(&out, binary.BigEndian, issuedAt.Unix()); err != nil {
		return nil, err
	}
	if err := binary.Write(&out, binary.BigEndian, int32(issuedAt.Nanosecond())); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func normalizedDigest(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", nil
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return "", errors.New("must be a SHA-256 hex digest")
	}
	return value, nil
}
