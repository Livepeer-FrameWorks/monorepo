package orchestrator

import (
	"fmt"
	"maps"
	"sort"
	"strings"
)

// DependencyGraph represents a directed acyclic graph of tasks
type DependencyGraph struct {
	tasks map[string]*Task
	edges map[string][]string // task -> dependencies
}

// NewDependencyGraph creates a new dependency graph
func NewDependencyGraph() *DependencyGraph {
	return &DependencyGraph{
		tasks: make(map[string]*Task),
		edges: make(map[string][]string),
	}
}

// AddTask adds a task to the graph
func (g *DependencyGraph) AddTask(task *Task) {
	g.tasks[task.Name] = task
	if _, exists := g.edges[task.Name]; !exists {
		g.edges[task.Name] = []string{}
	}

	// Add edges for dependencies
	g.edges[task.Name] = append(g.edges[task.Name], task.DependsOn...)
}

// TopologicalSort returns task batches in dependency order. Tasks within a
// batch have no unresolved dependencies AND target distinct hosts, so every
// batch can execute fully in parallel without same-host resource contention.
func (g *DependencyGraph) TopologicalSort() ([][]*Task, error) {
	// Calculate in-degree for each task
	inDegree := make(map[string]int)
	for name := range g.tasks {
		inDegree[name] = 0
	}

	for name, deps := range g.edges {
		inDegree[name] = len(deps)
	}

	var batches [][]*Task
	remaining := make(map[string]*Task, len(g.tasks))
	maps.Copy(remaining, g.tasks)

	for len(remaining) > 0 {
		// Admit at most one task per host per batch; same-host tasks contend
		// on dpkg, systemd, and filesystem state. Additional ready tasks on
		// an already-taken host keep inDegree 0 and are picked up next pass.
		batch := []*Task{}
		takenHosts := map[string]bool{}

		names := make([]string, 0, len(remaining))
		for name := range remaining {
			names = append(names, name)
		}
		sort.Strings(names)

		anyReady := false
		for _, name := range names {
			if inDegree[name] != 0 {
				continue
			}
			anyReady = true
			task := remaining[name]
			if task.Host != "" && takenHosts[task.Host] {
				continue
			}
			batch = append(batch, task)
			if task.Host != "" {
				takenHosts[task.Host] = true
			}
		}

		if len(batch) == 0 {
			if !anyReady {
				// Circular dependency detected
				cycle := g.findCycle(remaining)
				if len(cycle) > 0 {
					return nil, fmt.Errorf("circular dependency detected: %s", strings.Join(cycle, " -> "))
				}
				return nil, fmt.Errorf("circular dependency detected among tasks: %v", mapKeys(remaining))
			}
			return nil, fmt.Errorf("planner: ready tasks could not be scheduled (host-exclusion logic bug)")
		}

		batches = append(batches, batch)

		// Remove batch from remaining and update in-degrees
		for _, task := range batch {
			delete(remaining, task.Name)

			// Decrease in-degree for tasks that depend on this task
			for name := range remaining {
				for _, dep := range g.edges[name] {
					if dep == task.Name {
						inDegree[name]--
					}
				}
			}
		}
	}

	return batches, nil
}

// HasTask reports whether a task with the given name exists in the graph.
// Callers use this to emit DependsOn only for tasks actually in scope — the
// planner doesn't pad-then-strip, it gates at emission time.
func (g *DependencyGraph) HasTask(name string) bool {
	_, ok := g.tasks[name]
	return ok
}

// Validate checks for circular dependencies and missing dependencies
func (g *DependencyGraph) Validate() error {
	// Check for missing dependencies
	for name, deps := range g.edges {
		for _, dep := range deps {
			if _, exists := g.tasks[dep]; !exists {
				return fmt.Errorf("task %s depends on missing task %s", name, dep)
			}
		}
	}

	// Check for circular dependencies by trying topological sort
	_, err := g.TopologicalSort()
	if err != nil {
		return err
	}

	return nil
}

// mapKeys returns the keys of a map
func mapKeys(m map[string]*Task) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func (g *DependencyGraph) findCycle(remaining map[string]*Task) []string {
	visited := make(map[string]bool)
	active := make(map[string]bool)
	path := []string{}

	var visit func(name string) []string
	visit = func(name string) []string {
		if active[name] {
			cycleStart := indexOf(path, name)
			if cycleStart >= 0 {
				return append(append([]string{}, path[cycleStart:]...), name)
			}
			return []string{name, name}
		}
		if visited[name] {
			return nil
		}
		visited[name] = true
		active[name] = true
		path = append(path, name)

		deps := append([]string{}, g.edges[name]...)
		sort.Strings(deps)
		for _, dep := range deps {
			if _, ok := remaining[dep]; !ok {
				continue
			}
			if cycle := visit(dep); len(cycle) > 0 {
				return cycle
			}
		}

		active[name] = false
		path = path[:len(path)-1]
		return nil
	}

	names := make([]string, 0, len(remaining))
	for name := range remaining {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if cycle := visit(name); len(cycle) > 0 {
			return cycle
		}
	}
	return nil
}

func indexOf(items []string, target string) int {
	for i, item := range items {
		if item == target {
			return i
		}
	}
	return -1
}
