package memory

import (
	"context"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/claudioed/task-interleaving/internal/application/repository"
)

// TaskPool is the in-memory ports.TaskPool.
type TaskPool struct {
	mu    sync.Mutex
	tasks map[string]repository.Task
}

// NewTaskPool constructs an empty TaskPool.
func NewTaskPool() *TaskPool {
	return &TaskPool{tasks: map[string]repository.Task{}}
}

// Get implements ports.TaskPool.
func (p *TaskPool) Get(_ context.Context, id string) (repository.Task, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	t, ok := p.tasks[id]
	return cloneTask(t), ok, nil
}

// Save implements ports.TaskPool.
func (p *TaskPool) Save(_ context.Context, t repository.Task) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tasks[t.ID] = cloneTask(t)
	return nil
}

// Available implements ports.TaskPool.
func (p *TaskPool) Available(_ context.Context, siteID string) ([]repository.Task, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []repository.Task
	for _, t := range p.tasks {
		if t.State == repository.TaskAvailable && t.SiteID == siteID {
			out = append(out, cloneTask(t))
		}
	}
	slices.SortFunc(out, func(a, b repository.Task) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

func cloneTask(t repository.Task) repository.Task {
	t.RequiredCapabilities = slices.Clone(t.RequiredCapabilities)
	return t
}

// Snapshot implements Snapshotter. Stored tasks are replaced, never mutated in
// place, so a shallow copy of the map is a faithful restore point.
func (p *TaskPool) Snapshot() func() {
	p.mu.Lock()
	defer p.mu.Unlock()
	saved := maps.Clone(p.tasks)
	return func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.tasks = saved
	}
}
