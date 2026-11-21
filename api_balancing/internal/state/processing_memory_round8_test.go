package state

import "testing"

func TestRepeatedProcessingBudgetDoesNotPublishAgain(t *testing.T) {
	sm, _, server, _ := setupTripleWriteTest(t)
	sm.TouchNode("edge", true)
	sm.SetNodeProcessingMemoryBudget("edge", 2048)
	before := server.CommandCount()
	sm.SetNodeProcessingMemoryBudget("edge", 2048)
	if calls := server.CommandCount() - before; calls != 0 {
		t.Fatalf("unchanged processing budget caused %d Redis commands, want 0", calls)
	}
}
