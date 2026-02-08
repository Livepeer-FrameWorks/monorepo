package orchestrator

import (
	"fmt"
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

// TopologicalSort returns tasks ordered by dependencies (batches that can run in parallel)
func (g *DependencyGraph) TopologicalSort() ([][]*Task, error) {
	// Calculate in-degree for each task
	inDegree := make(map[string]int)
	for name := range g.tasks {
		inDegree[name] = 0
	}

	for name, deps := range g.edges {
		inDegree[name] = len(deps)
	}

	// Find tasks with no dependencies (can run first)
	var batches [][]*Task
	remaining := make(map[string]*Task)
	for name, task := range g.tasks {
		remaining[name] = task
	}

	for len(remaining) > 0 {
		// Find all tasks with no remaining dependencies
		batch := []*Task{}
		for name, task := range remaining {
			if inDegree[name] == 0 {
				batch = append(batch, task)
			}
		}

		if len(batch) == 0 {
			// Circular dependency detected
			cycle := g.findCycle(remaining)
			if len(cycle) > 0 {
				return nil, fmt.Errorf("circular dependency detected: %s", strings.Join(cycle, " -> "))
			}
			return nil, fmt.Errorf("circular dependency detected among tasks: %v", mapKeys(remaining))
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
