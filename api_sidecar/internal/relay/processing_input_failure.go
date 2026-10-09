package relay

import (
	"context"
	"errors"
	"net/http"
	"sync"
)

// processingInputFailures holds, per asset hash, the last upstream failure
// the relay hit while serving a processing input before any byte reached
// Mist. Mist only reports that its input did not boot; this is what tells the
// processing job whether storage stalled under it.
var processingInputFailures sync.Map

// processingInputDefects holds, per asset hash, a processing input whose
// stored source cannot be read whole on any attempt, so the processing job can
// name it when the input does not boot.
var processingInputDefects sync.Map

// noteProcessingInputFailure records err for assetHash when it is a transient
// upstream failure: the upstream did not answer before the reader gave up, the
// connection failed, or the upstream answered a server-side error. A source
// shorter than its catalog size is recorded as a defect instead. An upstream
// that answered a client error (missing, forbidden) is a property of the
// source and is not recorded.
func noteProcessingInputFailure(assetHash string, err error) {
	if assetHash == "" || err == nil {
		return
	}
	var short upstreamShortSourceError
	if errors.As(err, &short) {
		processingInputDefects.Store(assetHash, err.Error())
		return
	}
	if transientUpstreamFailure(err) {
		processingInputFailures.Store(assetHash, err.Error())
	}
}

// TakeProcessingInputFailure returns and forgets the transient upstream
// failure recorded for assetHash's processing input.
func TakeProcessingInputFailure(assetHash string) (string, bool) {
	return takeNote(&processingInputFailures, assetHash)
}

// TakeProcessingInputDefect returns and forgets the source defect recorded for
// assetHash's processing input. Another attempt reads the same source, so it is
// not a reason to retry.
func TakeProcessingInputDefect(assetHash string) (string, bool) {
	return takeNote(&processingInputDefects, assetHash)
}

func takeNote(notes *sync.Map, assetHash string) (string, bool) {
	v, ok := notes.LoadAndDelete(assetHash)
	if !ok {
		return "", false
	}
	reason, ok := v.(string)
	return reason, ok
}

func transientUpstreamFailure(err error) bool {
	var statusErr upstreamStatusError
	if errors.As(err, &statusErr) {
		return transientUpstreamStatus(statusErr.StatusCode)
	}
	return true
}

// transientUpstreamStatus reports whether an upstream status names a
// condition a later request can get past.
func transientUpstreamStatus(code int) bool {
	return code == http.StatusTooManyRequests || code >= http.StatusInternalServerError
}

// errUpstreamNoAnswer names a request whose reader hung up before the upstream
// answered it.
var errUpstreamNoAnswer = errors.New("upstream did not answer before the reader gave up")

// upstreamFailureCause replaces a cancellation caused by the reader leaving
// with what it means for the upstream: it did not answer in time.
func upstreamFailureCause(ctx context.Context, err error) error {
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return errUpstreamNoAnswer
	}
	return err
}
