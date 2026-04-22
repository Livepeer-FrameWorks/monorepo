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

func TestTopologicalSortSplitsSameHostTasks(t *testing.T) {
	graph := NewDependencyGraph()
	graph.AddTask(&Task{Name: "yugabyte-node-1", Host: "yuga-eu-1"})
	graph.AddTask(&Task{Name: "clickhouse", Host: "yuga-eu-1"})

	batches, err := graph.TopologicalSort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(batches) != 2 {
		t.Fatalf("expected 2 batches (same host must serialize), got %d: %v", len(batches), batches)
	}
	if len(batches[0]) != 1 || len(batches[1]) != 1 {
		t.Fatalf("expected one task per batch, got %v + %v", batches[0], batches[1])
	}
}

func TestTopologicalSortParallelAcrossHosts(t *testing.T) {
	graph := NewDependencyGraph()
	graph.AddTask(&Task{Name: "redis-foghorn", Host: "central-eu-1"})
	graph.AddTask(&Task{Name: "clickhouse", Host: "yuga-eu-1"})
	graph.AddTask(&Task{Name: "kafka-controller-101", Host: "regional-eu-1"})

	batches, err := graph.TopologicalSort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(batches) != 1 {
		t.Fatalf("expected 1 batch (distinct hosts parallelize), got %d", len(batches))
	}
	if len(batches[0]) != 3 {
		t.Fatalf("expected all 3 tasks in one batch, got %d", len(batches[0]))
	}
}

func TestTopologicalSortEmptyHostIgnoresExclusion(t *testing.T) {
	graph := NewDependencyGraph()
	graph.AddTask(&Task{Name: "task-a"})
	graph.AddTask(&Task{Name: "task-b"})

	batches, err := graph.TopologicalSort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(batches) != 1 || len(batches[0]) != 2 {
		t.Fatalf("tasks with empty Host must not participate in host exclusion, got %d batches: %v", len(batches), batches)
	}
}

func TestTopologicalSortSameHostRespectsDeps(t *testing.T) {
	graph := NewDependencyGraph()
	graph.AddTask(&Task{Name: "a", Host: "h1"})
	graph.AddTask(&Task{Name: "b", Host: "h1"})
	graph.AddTask(&Task{Name: "c", Host: "h2", DependsOn: []string{"a"}})

	batches, err := graph.TopologicalSort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(batches) != 2 {
		t.Fatalf("expected 2 batches, got %d: %v", len(batches), batches)
	}
	if !batchContains(batches[0], "a") {
		t.Fatalf("expected 'a' in first batch, got %v", batches[0])
	}
	if batchContains(batches[0], "b") {
		t.Fatalf("'b' must not share first batch with 'a' (same host)")
	}
	if !batchContains(batches[1], "b") || !batchContains(batches[1], "c") {
		t.Fatalf("expected 'b' and 'c' in second batch, got %v", batches[1])
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
