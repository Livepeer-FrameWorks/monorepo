package mediaauthority

import (
	"testing"
	"time"
)

func TestRestoreFenceRejectsConfirmationStartedBeforeBarrier(t *testing.T) {
	fence := &restoreFence{startup: true}
	started := fence.confirmationGeneration()
	fence.mu.Lock()
	fence.setDurableLocked(time.Now())
	fence.mu.Unlock()
	if fence.confirm("media_object", "stream", started) || !fence.holds("media_object", "stream") {
		t.Fatal("a fetch begun before the fence confirmed an authority")
	}
	if !fence.confirm("media_object", "stream", fence.confirmationGeneration()) || fence.holds("media_object", "stream") {
		t.Fatal("a fetch begun after the fence did not confirm the authority")
	}
}
