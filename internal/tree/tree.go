// Package tree groups containers into projects and tracks the cursor and
// expand/collapse state of the project/container tree. It has no UI or
// Docker dependencies so it can be tested in isolation.
package tree

import (
	"sort"
	"strings"
)

// Standalone is the synthetic project for containers without a compose
// project label. It always sorts last.
const Standalone = "(standalone)"

// Container is the subset of container information the tree and UI need.
type Container struct {
	ID      string
	Name    string
	Project string // compose project, "" for standalone containers
	Service string // compose service, "" for standalone containers
	Image   string
	State   string // running, exited, paused, restarting, created, dead, removing
	Status  string // human readable status from Docker, e.g. "Up 2 hours (healthy)"
	Ports   string
}

// DisplayName is the service name inside a compose project (the project is
// already shown on the parent row), otherwise the container name.
func (c Container) DisplayName() string {
	if c.Project != "" && c.Service != "" {
		return c.Service
	}
	return c.Name
}

func (c Container) Running() bool { return c.State == "running" }

// Health is a coarse classification used for colouring.
type Health int

const (
	Healthy Health = iota
	Degraded
	Failing
)

func (c Container) Health() Health {
	switch c.State {
	case "running":
		if strings.Contains(c.Status, "(unhealthy)") {
			return Degraded
		}
		return Healthy
	case "paused", "restarting", "removing":
		return Degraded
	default: // exited, dead, created
		return Failing
	}
}

// Project is a named group of containers.
type Project struct {
	Name       string
	Containers []Container
}

func (p Project) Running() int {
	n := 0
	for _, c := range p.Containers {
		if c.Running() {
			n++
		}
	}
	return n
}

// Health is the worst health of the project's containers.
func (p Project) Health() Health {
	h := Healthy
	for _, c := range p.Containers {
		if ch := c.Health(); ch > h {
			h = ch
		}
	}
	return h
}

// Group groups containers by compose project. Projects are sorted
// alphabetically with Standalone last; containers by display name.
func Group(cs []Container) []Project {
	byName := map[string][]Container{}
	for _, c := range cs {
		name := c.Project
		if name == "" {
			name = Standalone
		}
		byName[name] = append(byName[name], c)
	}
	projects := make([]Project, 0, len(byName))
	for name, members := range byName {
		sort.SliceStable(members, func(i, j int) bool {
			a, b := members[i], members[j]
			if a.DisplayName() != b.DisplayName() {
				return a.DisplayName() < b.DisplayName()
			}
			return a.Name < b.Name
		})
		projects = append(projects, Project{Name: name, Containers: members})
	}
	sort.Slice(projects, func(i, j int) bool {
		a, b := projects[i].Name, projects[j].Name
		if (a == Standalone) != (b == Standalone) {
			return b == Standalone
		}
		return a < b
	})
	return projects
}

// Row is one visible line of the tree. Container is -1 for a project row.
type Row struct {
	Project   int
	Container int
}

func (r Row) IsProject() bool { return r.Container < 0 }

// key identifies a row independently of its index, so the cursor survives
// refreshes that add or remove rows above it.
type key struct {
	project     string
	containerID string
}

// Tree is the navigable project/container tree.
type Tree struct {
	projects  []Project
	collapsed map[string]bool
	rows      []Row
	cursor    int
}

func New() *Tree {
	return &Tree{collapsed: map[string]bool{}}
}

func (t *Tree) Projects() []Project { return t.projects }
func (t *Tree) Rows() []Row         { return t.rows }
func (t *Tree) Cursor() int         { return t.cursor }

func (t *Tree) Project(r Row) *Project { return &t.projects[r.Project] }

func (t *Tree) Container(r Row) *Container {
	if r.IsProject() {
		return nil
	}
	return &t.projects[r.Project].Containers[r.Container]
}

func (t *Tree) IsCollapsed(project string) bool { return t.collapsed[project] }

// Selected returns the row under the cursor.
func (t *Tree) Selected() (Row, bool) {
	if len(t.rows) == 0 {
		return Row{}, false
	}
	return t.rows[t.cursor], true
}

// SelectedContainer returns the container under the cursor, or nil when the
// cursor is on a project row or the tree is empty.
func (t *Tree) SelectedContainer() *Container {
	r, ok := t.Selected()
	if !ok {
		return nil
	}
	return t.Container(r)
}

// Set replaces the containers while preserving the cursor and collapse state.
// If the selected container disappeared, the cursor moves to its neighbour in
// the same project, then to the project row, then to the nearest row.
func (t *Tree) Set(cs []Container) {
	prevCursor := t.cursor
	prevKey, prevContainerIdx := key{}, 0
	if r, ok := t.Selected(); ok {
		prevKey = t.keyOf(r)
		prevContainerIdx = r.Container
	}

	t.projects = Group(cs)
	t.rebuild()

	if len(t.rows) == 0 {
		t.cursor = 0
		return
	}
	if i := t.find(prevKey); i >= 0 {
		t.cursor = i
		return
	}
	if prevKey.containerID != "" {
		if i := t.find(key{project: prevKey.project}); i >= 0 {
			p := t.projects[t.rows[i].Project]
			t.cursor = i
			if !t.collapsed[p.Name] && len(p.Containers) > 0 {
				t.cursor = i + 1 + min(prevContainerIdx, len(p.Containers)-1)
			}
			return
		}
	}
	t.cursor = min(prevCursor, len(t.rows)-1)
}

func (t *Tree) Down() {
	if t.cursor < len(t.rows)-1 {
		t.cursor++
	}
}

func (t *Tree) Up() {
	if t.cursor > 0 {
		t.cursor--
	}
}

func (t *Tree) Top() { t.cursor = 0 }

func (t *Tree) Bottom() { t.cursor = max(0, len(t.rows)-1) }

// Expand expands the selected project. It is a no-op on container rows.
func (t *Tree) Expand() {
	r, ok := t.Selected()
	if !ok || !r.IsProject() {
		return
	}
	t.setCollapsed(r.Project, false)
}

// Collapse collapses the selected project, or moves to the parent project
// when a container is selected.
func (t *Tree) Collapse() {
	r, ok := t.Selected()
	if !ok {
		return
	}
	if !r.IsProject() {
		t.cursor = t.find(key{project: t.projects[r.Project].Name})
		return
	}
	t.setCollapsed(r.Project, true)
}

// Toggle flips the expand state of the selected project.
func (t *Tree) Toggle() {
	r, ok := t.Selected()
	if !ok || !r.IsProject() {
		return
	}
	t.setCollapsed(r.Project, !t.collapsed[t.projects[r.Project].Name])
}

func (t *Tree) setCollapsed(project int, collapsed bool) {
	name := t.projects[project].Name
	if collapsed {
		t.collapsed[name] = true
	} else {
		delete(t.collapsed, name)
	}
	t.rebuild()
	t.cursor = t.find(key{project: name})
}

func (t *Tree) rebuild() {
	t.rows = t.rows[:0]
	for pi, p := range t.projects {
		t.rows = append(t.rows, Row{Project: pi, Container: -1})
		if t.collapsed[p.Name] {
			continue
		}
		for ci := range p.Containers {
			t.rows = append(t.rows, Row{Project: pi, Container: ci})
		}
	}
}

func (t *Tree) keyOf(r Row) key {
	k := key{project: t.projects[r.Project].Name}
	if !r.IsProject() {
		k.containerID = t.projects[r.Project].Containers[r.Container].ID
	}
	return k
}

func (t *Tree) find(k key) int {
	if k.project == "" {
		return -1
	}
	for i, r := range t.rows {
		if t.keyOf(r) == k {
			return i
		}
	}
	return -1
}
