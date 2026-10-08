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

	WorkingDir  string   // compose project directory
	ConfigFiles []string // compose files, absolute paths
	Networks    []string
	Volumes     []string // named volumes
	HostPorts   []int    // published host ports
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

// HealthCheck is the healthcheck result reported in the status text:
// "healthy", "unhealthy", "starting", or "" when there is no healthcheck.
func (c Container) HealthCheck() string {
	switch {
	case strings.Contains(c.Status, "(healthy)"):
		return "healthy"
	case strings.Contains(c.Status, "(unhealthy)"):
		return "unhealthy"
	case strings.Contains(c.Status, "(health: starting)"):
		return "starting"
	}
	return ""
}

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
		if c.HealthCheck() == "unhealthy" {
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

	// While a filter is active, matching projects start expanded and fold
	// state lives in filterCollapsed, so the user's own collapse state is
	// back unchanged once the filter is cleared.
	filter          string
	filterCollapsed map[string]bool
	matches         map[int]int // project index -> number of matching containers
}

func New() *Tree {
	return &Tree{collapsed: map[string]bool{}, filterCollapsed: map[string]bool{}}
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

func (t *Tree) IsCollapsed(project string) bool { return t.folds()[project] }

func (t *Tree) folds() map[string]bool {
	if t.filter != "" {
		return t.filterCollapsed
	}
	return t.collapsed
}

func (t *Tree) Filter() string { return t.filter }

// Matching is the number of containers in the row's project that match the
// active filter.
func (t *Tree) Matching(r Row) int { return t.matches[r.Project] }

// SetFilter shows only containers whose shown name, container name or
// project name contains q (case-insensitive). An empty q clears the filter.
func (t *Tree) SetFilter(q string) {
	if q == t.filter {
		return
	}
	t.filter = q
	t.filterCollapsed = map[string]bool{}
	t.update(nil)
}

// FirstContainer moves the cursor to the first container row, if any.
func (t *Tree) FirstContainer() {
	for i, r := range t.rows {
		if !r.IsProject() {
			t.cursor = i
			return
		}
	}
}

func (t *Tree) matchesFilter(p Project, c Container) bool {
	q := strings.ToLower(t.filter)
	return strings.Contains(strings.ToLower(p.Name), q) ||
		strings.Contains(strings.ToLower(c.DisplayName()), q) ||
		strings.Contains(strings.ToLower(c.Name), q)
}

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
	t.update(func() { t.projects = Group(cs) })
}

// update applies change (if any), rebuilds the rows and restores the cursor.
func (t *Tree) update(change func()) {
	prevCursor := t.cursor
	prevKey, prevPos := key{}, 0 // prevPos: position below the project row
	if r, ok := t.Selected(); ok {
		prevKey = t.keyOf(r)
		prevPos = t.cursor - t.find(key{project: prevKey.project}) - 1
	}

	if change != nil {
		change()
	}
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
			t.cursor = i
			if n := t.containerRowsAfter(i); n > 0 {
				t.cursor = i + 1 + min(prevPos, n-1)
			}
			return
		}
	}
	t.cursor = min(prevCursor, len(t.rows)-1)
}

// containerRowsAfter counts the container rows directly below project row i.
func (t *Tree) containerRowsAfter(i int) int {
	n := 0
	for j := i + 1; j < len(t.rows) && !t.rows[j].IsProject(); j++ {
		n++
	}
	return n
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
	t.setCollapsed(r.Project, !t.folds()[t.projects[r.Project].Name])
}

func (t *Tree) setCollapsed(project int, collapsed bool) {
	name := t.projects[project].Name
	if collapsed {
		t.folds()[name] = true
	} else {
		delete(t.folds(), name)
	}
	t.rebuild()
	t.cursor = t.find(key{project: name})
}

func (t *Tree) rebuild() {
	t.rows = t.rows[:0]
	t.matches = map[int]int{}
	folds := t.folds()
	for pi, p := range t.projects {
		var visible []int
		for ci, c := range p.Containers {
			if t.filter == "" || t.matchesFilter(p, c) {
				visible = append(visible, ci)
			}
		}
		if t.filter != "" && len(visible) == 0 {
			continue
		}
		t.matches[pi] = len(visible)
		t.rows = append(t.rows, Row{Project: pi, Container: -1})
		if folds[p.Name] {
			continue
		}
		for _, ci := range visible {
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
