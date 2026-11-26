package state

import "testing"

func TestRepeatedProcessingBudgetDoesNotPublishAgain(t *testing.T) {
	sm, _, server, _ := setupTripleWriteTest(t)
	// Only the setter's I/O belongs in this counter; the independent changelog
	// reader can otherwise issue XREAD between either sample.
	sm.redisCancel()
	sm.redisWg.Wait()
	sm.TouchNode("edge", true)
	initial := server.CommandCount()
	sm.SetNodeProcessingMemoryBudget("edge", 2048)
	if server.CommandCount() == initial {
		t.Fatal("the initial budget did not publish to Redis")
	}
	before := server.CommandCount()
	sm.SetNodeProcessingMemoryBudget("edge", 2048)
	if calls := server.CommandCount() - before; calls != 0 {
		t.Fatalf("unchanged processing budget caused %d Redis commands, want 0", calls)
	}
}
