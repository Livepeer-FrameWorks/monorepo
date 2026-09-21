package mediaauthority

import (
	"bytes"
	"crypto/sha256"
	"strconv"
)

// HeldSummary summarizes a set of authority versions so two sides can tell
// whether they hold the same set without exchanging it: a count, and the XOR of
// one SHA-256 per member. XOR makes it independent of the order the members are
// read in, which matters because the two sides read them from different
// databases with different collations.
//
// It answers "is there anything to repeat", not "what": a cell uses it to ask
// for a replay only when what it holds differs from what the control plane has
// on record as delivered. It is not a security boundary.
type HeldSummary struct {
	Count  int64
	digest [sha256.Size]byte
}

// Add includes one authority version.
func (h *HeldSummary) Add(authorityKind, authorityID string, authorityVersion int64) {
	member := sha256.Sum256([]byte(authorityKind + "\x00" + authorityID + "\x00" + strconv.FormatInt(authorityVersion, 10)))
	for i := range h.digest {
		h.digest[i] ^= member[i]
	}
	h.Count++
}

// Digest returns the summary's digest.
func (h *HeldSummary) Digest() []byte {
	return bytes.Clone(h.digest[:])
}

// Matches reports whether a summary received from the other side describes the
// same set.
func (h *HeldSummary) Matches(count int64, digest []byte) bool {
	return h.Count == count && bytes.Equal(h.digest[:], digest)
}
