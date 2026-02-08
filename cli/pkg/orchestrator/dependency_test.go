package orchestrator

import (
	"strings"
	"testing"
)

func TestTopologicalSortBatches(t *testing.T) {
	graph := NewDependencyGraph()
	graph.AddTask(&Task{Name: "task-a", DependsOn: []string{"task-b"}})
	graph.AddTask(&Task{Name: "task-b", DependsOn: []string{"task-c"}})
	graph.AddTask(&Task{Name: "task-c"})

	batches, err := graph.TopologicalSort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(batches) != 3 {
		t.Fatalf("expected 3 batches, got %d", len(batches))
	}
	if !batchContains(batches[0], "task-c") {
		t.Fatalf("expected first batch to contain task-c")
	}
	if !batchContains(batches[1], "task-b") {
		t.Fatalf("expected second batch to contain task-b")
	}
	if !batchContains(batches[2], "task-a") {
		t.Fatalf("expected third batch to contain task-a")
	}
}

func TestTopologicalSortCircularDependencyTrace(t *testing.T) {
	graph := NewDependencyGraph()
	graph.AddTask(&Task{Name: "task-a", DependsOn: []string{"task-b"}})
	graph.AddTask(&Task{Name: "task-b", DependsOn: []string{"task-c"}})
	graph.AddTask(&Task{Name: "task-c", DependsOn: []string{"task-a"}})

	_, err := graph.TopologicalSort()
	if err == nil {
		t.Fatal("expected error for circular dependency")
	}
	if !strings.Contains(err.Error(), "task-a -> task-b -> task-c -> task-a") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func batchContains(tasks []*Task, name string) bool {
	for _, task := range tasks {
		if task.Name == name {
			return true
		}
	}
	return false
}
