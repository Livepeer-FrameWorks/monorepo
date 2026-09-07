package control

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/nodeidentity"
	"golang.org/x/sys/unix"
)

var acceptedEnrollments sync.Map
var directCredentialCleanupCompletions sync.Map
var finalizedEnrollmentCredentials sync.Map

type credentialCleanupDeferredError struct {
	cause error
}

func (e *credentialCleanupDeferredError) Error() string {
	return fmt.Sprintf("credential cleanup pending after direct scrub failed: %v", e.cause)
}

func (e *credentialCleanupDeferredError) Unwrap() error {
	return e.cause
}

const (
	containerStateDir       = "/data/state"
	containerTokenFile      = "/run/frameworks/.edge-enroll.env"
	containerRuntimeEnvFile = "/run/frameworks/.edge.env"
	helmsmanRuntimeUID      = uint32(1001)
)

func enrollmentReceiptPath(stateDir, nodeID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(nodeID)))
	return filepath.Join(stateDir, "enrollment", hex.EncodeToString(sum[:])+".accepted")
}

func enrollmentTokenForRegistration(stateDir, nodeID, configuredToken string, rotationRequested bool) string {
	token := strings.TrimSpace(configuredToken)
	if token == "" || rotationRequested {
		return token
	}
	key := strings.TrimSpace(stateDir) + "\x00" + strings.TrimSpace(nodeID)
	if _, ok := acceptedEnrollments.Load(key); ok {
		return ""
	}
	if dirFD, err := openEnrollmentDirectoryNoFollow(stateDir); err == nil {
		receiptName := filepath.Base(enrollmentReceiptPath(stateDir, nodeID))
		contents, meta, readErr := readRegularFileAt(dirFD, receiptName)
		_ = unix.Close(dirFD)
		if readErr == nil && strings.TrimSpace(string(contents)) == "accepted" && meta.uid == uint32(os.Geteuid()) {
			acceptedEnrollments.Store(key, struct{}{})
			return ""
		}
	}
	return token
}

func markEnrollmentAccepted(stateDir, nodeID string) error {
	stateDir = strings.TrimSpace(stateDir)
	nodeID = strings.TrimSpace(nodeID)
	if stateDir == "" || nodeID == "" {
		return fmt.Errorf("state directory and node ID are required")
	}
	key := stateDir + "\x00" + nodeID
	dirFD, err := openOrCreateEnrollmentDirectoryNoFollow(stateDir)
	if err != nil {
		return err
	}
	defer unix.Close(dirFD) //nolint:errcheck
	receiptName := filepath.Base(enrollmentReceiptPath(stateDir, nodeID))
	fd, err := unix.Openat(dirFD, receiptName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if errors.Is(err, unix.EEXIST) {
		contents, meta, readErr := readRegularFileAt(dirFD, receiptName)
		if readErr != nil {
			return fmt.Errorf("read existing enrollment receipt: %w", readErr)
		}
		if strings.TrimSpace(string(contents)) != "accepted" || meta.uid != uint32(os.Geteuid()) {
			return fmt.Errorf("existing enrollment receipt is not trusted")
		}
		acceptedEnrollments.Store(key, struct{}{})
		return nil
	}
	if err != nil {
		return fmt.Errorf("open enrollment receipt: %w", err)
	}
	file := os.NewFile(uintptr(fd), receiptName)
	if file == nil {
		_ = unix.Close(fd)
		return fmt.Errorf("open enrollment receipt")
	}
	removeIncomplete := true
	defer func() {
		if removeIncomplete {
			if cleanupErr := unix.Unlinkat(dirFD, receiptName, 0); cleanupErr != nil && !errors.Is(cleanupErr, unix.ENOENT) {
				CredentialCleanupOutcomes.WithLabelValues("receipt_cleanup_error").Inc()
			}
		}
	}()
	if _, err = file.WriteString("accepted\n"); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("persist enrollment receipt: %w", err)
	}
	if err := unix.Fsync(dirFD); err != nil {
		return fmt.Errorf("sync enrollment receipt directory: %w", err)
	}
	removeIncomplete = false
	acceptedEnrollments.Store(key, struct{}{})
	return nil
}

func clearConsumedEnrollmentCredentials(tokenFile, runtimeEnvFile string) error {
	runtimeEnvFile = strings.TrimSpace(runtimeEnvFile)
	if runtimeEnvFile == "" {
		return fmt.Errorf("runtime environment file is required for credential cleanup")
	}
	tokenFile = strings.TrimSpace(tokenFile)
	if tokenFile == "" {
		return fmt.Errorf("enrollment token file is required for credential cleanup")
	}
	if err := rewriteEnvValue(runtimeEnvFile, "HELMSMAN_ROTATE_NODE_IDENTITY", ""); err != nil {
		return fmt.Errorf("clear identity rotation flag: %w", err)
	}
	if err := rewriteEnvValue(tokenFile, "EDGE_ENROLLMENT_TOKEN", ""); err != nil {
		return fmt.Errorf("clear enrollment token: %w", err)
	}
	return nil
}

func credentialCleanupRequestPath(stateDir string) string {
	return filepath.Join(strings.TrimSpace(stateDir), "enrollment", "credential-cleanup.request")
}

func credentialCleanupCompletionPath(stateDir string) string {
	return filepath.Join(strings.TrimSpace(stateDir), "enrollment", "credential-cleanup.completed")
}

func requestCredentialCleanup(stateDir, nodeID string) error {
	dirFD, err := openOrCreateEnrollmentDirectoryNoFollow(stateDir)
	if err != nil {
		return err
	}
	defer unix.Close(dirFD) //nolint:errcheck
	receipt := filepath.Base(enrollmentReceiptPath(stateDir, nodeID))
	receiptContents, receiptMeta, err := readRegularFileAt(dirFD, receipt)
	if err != nil || strings.TrimSpace(string(receiptContents)) != "accepted" || receiptMeta.uid != uint32(os.Geteuid()) {
		return fmt.Errorf("credential cleanup request has no trusted enrollment receipt")
	}
	requestName := filepath.Base(credentialCleanupRequestPath(stateDir))
	fd, err := unix.Openat(dirFD, requestName, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		if errors.Is(err, unix.EEXIST) {
			contents, requestMeta, readErr := readRegularFileAt(dirFD, requestName)
			if readErr != nil {
				return fmt.Errorf("read existing credential cleanup request: %w", readErr)
			}
			if requestMeta.uid != uint32(os.Geteuid()) {
				return fmt.Errorf("existing credential cleanup request is not trusted")
			}
			if strings.TrimSpace(string(contents)) != receipt {
				if replaceErr := replaceCredentialCleanupRequestAt(dirFD, requestName, receipt); replaceErr != nil {
					return replaceErr
				}
			}
			completionName := filepath.Base(credentialCleanupCompletionPath(stateDir))
			if unlinkErr := unix.Unlinkat(dirFD, completionName, 0); unlinkErr != nil && !errors.Is(unlinkErr, unix.ENOENT) {
				return fmt.Errorf("invalidate credential cleanup completion: %w", unlinkErr)
			}
			if syncErr := unix.Fsync(dirFD); syncErr != nil {
				return fmt.Errorf("sync credential cleanup directory: %w", syncErr)
			}
			return nil
		}
		return fmt.Errorf("create credential cleanup request: %w", err)
	}
	file := os.NewFile(uintptr(fd), requestName)
	if file == nil {
		_ = unix.Close(fd)
		return fmt.Errorf("open credential cleanup request")
	}
	removeIncomplete := true
	defer func() {
		if removeIncomplete {
			if cleanupErr := unix.Unlinkat(dirFD, requestName, 0); cleanupErr != nil && !errors.Is(cleanupErr, unix.ENOENT) {
				CredentialCleanupOutcomes.WithLabelValues("request_cleanup_error").Inc()
			}
		}
	}()
	if _, err = file.WriteString(receipt + "\n"); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("persist credential cleanup request: %w", err)
	}
	// A new pending generation invalidates the previous root-helper success
	// receipt. Pending metrics take precedence even if this best-effort unlink
	// loses a race with the helper.
	completionName := filepath.Base(credentialCleanupCompletionPath(stateDir))
	if unlinkErr := unix.Unlinkat(dirFD, completionName, 0); unlinkErr != nil && !errors.Is(unlinkErr, unix.ENOENT) {
		return fmt.Errorf("invalidate credential cleanup completion: %w", unlinkErr)
	}
	if err := unix.Fsync(dirFD); err != nil {
		return fmt.Errorf("sync credential cleanup directory: %w", err)
	}
	removeIncomplete = false
	return nil
}

func replaceCredentialCleanupRequestAt(dirFD int, requestName, receipt string) error {
	random := make([]byte, 16)
	if _, err := cryptorand.Read(random); err != nil {
		return fmt.Errorf("generate cleanup request temporary name: %w", err)
	}
	temporaryName := ".credential-cleanup.request-" + hex.EncodeToString(random)
	fd, err := unix.Openat(dirFD, temporaryName, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("create replacement cleanup request: %w", err)
	}
	file := os.NewFile(uintptr(fd), temporaryName)
	if file == nil {
		_ = unix.Close(fd)
		return fmt.Errorf("open replacement cleanup request")
	}
	removeIncomplete := true
	defer func() {
		if removeIncomplete {
			if cleanupErr := unix.Unlinkat(dirFD, temporaryName, 0); cleanupErr != nil && !errors.Is(cleanupErr, unix.ENOENT) {
				CredentialCleanupOutcomes.WithLabelValues("request_cleanup_error").Inc()
			}
		}
	}()
	if _, err = file.WriteString(receipt + "\n"); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("persist replacement cleanup request: %w", err)
	}
	if err := unix.Renameat(dirFD, temporaryName, dirFD, requestName); err != nil {
		return fmt.Errorf("replace stale cleanup request: %w", err)
	}
	if err := unix.Fsync(dirFD); err != nil {
		return fmt.Errorf("sync credential cleanup directory: %w", err)
	}
	removeIncomplete = false
	return nil
}

func finalizeAcceptedEnrollment(stateDir, nodeID, tokenFile, runtimeEnvFile string, rotationRequested bool, submittedCredential string) error {
	credentialSubmitted := strings.TrimSpace(submittedCredential) != ""
	credentialDigest := sha256.Sum256([]byte(submittedCredential))
	finalizationKey := strings.TrimSpace(stateDir) + "\x00" + strings.TrimSpace(nodeID) + "\x00" + hex.EncodeToString(credentialDigest[:])
	if credentialSubmitted {
		if _, finalized := finalizedEnrollmentCredentials.Load(finalizationKey); finalized {
			return nil
		}
	}
	if rotationRequested {
		if err := nodeidentity.CompleteRotation(stateDir, nodeID); err != nil {
			return fmt.Errorf("complete node identity rotation: %w", err)
		}
	}
	if err := markEnrollmentAccepted(stateDir, nodeID); err != nil {
		return err
	}
	if !credentialSubmitted {
		return nil
	}
	cleanupErr := clearConsumedEnrollmentCredentials(tokenFile, runtimeEnvFile)
	if cleanupErr == nil {
		completedAt := time.Now().Unix()
		directCredentialCleanupCompletions.Store(filepath.Clean(stateDir), completedAt)
		if usesRootCredentialCleanup(tokenFile, runtimeEnvFile) {
			// In container mode only the root helper may write the durable
			// completion marker; an unprivileged direct path must not forge it.
			if stateErr := removeCredentialCleanupCompletion(stateDir); stateErr != nil {
				CredentialCleanupOutcomes.WithLabelValues("completion_error").Inc()
				return fmt.Errorf("remove untrusted credential cleanup completion: %w", stateErr)
			}
		} else {
			// Native installs have no privilege split. Persist the timestamp so
			// the metrics process can restore last-success after a restart.
			stateErr := errors.Join(
				removeObsoleteCredentialCleanupRequest(stateDir, nodeID),
				recordCredentialCleanupCompletion(stateDir),
			)
			if stateErr != nil {
				CredentialCleanupOutcomes.WithLabelValues("completion_error").Inc()
				return fmt.Errorf("persist native credential cleanup state: %w", stateErr)
			}
		}
		CredentialCleanupLastSuccessTimestamp.Set(float64(completedAt))
		CredentialCleanupPending.Set(0)
		CredentialCleanupPendingSeconds.Set(0)
		CredentialCleanupObservationError.Set(0)
		CredentialCleanupOutcomes.WithLabelValues("direct").Inc()
		finalizedEnrollmentCredentials.Store(finalizationKey, struct{}{})
		return nil
	}
	if requestErr := requestCredentialCleanup(stateDir, nodeID); requestErr != nil {
		CredentialCleanupOutcomes.WithLabelValues("request_error").Inc()
		return fmt.Errorf("clear consumed enrollment credentials and request root cleanup: %w", errors.Join(cleanupErr, requestErr))
	}
	CredentialCleanupLastSuccessTimestamp.Set(0)
	CredentialCleanupPending.Set(1)
	CredentialCleanupPendingSeconds.Set(0)
	CredentialCleanupObservationError.Set(0)
	CredentialCleanupOutcomes.WithLabelValues("deferred").Inc()
	finalizedEnrollmentCredentials.Store(finalizationKey, struct{}{})
	return &credentialCleanupDeferredError{cause: cleanupErr}
}

func removeObsoleteCredentialCleanupRequest(stateDir, nodeID string) error {
	dirFD, err := openEnrollmentDirectoryNoFollow(stateDir)
	if err != nil {
		return err
	}
	defer unix.Close(dirFD) //nolint:errcheck

	requestName := filepath.Base(credentialCleanupRequestPath(stateDir))
	request, requestMeta, err := readRegularFileAt(dirFD, requestName)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read obsolete credential cleanup request: %w", err)
	}
	if requestMeta.uid != uint32(os.Geteuid()) {
		return fmt.Errorf("obsolete credential cleanup request is not trusted")
	}
	receiptName := filepath.Base(enrollmentReceiptPath(stateDir, nodeID))
	receipt, receiptMeta, err := readRegularFileAt(dirFD, receiptName)
	if err != nil {
		return fmt.Errorf("read current enrollment receipt: %w", err)
	}
	if receiptMeta.uid != uint32(os.Geteuid()) || strings.TrimSpace(string(receipt)) != "accepted" {
		return fmt.Errorf("current enrollment receipt is not trusted")
	}
	if strings.TrimSpace(string(request)) != receiptName && requestMeta.modTime.After(receiptMeta.modTime) {
		return fmt.Errorf("credential cleanup request belongs to a newer enrollment receipt")
	}
	if err := unix.Unlinkat(dirFD, requestName, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("remove obsolete credential cleanup request: %w", err)
	}
	if err := unix.Fsync(dirFD); err != nil {
		return fmt.Errorf("sync credential cleanup directory: %w", err)
	}
	return nil
}

func processCredentialCleanupRequest(stateDir, nodeID, tokenFile, runtimeEnvFile string, expectedOwnerUID ...uint32) error {
	dirFD, err := openEnrollmentDirectoryNoFollow(stateDir)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return err
	}
	defer unix.Close(dirFD) //nolint:errcheck

	requestName := filepath.Base(credentialCleanupRequestPath(stateDir))
	request, requestMeta, err := readRegularFileAt(dirFD, requestName)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return err
	}
	trustedOwnerUID := uint32(os.Geteuid())
	if len(expectedOwnerUID) > 0 {
		trustedOwnerUID = expectedOwnerUID[0]
	}
	if requestMeta.uid != trustedOwnerUID {
		return fmt.Errorf("credential cleanup request has untrusted owner uid %d", requestMeta.uid)
	}
	receiptName := strings.TrimSpace(string(request))
	expectedReceipt := filepath.Base(enrollmentReceiptPath(stateDir, nodeID))
	if receiptName == "" || receiptName != expectedReceipt {
		return fmt.Errorf("credential cleanup request has no valid enrollment receipt")
	}
	receipt, receiptMeta, err := readRegularFileAt(dirFD, receiptName)
	if err != nil {
		return fmt.Errorf("credential cleanup request is not backed by an accepted enrollment receipt")
	}
	if receiptMeta.uid != trustedOwnerUID || strings.TrimSpace(string(receipt)) != "accepted" {
		return fmt.Errorf("credential cleanup request is not backed by a trusted enrollment receipt")
	}
	if err := clearConsumedEnrollmentCredentials(tokenFile, runtimeEnvFile); err != nil {
		return err
	}
	if err := recordCredentialCleanupCompletionAt(dirFD); err != nil {
		return fmt.Errorf("record credential cleanup completion: %w", err)
	}
	if err := unix.Unlinkat(dirFD, requestName, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("remove credential cleanup request: %w", err)
	}
	CredentialCleanupPending.Set(0)
	CredentialCleanupPendingSeconds.Set(0)
	CredentialCleanupOutcomes.WithLabelValues("scrubbed").Inc()
	if err := unix.Fsync(dirFD); err != nil {
		return fmt.Errorf("sync credential cleanup directory: %w", err)
	}
	return nil
}

func recordCredentialCleanupCompletion(stateDir string) error {
	dirFD, err := openEnrollmentDirectoryNoFollow(stateDir)
	if err != nil {
		return err
	}
	defer unix.Close(dirFD) //nolint:errcheck
	return recordCredentialCleanupCompletionAt(dirFD)
}

func removeCredentialCleanupCompletion(stateDir string) error {
	dirFD, err := openEnrollmentDirectoryNoFollow(stateDir)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return err
	}
	defer unix.Close(dirFD) //nolint:errcheck
	completionName := filepath.Base(credentialCleanupCompletionPath(stateDir))
	if err := unix.Unlinkat(dirFD, completionName, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	return unix.Fsync(dirFD)
}

const maxEnrollmentStateFileSize = 64 << 10

type enrollmentFileMetadata struct {
	uid     uint32
	modTime time.Time
}

func usesRootCredentialCleanup(tokenFile, runtimeEnvFile string) bool {
	return filepath.Clean(tokenFile) == containerTokenFile && filepath.Clean(runtimeEnvFile) == containerRuntimeEnvFile
}

func trustedCredentialCleanupCompletionOwner(rootOwned bool, uid uint32) bool {
	return !rootOwned || uid == 0
}

func openEnrollmentDirectoryNoFollow(stateDir string) (int, error) {
	return openEnrollmentDirectory(stateDir, false)
}

func openOrCreateEnrollmentDirectoryNoFollow(stateDir string) (int, error) {
	return openEnrollmentDirectory(stateDir, true)
}

func openEnrollmentDirectory(stateDir string, create bool) (int, error) {
	stateDir = strings.TrimSpace(stateDir)
	if stateDir == "" {
		return -1, fmt.Errorf("state directory is required")
	}
	stateFD, err := openDirectoryPathNoFollow(filepath.Clean(stateDir), create)
	if err != nil {
		return -1, fmt.Errorf("open state directory: %w", err)
	}
	defer unix.Close(stateFD) //nolint:errcheck
	if create {
		if mkdirErr := unix.Mkdirat(stateFD, "enrollment", 0o700); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
			return -1, fmt.Errorf("create enrollment directory: %w", mkdirErr)
		}
	}
	dirFD, err := unix.Openat(stateFD, "enrollment", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, fmt.Errorf("open enrollment directory: %w", err)
	}
	return dirFD, nil
}

func openDirectoryPathNoFollow(path string, create bool) (int, error) {
	cleaned := filepath.Clean(path)
	// macOS exposes /var and /tmp through root-owned compatibility symlinks.
	// Canonicalize only those fixed system aliases; arbitrary caller-controlled
	// symlink components remain rejected by O_NOFOLLOW below.
	if runtime.GOOS == "darwin" && (cleaned == "/var" || strings.HasPrefix(cleaned, "/var/") || cleaned == "/tmp" || strings.HasPrefix(cleaned, "/tmp/")) {
		cleaned = "/private" + cleaned
	}
	start := "."
	if filepath.IsAbs(cleaned) {
		start = string(filepath.Separator)
	}
	fd, err := unix.Open(start, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	components := strings.Split(strings.TrimPrefix(cleaned, string(filepath.Separator)), string(filepath.Separator))
	for _, component := range components {
		if component == "" || component == "." {
			continue
		}
		if component == ".." {
			_ = unix.Close(fd)
			return -1, fmt.Errorf("parent traversal is not allowed")
		}
		if create {
			if mkdirErr := unix.Mkdirat(fd, component, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				_ = unix.Close(fd)
				return -1, mkdirErr
			}
		}
		nextFD, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return -1, openErr
		}
		fd = nextFD
	}
	return fd, nil
}

func readRegularFileAt(dirFD int, name string) ([]byte, enrollmentFileMetadata, error) {
	if name == "" || filepath.Base(name) != name {
		return nil, enrollmentFileMetadata{}, fmt.Errorf("invalid enrollment state file name")
	}
	fd, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, enrollmentFileMetadata{}, err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, enrollmentFileMetadata{}, fmt.Errorf("open enrollment state file")
	}
	defer file.Close() //nolint:errcheck
	var stat unix.Stat_t
	if statErr := unix.Fstat(fd, &stat); statErr != nil {
		return nil, enrollmentFileMetadata{}, statErr
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, enrollmentFileMetadata{}, fmt.Errorf("enrollment state path is not a regular file")
	}
	if stat.Size < 0 || stat.Size > maxEnrollmentStateFileSize {
		return nil, enrollmentFileMetadata{}, fmt.Errorf("enrollment state file exceeds %d bytes", maxEnrollmentStateFileSize)
	}
	info, err := file.Stat()
	if err != nil {
		return nil, enrollmentFileMetadata{}, err
	}
	contents, err := io.ReadAll(io.LimitReader(file, maxEnrollmentStateFileSize+1))
	if err != nil {
		return nil, enrollmentFileMetadata{}, err
	}
	return contents, enrollmentFileMetadata{uid: stat.Uid, modTime: info.ModTime()}, nil
}

func recordCredentialCleanupCompletionAt(dirFD int) error {
	random := make([]byte, 16)
	if _, err := cryptorand.Read(random); err != nil {
		return fmt.Errorf("generate completion temporary name: %w", err)
	}
	temporaryName := ".credential-cleanup.completed-" + hex.EncodeToString(random)
	fd, err := unix.Openat(dirFD, temporaryName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), temporaryName)
	if file == nil {
		_ = unix.Close(fd)
		return fmt.Errorf("open credential cleanup completion")
	}
	defer unix.Unlinkat(dirFD, temporaryName, 0) //nolint:errcheck
	if err = file.Chmod(0o644); err == nil {
		_, err = file.WriteString(strconv.FormatInt(time.Now().Unix(), 10) + "\n")
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	completionName := filepath.Base(credentialCleanupCompletionPath("."))
	if err := unix.Renameat(dirFD, temporaryName, dirFD, completionName); err != nil {
		return err
	}
	if err := unix.Fsync(dirFD); err != nil {
		return fmt.Errorf("sync credential cleanup directory: %w", err)
	}
	return nil
}

// RunCredentialCleanupWorker is the container's narrow root helper. Helmsman
// persists a non-secret request only after rotation completion and the
// enrollment receipt; this worker then scrubs the root-owned bind mounts.
func RunCredentialCleanupWorker(ctx context.Context, stateDir, nodeID, tokenFile, runtimeEnvFile string) error {
	if err := validateCredentialCleanupWorker(os.Geteuid(), stateDir, nodeID, tokenFile, runtimeEnvFile); err != nil {
		fmt.Fprintf(os.Stderr, "credential cleanup worker disabled: %v\n", err)
		<-ctx.Done()
		return nil
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := processCredentialCleanupRequest(stateDir, nodeID, tokenFile, runtimeEnvFile, helmsmanRuntimeUID); err != nil {
			fmt.Fprintf(os.Stderr, "credential cleanup worker: %v\n", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func validateCredentialCleanupWorker(euid int, stateDir, nodeID, tokenFile, runtimeEnvFile string) error {
	if euid != 0 {
		return fmt.Errorf("must run as root")
	}
	if strings.TrimSpace(stateDir) == "" || strings.TrimSpace(nodeID) == "" || strings.TrimSpace(tokenFile) == "" || strings.TrimSpace(runtimeEnvFile) == "" {
		return fmt.Errorf("requires state, node identity, and both environment file paths")
	}
	if filepath.Clean(stateDir) != containerStateDir || filepath.Clean(tokenFile) != containerTokenFile || filepath.Clean(runtimeEnvFile) != containerRuntimeEnvFile {
		return fmt.Errorf("credential cleanup paths do not match the fixed container mounts")
	}
	return nil
}

type credentialCleanupObservationState struct {
	observationErrorSince time.Time
	pendingSince          time.Time
}

func observeCredentialCleanupOnce(stateDir string, rootOwnedCompletion bool, now time.Time, state *credentialCleanupObservationState) {
	setIdle := func() {
		state.observationErrorSince = time.Time{}
		state.pendingSince = time.Time{}
		var completedTimestamp int64
		if completedAt, ok := directCredentialCleanupCompletions.Load(filepath.Clean(stateDir)); ok {
			if timestamp, valid := completedAt.(int64); valid {
				completedTimestamp = timestamp
			}
		}
		CredentialCleanupLastSuccessTimestamp.Set(float64(completedTimestamp))
		CredentialCleanupPending.Set(0)
		CredentialCleanupPendingSeconds.Set(0)
		CredentialCleanupObservationError.Set(0)
	}
	setObservationError := func() {
		if state.observationErrorSince.IsZero() {
			state.observationErrorSince = now
		}
		ageSince := state.observationErrorSince
		if !state.pendingSince.IsZero() && state.pendingSince.Before(ageSince) {
			ageSince = state.pendingSince
		}
		age := now.Sub(ageSince).Seconds()
		if age < 0 {
			age = 0
		}
		CredentialCleanupLastSuccessTimestamp.Set(0)
		CredentialCleanupPending.Set(1)
		CredentialCleanupPendingSeconds.Set(age)
		CredentialCleanupObservationError.Set(1)
	}

	dirFD, err := openEnrollmentDirectoryNoFollow(stateDir)
	if err != nil {
		setObservationError()
		return
	}
	defer unix.Close(dirFD) //nolint:errcheck

	requestName := filepath.Base(credentialCleanupRequestPath(stateDir))
	_, requestMeta, requestErr := readRegularFileAt(dirFD, requestName)
	if requestErr == nil {
		state.observationErrorSince = time.Time{}
		if state.pendingSince.IsZero() || requestMeta.modTime.Before(state.pendingSince) {
			state.pendingSince = requestMeta.modTime
		}
		CredentialCleanupLastSuccessTimestamp.Set(0)
		CredentialCleanupPending.Set(1)
		age := now.Sub(state.pendingSince).Seconds()
		if age < 0 {
			age = 0
		}
		CredentialCleanupPendingSeconds.Set(age)
		CredentialCleanupObservationError.Set(0)
		return
	}
	if !errors.Is(requestErr, unix.ENOENT) {
		setObservationError()
		return
	}

	completionName := filepath.Base(credentialCleanupCompletionPath(stateDir))
	completion, completionMeta, completionErr := readRegularFileAt(dirFD, completionName)
	if errors.Is(completionErr, unix.ENOENT) {
		setIdle()
		return
	}
	if completionErr != nil || !trustedCredentialCleanupCompletionOwner(rootOwnedCompletion, completionMeta.uid) {
		setObservationError()
		return
	}
	completedAt, parseErr := strconv.ParseInt(strings.TrimSpace(string(completion)), 10, 64)
	if parseErr != nil || completedAt <= 0 {
		setObservationError()
		return
	}
	state.observationErrorSince = time.Time{}
	state.pendingSince = time.Time{}
	CredentialCleanupLastSuccessTimestamp.Set(float64(completedAt))
	CredentialCleanupPending.Set(0)
	CredentialCleanupPendingSeconds.Set(0)
	CredentialCleanupObservationError.Set(0)
}

// ObserveCredentialCleanup exposes a pending request even when the root helper
// is absent or cannot complete it. The request contains no credential.
func ObserveCredentialCleanup(ctx context.Context, stateDir, tokenFile, runtimeEnvFile string) {
	rootOwnedCompletion := usesRootCredentialCleanup(tokenFile, runtimeEnvFile)
	state := credentialCleanupObservationState{}
	observeCredentialCleanupOnce(stateDir, rootOwnedCompletion, time.Now(), &state)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			observeCredentialCleanupOnce(stateDir, rootOwnedCompletion, time.Now(), &state)
		}
	}
}

func rewriteEnvValue(path, key, value string) error {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return fmt.Errorf("open environment file")
	}
	defer file.Close() //nolint:errcheck
	var stat unix.Stat_t
	if statErr := unix.Fstat(fd, &stat); statErr != nil {
		return statErr
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("environment target is not a regular file")
	}
	// The container helper must never let the Helmsman uid choose the file
	// contents that root edits. Other operator-owned host uid values are valid
	// bind-mount owners and remain supported.
	if os.Geteuid() == 0 && stat.Uid == 1001 {
		return fmt.Errorf("environment target is owned by the Helmsman runtime uid")
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return err
	}
	prefix := []byte(key + "=")
	type valueRange struct {
		start int
		end   int
	}
	var matches []valueRange
	for lineStart := 0; lineStart <= len(data); {
		lineEnd := lineStart
		for lineEnd < len(data) && data[lineEnd] != '\n' {
			lineEnd++
		}
		line := data[lineStart:lineEnd]
		trimmedOffset := 0
		for trimmedOffset < len(line) && (line[trimmedOffset] == ' ' || line[trimmedOffset] == '\t') {
			trimmedOffset++
		}
		if len(line[trimmedOffset:]) >= len("export ") && string(line[trimmedOffset:trimmedOffset+len("export ")]) == "export " {
			trimmedOffset += len("export ")
		}
		if len(line[trimmedOffset:]) >= len(prefix) && string(line[trimmedOffset:trimmedOffset+len(prefix)]) == string(prefix) {
			valueStart := lineStart + trimmedOffset + len(prefix)
			valueEnd := valueStart
			for valueEnd < len(data) && data[valueEnd] != '\n' && data[valueEnd] != '\r' {
				valueEnd++
			}
			matches = append(matches, valueRange{start: valueStart, end: valueEnd})
		}
		if lineEnd == len(data) {
			break
		}
		lineStart = lineEnd + 1
	}
	if len(matches) == 0 {
		return nil
	}
	for _, match := range matches {
		if len(value) > match.end-match.start {
			return fmt.Errorf("replacement for %s exceeds existing value width", key)
		}
	}
	for _, match := range matches {
		replacement := make([]byte, match.end-match.start)
		for i := range replacement {
			replacement[i] = ' '
		}
		copy(replacement, value)
		n, writeErr := file.WriteAt(replacement, int64(match.start))
		if writeErr != nil {
			return writeErr
		}
		if n != len(replacement) {
			return fmt.Errorf("short environment scrub: wrote %d of %d bytes", n, len(replacement))
		}
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return file.Close()
}
