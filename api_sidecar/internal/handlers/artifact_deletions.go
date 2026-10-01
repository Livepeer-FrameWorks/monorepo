package handlers

import (
	"context"
	"errors"
	"sync"
	"time"
)

// errArtifactDeletedOnNode is the cancellation cause of a freeze upload whose artifact a Foghorn delete command
// removed from this node while the upload ran.
var errArtifactDeletedOnNode = errors.New("artifact deleted on this node")

// artifactDeletionWindow is how long a delete command keeps a freeze of that artifact from starting and classifies
// its missing local file as deleted rather than lost. It covers a freeze command Foghorn sent before the deletion
// that reaches this node after it.
const artifactDeletionWindow = 30 * time.Minute

// artifactDeletions records the clip/VOD delete commands this node received and the freeze uploads in flight per
// artifact, so a deletion stops the upload of the same artifact instead of removing the file under it.
type artifactDeletions struct {
	mu        sync.Mutex
	deletedAt map[string]time.Time
	uploads   map[string]map[uint64]context.CancelCauseFunc
	nextID    uint64
}

var nodeArtifactDeletions = newArtifactDeletions()

func newArtifactDeletions() *artifactDeletions {
	return &artifactDeletions{
		deletedAt: make(map[string]time.Time),
		uploads:   make(map[string]map[uint64]context.CancelCauseFunc),
	}
}

// noteDeleted records a delete command for hash and cancels every freeze upload of that artifact still running.
func (d *artifactDeletions) noteDeleted(hash string) {
	if hash == "" {
		return
	}
	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	for h, at := range d.deletedAt {
		if now.Sub(at) > artifactDeletionWindow {
			delete(d.deletedAt, h)
		}
	}
	d.deletedAt[hash] = now
	for _, cancel := range d.uploads[hash] {
		cancel(errArtifactDeletedOnNode)
	}
}

// deletedRecently reports whether a delete command for hash arrived within artifactDeletionWindow.
func (d *artifactDeletions) deletedRecently(hash string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	at, ok := d.deletedAt[hash]
	return ok && time.Since(at) <= artifactDeletionWindow
}

// trackUpload returns a context that a delete command for hash cancels with errArtifactDeletedOnNode, and the
// release func the upload calls when it ends.
func (d *artifactDeletions) trackUpload(parent context.Context, hash string) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	d.mu.Lock()
	d.nextID++
	id := d.nextID
	if d.uploads[hash] == nil {
		d.uploads[hash] = make(map[uint64]context.CancelCauseFunc)
	}
	d.uploads[hash][id] = cancel
	d.mu.Unlock()
	return ctx, func() {
		d.mu.Lock()
		delete(d.uploads[hash], id)
		if len(d.uploads[hash]) == 0 {
			delete(d.uploads, hash)
		}
		d.mu.Unlock()
		cancel(nil)
	}
}
