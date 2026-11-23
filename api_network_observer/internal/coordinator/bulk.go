package coordinator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"frameworks/api_network_observer/internal/store"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	"github.com/google/uuid"
)

func (a *API) ingestBulk(w http.ResponseWriter, r *http.Request) {
	var envelope struct {
		Records [][]byte `json:"records"`
	}
	payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 512<<10))
	if err != nil {
		http.Error(w, "batch body exceeds bound", http.StatusRequestEntityTooLarge)
		return
	}
	if json.Unmarshal(payload, &envelope) != nil {
		http.Error(w, "invalid bounded batch", 400)
		return
	}
	if len(envelope.Records) == 0 || len(envelope.Records) > 64 {
		http.Error(w, "batch must contain 1 to 64 records", 400)
		return
	}
	receipts := make([]store.Receipt, len(envelope.Records))
	ids := make([]string, len(receipts))
	observer := ""
	for i, raw := range envelope.Records {
		var b obs.Batch
		if json.Unmarshal(raw, &b) != nil {
			http.Error(w, "invalid record", 400)
			return
		}
		if _, parseErr := uuid.Parse(b.ID); parseErr != nil {
			http.Error(w, "invalid record ID", 400)
			return
		}
		if b.ObserverID == "" || len(b.ObserverID) > 128 || strings.HasPrefix(b.ObserverID, "gateway:") {
			http.Error(w, "invalid observer", 400)
			return
		}
		if observer == "" {
			observer = b.ObserverID
		}
		if observer != b.ObserverID {
			http.Error(w, "mixed observers", 400)
			return
		}
		sum := sha256.Sum256(raw)
		ids[i] = b.ID
		receipts[i] = store.Receipt{ID: b.ID, Digest: hex.EncodeToString(sum[:]), Observer: b.ObserverID, Kind: "prober", Payload: raw}
	}
	codes, err := a.Store.ReceiveMany(r.Context(), receipts)
	if errors.Is(err, store.ErrInboxFull) {
		w.Header().Set("Retry-After", "5")
		http.Error(w, "observation capacity reached", http.StatusTooManyRequests)
		return
	}
	if err != nil {
		http.Error(w, "receipt unavailable; retry original records", http.StatusServiceUnavailable)
		return
	}
	type result struct {
		ID       string `json:"id"`
		Accepted bool   `json:"accepted"`
		Code     string `json:"code,omitempty"`
	}
	results := make([]result, len(ids))
	for i, id := range ids {
		results[i] = result{ID: id, Accepted: codes[i] == "", Code: codes[i]}
	}
	respond(w, 200, map[string]any{"results": results})
}
