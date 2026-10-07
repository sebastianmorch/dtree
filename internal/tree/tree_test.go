package tree

import (
	"reflect"
	"testing"
)

func c(id, project, service string) Container {
	name := id
	if project != "" {
		name = project + "-" + service + "-1"
	}
	return Container{ID: id, Name: name, Project: project, Service: service, State: "running"}
}

func names(t *Tree) []string {
	var out []string
	for _, r := range t.Rows() {
		if r.IsProject() {
			out = append(out, t.Project(r).Name)
		} else {
			out = append(out, "  "+t.Container(r).DisplayName())
		}
	}
	return out
}

func selected(t *Tree) string {
	r, ok := t.Selected()
	if !ok {
		return ""
	}
	if r.IsProject() {
		return t.Project(r).Name
	}
	return t.Container(r).ID
}

func TestGroupSortsProjectsWithStandaloneLast(t *testing.T) {
	ps := Group([]Container{
		c("1", "", ""),
		c("2", "zeta", "web"),
		c("3", "alpha", "db"),
		c("4", "alpha", "api"),
	})
	var got []string
	for _, p := range ps {
		got = append(got, p.Name)
		for _, c := range p.Containers {
			got = append(got, "  "+c.DisplayName())
		}
	}
	want := []string{"alpha", "  api", "  db", "zeta", "  web", Standalone, "  1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDisplayName(t *testing.T) {
	tests := []struct {
		c    Container
		want string
	}{
		{Container{Name: "shop-api-1", Project: "shop", Service: "api"}, "api"},
		{Container{Name: "buildkit"}, "buildkit"},
		{Container{Name: "odd", Project: "shop"}, "odd"},
	}
	for _, tt := range tests {
		if got := tt.c.DisplayName(); got != tt.want {
			t.Errorf("DisplayName(%+v) = %q, want %q", tt.c, got, tt.want)
		}
	}
}

func TestHealth(t *testing.T) {
	tests := []struct {
		state, status string
		want          Health
	}{
		{"running", "Up 2 hours", Healthy},
		{"running", "Up 2 hours (healthy)", Healthy},
		{"running", "Up 2 hours (unhealthy)", Degraded},
		{"paused", "Up 2 hours (Paused)", Degraded},
		{"restarting", "Restarting (1) 3 seconds ago", Degraded},
		{"exited", "Exited (1) 5 minutes ago", Failing},
		{"created", "Created", Failing},
	}
	for _, tt := range tests {
		if got := (Container{State: tt.state, Status: tt.status}).Health(); got != tt.want {
			t.Errorf("Health(%s, %s) = %v, want %v", tt.state, tt.status, got, tt.want)
		}
	}
	p := Project{Containers: []Container{
		{State: "running"},
		{State: "running", Status: "Up (unhealthy)"},
	}}
	if p.Health() != Degraded || p.Running() != 2 {
		t.Errorf("project health = %v running = %d", p.Health(), p.Running())
	}
}

func TestNavigationAndFolding(t *testing.T) {
	tr := New()
	tr.Set([]Container{c("a1", "a", "x"), c("a2", "a", "y"), c("b1", "b", "z")})
	if got := names(tr); !reflect.DeepEqual(got, []string{"a", "  x", "  y", "b", "  z"}) {
		t.Fatalf("rows = %q", got)
	}

	tr.Up() // already at top
	if selected(tr) != "a" {
		t.Fatalf("selected %q", selected(tr))
	}
	tr.Expand() // no-op, already expanded
	tr.Down()
	tr.Down()
	if selected(tr) != "a2" {
		t.Fatalf("selected %q", selected(tr))
	}
	tr.Expand() // no-op on container
	if selected(tr) != "a2" {
		t.Fatalf("expand on container moved cursor to %q", selected(tr))
	}
	tr.Collapse() // container -> parent
	if selected(tr) != "a" || tr.IsCollapsed("a") {
		t.Fatalf("collapse on container: selected %q collapsed %v", selected(tr), tr.IsCollapsed("a"))
	}
	tr.Collapse()
	if !tr.IsCollapsed("a") || !reflect.DeepEqual(names(tr), []string{"a", "b", "  z"}) {
		t.Fatalf("after collapse rows = %q", names(tr))
	}
	tr.Bottom()
	if selected(tr) != "b1" {
		t.Fatalf("bottom selected %q", selected(tr))
	}
	tr.Down()
	if selected(tr) != "b1" {
		t.Fatalf("down past bottom selected %q", selected(tr))
	}
	tr.Top()
	tr.Toggle()
	if tr.IsCollapsed("a") || selected(tr) != "a" {
		t.Fatalf("toggle: collapsed %v selected %q", tr.IsCollapsed("a"), selected(tr))
	}
	tr.Down()
	tr.Toggle() // no-op on container
	if tr.IsCollapsed("a") {
		t.Fatal("toggle on container collapsed project")
	}
}

func TestSetPreservesCursorByKey(t *testing.T) {
	tr := New()
	tr.Set([]Container{c("b1", "b", "z"), c("c1", "c", "w")})
	tr.Bottom() // c1
	// a new project sorts above the cursor; cursor must stay on c1
	tr.Set([]Container{c("a1", "a", "x"), c("b1", "b", "z"), c("c1", "c", "w")})
	if selected(tr) != "c1" {
		t.Fatalf("selected %q, want c1", selected(tr))
	}
}

func TestSetPreservesCollapseState(t *testing.T) {
	tr := New()
	tr.Set([]Container{c("a1", "a", "x"), c("b1", "b", "z")})
	tr.Collapse()
	tr.Set([]Container{c("a1", "a", "x"), c("a2", "a", "y"), c("b1", "b", "z")})
	if !reflect.DeepEqual(names(tr), []string{"a", "b", "  z"}) {
		t.Fatalf("rows = %q", names(tr))
	}
}

func TestSetSelectedContainerDisappears(t *testing.T) {
	tests := []struct {
		name   string
		before []Container
		pick   int // row index to select before the update
		after  []Container
		want   string
	}{
		{
			name:   "moves to neighbour in same project",
			before: []Container{c("a1", "a", "x"), c("a2", "a", "y"), c("a3", "a", "z")},
			pick:   2, // a2
			after:  []Container{c("a1", "a", "x"), c("a3", "a", "z")},
			want:   "a3",
		},
		{
			name:   "last container removed moves to new last",
			before: []Container{c("a1", "a", "x"), c("a2", "a", "y")},
			pick:   2, // a2
			after:  []Container{c("a1", "a", "x")},
			want:   "a1",
		},
		{
			name:   "project gone falls back to nearest row",
			before: []Container{c("a1", "a", "x"), c("b1", "b", "y"), c("c1", "c", "z")},
			pick:   3, // b1
			after:  []Container{c("a1", "a", "x"), c("c1", "c", "z")},
			want:   "c1",
		},
		{
			name:   "everything gone at the end clamps",
			before: []Container{c("a1", "a", "x"), c("b1", "b", "y")},
			pick:   3, // b1
			after:  []Container{c("a1", "a", "x")},
			want:   "a1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := New()
			tr.Set(tt.before)
			for i := 0; i < tt.pick; i++ {
				tr.Down()
			}
			tr.Set(tt.after)
			if got := selected(tr); got != tt.want {
				t.Fatalf("selected %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEmptyTree(t *testing.T) {
	tr := New()
	tr.Set(nil)
	tr.Down()
	tr.Up()
	tr.Bottom()
	tr.Expand()
	tr.Collapse()
	tr.Toggle()
	if _, ok := tr.Selected(); ok {
		t.Fatal("empty tree has a selection")
	}
	if tr.SelectedContainer() != nil {
		t.Fatal("empty tree has a selected container")
	}
	tr.Set([]Container{c("a1", "a", "x")})
	tr.Set(nil)
	if tr.Cursor() != 0 {
		t.Fatalf("cursor %d", tr.Cursor())
	}
}

func TestFilter(t *testing.T) {
	cs := []Container{
		c("l1", "local", "redis"),
		c("l2", "local", "hub"),
		c("s1", "shop", "redis"),
		c("s2", "shop", "api"),
		c("x1", "", ""),
	}
	tests := []struct {
		query string
		want  []string
	}{
		{"", []string{"local", "  hub", "  redis", "shop", "  api", "  redis", Standalone, "  x1"}},
		{"RED", []string{"local", "  redis", "shop", "  redis"}},
		{"shop", []string{"shop", "  api", "  redis"}}, // project name shows the whole project
		{"local-hub", []string{"local", "  hub"}},      // full container name
		{"stand", []string{Standalone, "  x1"}},        // synthetic group name
		{"nothing", nil},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			tr := New()
			tr.Set(cs)
			tr.SetFilter(tt.query)
			if got := names(tr); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("rows = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFilterMatchingCount(t *testing.T) {
	tr := New()
	tr.Set([]Container{c("l1", "local", "redis"), c("l2", "local", "hub")})
	tr.SetFilter("redis")
	if r, _ := tr.Selected(); tr.Matching(r) != 1 {
		t.Fatalf("matching = %d, want 1", tr.Matching(r))
	}
}

func TestFilterExpandsAndRestoresFoldState(t *testing.T) {
	tr := New()
	tr.Set([]Container{c("a1", "a", "x"), c("b1", "b", "y")})
	tr.Collapse() // collapse a
	tr.SetFilter("x")
	if tr.IsCollapsed("a") || !reflect.DeepEqual(names(tr), []string{"a", "  x"}) {
		t.Fatalf("filter should expand matches: %q", names(tr))
	}
	tr.Collapse() // folding inside the filter is separate
	if !tr.IsCollapsed("a") {
		t.Fatal("collapse during filter")
	}
	tr.SetFilter("")
	if !tr.IsCollapsed("a") || !reflect.DeepEqual(names(tr), []string{"a", "b", "  y"}) {
		t.Fatalf("original fold state not restored: %q", names(tr))
	}
	tr.Expand()
	tr.SetFilter("x")
	tr.Collapse()
	tr.SetFilter("")
	if tr.IsCollapsed("a") {
		t.Fatal("fold inside filter leaked into normal state")
	}
}

func TestFilterKeepsCursorAndFirstContainer(t *testing.T) {
	tr := New()
	tr.Set([]Container{c("a1", "a", "redis"), c("b1", "b", "redis"), c("b2", "b", "api")})
	tr.Bottom() // b2 (api)
	tr.SetFilter("redis")
	if got := selected(tr); got != "b1" { // b2 hidden: neighbour in same project
		t.Fatalf("selected %q, want b1", got)
	}
	tr.FirstContainer()
	if got := selected(tr); got != "a1" {
		t.Fatalf("FirstContainer selected %q", got)
	}
	tr.SetFilter("")
	if got := selected(tr); got != "a1" {
		t.Fatalf("clearing filter moved cursor to %q", got)
	}
	// Refreshes keep applying the filter.
	tr.SetFilter("api")
	tr.Set([]Container{c("a1", "a", "redis"), c("b2", "b", "api"), c("c1", "c", "api")})
	if got := names(tr); !reflect.DeepEqual(got, []string{"b", "  api", "c", "  api"}) {
		t.Fatalf("rows after refresh = %q", got)
	}
}
