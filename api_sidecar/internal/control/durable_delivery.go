package control

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// How a durable outbox row leaves the outbox depends on the Foghorn the
// connection reached, and the connection learns which from the first message
// Foghorn sends on it:
//
//   - ControlCapabilities{durable_delivery_acks}: the row is deleted when
//     Foghorn's DurableDeliveryAck for its delivery id reports success. A
//     successful Send only means the frame reached a local buffer, so a
//     Foghorn that dies without closing the socket would otherwise take rows
//     with it. A failed ack returns the row to pending for a later resend.
//   - any other message: Foghorn predates acks, and the row is confirmed by
//     the next heartbeat sent after it, as before acks existed.
//   - nothing yet: no row is confirmed. Rows sent so far stay in flight until
//     the contract is known or, if the connection ends first, are resent on
//     the next one.
//
// Foghorn sends ControlCapabilities before it reads Register, so the decision
// is made from stream order and never from how long Foghorn takes to answer.
const (
	durableContractUndecided int32 = iota
	durableContractHeartbeat
	durableContractAck
)

// durableOutboxNackRetryDelay holds back a row Foghorn could not apply, so
// the drain run by every new durable message does not resend it at once.
const durableOutboxNackRetryDelay = 10 * time.Second

// durableDeliveryContract is shared by every streamConn value describing the
// same connection.
type durableDeliveryContract struct {
	state atomic.Int32
}

func newDurableDeliveryContract() *durableDeliveryContract {
	return &durableDeliveryContract{}
}

// observe settles the contract from the first message Foghorn sends.
func (c *durableDeliveryContract) observe(msg *ipcpb.ControlMessage) {
	if c == nil || c.state.Load() != durableContractUndecided {
		return
	}
	next := durableContractHeartbeat
	if msg.GetControlCapabilities().GetDurableDeliveryAcks() {
		next = durableContractAck
	}
	c.state.CompareAndSwap(durableContractUndecided, next)
}

func (c *durableDeliveryContract) current() int32 {
	if c == nil {
		return durableContractUndecided
	}
	return c.state.Load()
}

// durableOutboxRetryNotBefore is guarded by durableOutboxMu.
var durableOutboxRetryNotBefore = make(map[string]time.Time)

// durableDeliveryIDForPath names a row by its file name without the .pb
// suffix: unique per row and unchanged by the pending/in-flight renames, so
// every resend of a row, also after a restart, carries the same id.
func durableDeliveryIDForPath(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".pb")
}

func validDurableDeliveryID(id string) bool {
	return id != "" && safeOutboxID(id) == id
}

// durableOutboxRetryDeferredLocked reports whether a row Foghorn refused is
// still waiting out its retry delay.
func durableOutboxRetryDeferredLocked(id string, now time.Time) bool {
	at, ok := durableOutboxRetryNotBefore[id]
	if !ok {
		return false
	}
	if !now.Before(at) {
		delete(durableOutboxRetryNotBefore, id)
		return false
	}
	return true
}

// handleDurableDeliveryAck applies Foghorn's answer for one row. A success
// removes the row in whatever state it is in: Foghorn has handled it, so a
// copy already queued for resend on a newer connection is redundant.
func handleDurableDeliveryAck(conn *streamConn, ack *ipcpb.DurableDeliveryAck) {
	id := ack.GetDeliveryId()
	if conn == nil || !validDurableDeliveryID(id) {
		if pkgLogger != nil {
			pkgLogger.WithField("delivery_id", id).Warn("Ignoring durable delivery ack with an invalid delivery id")
		}
		return
	}
	durableOutboxMu.Lock()
	defer durableOutboxMu.Unlock()
	dir := durableOutboxDir()
	var err error
	if ack.GetSuccess() {
		err = removeAckedDurableRowLocked(dir, id)
	} else {
		err = requeueRefusedDurableRowLocked(dir, id, conn.epoch, ack.GetError())
	}
	if syncErr := syncDurableOutboxDir(dir); syncErr != nil {
		err = errors.Join(err, syncErr)
	}
	updateDurableOutboxMetricsLocked(dir)
	if err != nil && pkgLogger != nil {
		pkgLogger.WithError(err).WithField("delivery_id", id).Warn("Failed to apply durable delivery ack")
	}
}

func removeAckedDurableRowLocked(dir, id string) error {
	inflight, err := filepath.Glob(filepath.Join(dir, id+".pb.sent.*"))
	if err != nil {
		return err
	}
	removed := false
	var errs []error
	for _, path := range append(inflight, filepath.Join(dir, id+".pb")) {
		switch removeErr := os.Remove(path); {
		case removeErr == nil:
			removed = true
		case !errors.Is(removeErr, os.ErrNotExist):
			errs = append(errs, removeErr)
		}
	}
	delete(durableOutboxRetryNotBefore, id)
	if removed {
		ControlDeliveryOutcomes.WithLabelValues("durable", "acked").Inc()
	}
	return errors.Join(errs...)
}

func requeueRefusedDurableRowLocked(dir, id, epoch, reason string) error {
	ControlDeliveryOutcomes.WithLabelValues("durable", "refused").Inc()
	if pkgLogger != nil {
		pkgLogger.WithFields(logging.Fields{"delivery_id": id, "error": reason}).
			Warn("Foghorn could not apply a durable control message; resending it later")
	}
	inflightPath := filepath.Join(dir, id+".pb.sent."+epoch)
	pendingPath := filepath.Join(dir, id+".pb")
	if _, err := os.Stat(inflightPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Already back to pending after a reconnect, or deleted by a
			// successful ack of an earlier send.
			return nil
		}
		return err
	}
	if _, err := os.Stat(pendingPath); err == nil {
		return fmt.Errorf("durable control outbox pending collision: %s", pendingPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(inflightPath, pendingPath); err != nil {
		return err
	}
	durableOutboxRetryNotBefore[id] = time.Now().Add(durableOutboxNackRetryDelay)
	return nil
}
