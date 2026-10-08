package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"dtree/internal/docker"
	"dtree/internal/tree"
)

type fakeClient struct {
	running, all []tree.Container
	logs         []docker.LogLine
	logOpts      docker.LogOptions
	inspect      string
	events       chan struct{}
	eventErrs    chan error
}

func newFake() *fakeClient {
	return &fakeClient{
		running: []tree.Container{
			{ID: "a1", Name: "shop-api-1", Project: "shop", Service: "api", Image: "shop:dev", State: "running", Status: "Up 2 hours", Ports: "8080->8080"},
			{ID: "a2", Name: "shop-db-1", Project: "shop", Service: "db", Image: "postgres:16", State: "running", Status: "Up 2 hours"},
			{ID: "s1", Name: "buildkit", Image: "moby/buildkit", State: "running", Status: "Up 3 days"},
		},
		inspect:   `{"Id":"a1","State":{"Running":true}}`,
		events:    make(chan struct{}),
		eventErrs: make(chan error),
	}
}

func (f *fakeClient) List(_ context.Context, all bool) ([]tree.Container, error) {
	if all {
		return append(append([]tree.Container{}, f.running...), f.all...), nil
	}
	return f.running, nil
}

func (f *fakeClient) Events(context.Context) (<-chan struct{}, <-chan error) {
	return f.events, f.eventErrs
}

func (f *fakeClient) Logs(_ context.Context, _ string, opts docker.LogOptions) (<-chan docker.LogLine, error) {
	f.logOpts = opts
	ch := make(chan docker.LogLine, len(f.logs))
	for _, l := range f.logs {
		ch <- l
	}
	close(ch)
	return ch, nil
}

func (f *fakeClient) Inspect(context.Context, string) ([]byte, error) {
	return []byte(f.inspect), nil
}

// run executes cmd and feeds resulting messages back into the model until
// no commands remain. Ticks and blocking event waits are skipped.
func run(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for steps := 0; len(queue) > 0; steps++ {
		if steps > 100 {
			t.Fatal("too many steps")
		}
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		msg := runWithTimeout(c)
		switch msg := msg.(type) {
		case nil:
		case tea.BatchMsg:
			queue = append(queue, msg...)
		default:
			_, next := m.Update(msg)
			queue = append(queue, next)
		}
	}
}

// runWithTimeout returns nil for commands that block (event waits, ticks).
func runWithTimeout(c tea.Cmd) tea.Msg {
	done := make(chan tea.Msg, 1)
	go func() { done <- c() }()
	select {
	case msg := <-done:
		return msg
	case <-timeAfter():
		return nil
	}
}

func press(t *testing.T, m *Model, keys ...string) {
	t.Helper()
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "backspace":
			msg = tea.KeyMsg{Type: tea.KeyBackspace}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "up":
			msg = tea.KeyMsg{Type: tea.KeyUp}
		case "left":
			msg = tea.KeyMsg{Type: tea.KeyLeft}
		case "right":
			msg = tea.KeyMsg{Type: tea.KeyRight}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		_, cmd := m.Update(msg)
		run(t, m, cmd)
	}
}

func setup(t *testing.T) (*Model, *fakeClient) {
	t.Helper()
	f := newFake()
	m := New(f, false)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	run(t, m, m.Init())
	return m, f
}

func screen(m *Model) string { return ansi.Strip(m.View()) }

func TestTreeRendersProjectsAndContainers(t *testing.T) {
	m, _ := setup(t)
	s := screen(m)
	for _, want := range []string{"▾ shop", "2/2 running", "api", "shop:dev", "8080->8080", "▾ (standalone)", "buildkit", "connected"} {
		if !strings.Contains(s, want) {
			t.Errorf("screen missing %q:\n%s", want, s)
		}
	}
	if strings.Index(s, "shop") > strings.Index(s, "(standalone)") {
		t.Error("standalone should be listed last")
	}
}

func TestVimAndArrowNavigation(t *testing.T) {
	m, _ := setup(t)
	press(t, m, "j", "down")
	if c := m.tree.SelectedContainer(); c == nil || c.ID != "a2" {
		t.Fatalf("selected %+v, want a2", c)
	}
	press(t, m, "k")
	press(t, m, "h") // container -> parent
	if m.tree.SelectedContainer() != nil {
		t.Fatal("h on container should move to project")
	}
	press(t, m, "left") // collapse
	if !m.tree.IsCollapsed("shop") || !strings.Contains(screen(m), "▸ shop") {
		t.Fatalf("shop not collapsed:\n%s", screen(m))
	}
	press(t, m, "l")
	press(t, m, "G")
	if c := m.tree.SelectedContainer(); c == nil || c.ID != "s1" {
		t.Fatalf("G selected %+v", c)
	}
	press(t, m, "g", "enter") // toggle project
	if !m.tree.IsCollapsed("shop") {
		t.Fatal("enter on project should toggle")
	}
}

func TestActionsOnProjectRowShowStatus(t *testing.T) {
	m, _ := setup(t)
	for _, k := range []string{"L", "i", "s"} {
		press(t, m, k)
		if m.mode != modeTree || !strings.Contains(screen(m), "select a container first") {
			t.Fatalf("%s on project: mode %v\n%s", k, m.mode, screen(m))
		}
	}
}

func TestLogsViewer(t *testing.T) {
	m, f := setup(t)
	f.logs = []docker.LogLine{{Text: "hello"}, {Text: "boom", Stderr: true}}
	press(t, m, "j", "L")
	if m.mode != modeViewer || m.viewer.kind != viewLogs {
		t.Fatal("logs viewer not open")
	}
	if f.logOpts != (docker.LogOptions{Tail: 500, Follow: true}) {
		t.Fatalf("log options %+v", f.logOpts)
	}
	s := screen(m)
	for _, want := range []string{"logs › shop/api", "hello", "boom", "container exited", "FOLLOWING"} {
		if !strings.Contains(s, want) {
			t.Errorf("screen missing %q:\n%s", want, s)
		}
	}

	press(t, m, "t")
	if !f.logOpts.Timestamps {
		t.Fatal("t should restart logs with timestamps")
	}
	press(t, m, "q")
	if m.mode != modeTree || m.viewer != nil {
		t.Fatal("q should return to tree")
	}
	press(t, m, " ") // space on container opens logs
	if m.mode != modeViewer {
		t.Fatal("space on container should open logs")
	}
	press(t, m, "esc")
	if m.mode != modeTree {
		t.Fatal("esc should return to tree")
	}
}

func TestStoppedContainerLogsDoNotFollow(t *testing.T) {
	m, f := setup(t)
	f.all = []tree.Container{{ID: "x1", Name: "shop-worker-1", Project: "shop", Service: "worker", State: "exited", Status: "Exited (1) 5 minutes ago"}}
	press(t, m, "a")
	if !strings.Contains(screen(m), "worker") || !strings.Contains(screen(m), "all containers") {
		t.Fatalf("a should show stopped containers:\n%s", screen(m))
	}
	press(t, m, "j", "j", "j", "L") // shop, api, db, worker
	if f.logOpts.Follow {
		t.Fatal("stopped container logs should not follow")
	}
	if !strings.Contains(screen(m), "PAUSED") {
		t.Fatalf("expected PAUSED:\n%s", screen(m))
	}
	press(t, m, "q", "s")
	if !strings.Contains(screen(m), "worker is not running") {
		t.Fatalf("shell on stopped container:\n%s", screen(m))
	}
	m.status = ""
	press(t, m, "enter") // enter on a container opens a shell
	if m.mode != modeTree || !strings.Contains(screen(m), "worker is not running") {
		t.Fatalf("enter on container should try a shell:\n%s", screen(m))
	}
	press(t, m, "a")
	if len(m.tree.Rows()) != 5 { // shop, api, db, (standalone), buildkit
		t.Fatal("a again should hide stopped containers")
	}
}

func TestInspectViewerShowsYAML(t *testing.T) {
	m, _ := setup(t)
	press(t, m, "j", "i")
	s := screen(m)
	for _, want := range []string{"inspect › shop/api", "Id: a1", "State:", "  Running: true"} {
		if !strings.Contains(s, want) {
			t.Errorf("screen missing %q:\n%s", want, s)
		}
	}
}

func TestStaleListResultIgnored(t *testing.T) {
	m, _ := setup(t)
	m.Update(containersMsg{all: true, containers: nil})
	if len(m.tree.Rows()) == 0 {
		t.Fatal("result for a different scope should be ignored")
	}
}

func TestEventTriggersRefresh(t *testing.T) {
	m, f := setup(t)
	f.running = f.running[:1]
	_, cmd := m.Update(eventMsg{gen: m.eventsGen})
	if !m.refreshPending {
		t.Fatal("event should schedule a refresh")
	}
	m.Update(eventMsg{gen: m.eventsGen}) // coalesced
	run(t, m, cmd)
	_, cmd = m.Update(refreshMsg{})
	run(t, m, cmd)
	if strings.Contains(screen(m), "buildkit") {
		t.Fatalf("refresh did not apply:\n%s", screen(m))
	}
	m.Update(eventsErrMsg{gen: m.eventsGen})
	if m.connected || !strings.Contains(screen(m), "disconnected") {
		t.Fatal("events error should mark disconnected")
	}
}

func TestHelpOverlay(t *testing.T) {
	m, _ := setup(t)
	press(t, m, "?")
	if !strings.Contains(screen(m), "toggle running / all containers") {
		t.Fatal("help not shown")
	}
	press(t, m, "x")
	if m.help {
		t.Fatal("any key should close help")
	}
}

func TestViewerFollowSearchAndTrim(t *testing.T) {
	v := newViewer(viewLogs, "t", 40, 5)
	v.follow = true
	var lines []docker.LogLine
	for i := 0; i < 20; i++ {
		lines = append(lines, docker.LogLine{Text: fmt.Sprintf("line %d", i)})
	}
	v.appendLogs(lines)
	if v.offset != 15 {
		t.Fatalf("follow offset %d, want 15", v.offset)
	}
	v.update("k")
	if v.follow {
		t.Fatal("scrolling up should pause follow")
	}
	v.appendLogs([]docker.LogLine{{Text: "more"}})
	if v.offset != 14 {
		t.Fatalf("paused offset moved to %d", v.offset)
	}
	v.update("G")
	if !v.follow || v.offset != v.maxOffset() {
		t.Fatal("G should resume follow at bottom")
	}

	for _, k := range []string{"/", "l", "i", "n", "e", " ", "3", "enter"} {
		v.update(k)
	}
	if v.match != 3 || v.follow {
		t.Fatalf("search match %d follow %v", v.match, v.follow)
	}
	v.update("n")
	if v.match != 3 { // only one match, wraps to itself
		t.Fatalf("n match %d", v.match)
	}
	for _, k := range []string{"/", "z", "z", "enter"} {
		v.update(k)
	}
	if v.match != -1 || !strings.Contains(v.footer(), "pattern not found") {
		t.Fatal("expected not found")
	}

	big := make([]docker.LogLine, maxLogLines)
	v.appendLogs(big)
	if len(v.lines) != maxLogLines {
		t.Fatalf("buffer %d lines, want %d", len(v.lines), maxLogLines)
	}
}

func TestViewerWrap(t *testing.T) {
	v := newViewer(viewInspect, "t", 10, 3)
	v.setContent([]string{strings.Repeat("x", 25), "short"})
	v.update("w")
	if v.maxOffset() != 1 { // 3 rows for the long line + 1 > 3
		t.Fatalf("wrapped maxOffset %d", v.maxOffset())
	}
	rows := strings.Split(ansi.Strip(v.view()), "\n")
	if len(rows) != 5 || rows[1] != strings.Repeat("x", 10) {
		t.Fatalf("wrapped view %q", rows)
	}
}

func TestFilterByName(t *testing.T) {
	m, f := setup(t)
	f.running = append(f.running, tree.Container{ID: "k1", Name: "jk-tool", Image: "x", State: "running", Status: "Up"})
	press(t, m, "r")

	press(t, m, "/", "d", "b")
	s := screen(m)
	if !m.filtering || !strings.Contains(s, "/db█") || !strings.Contains(s, "filter: db") {
		t.Fatalf("filter input not shown:\n%s", s)
	}
	if strings.Contains(s, "buildkit") || !strings.Contains(s, "1/2 matching") {
		t.Fatalf("filter not applied:\n%s", s)
	}
	if c := m.tree.SelectedContainer(); c == nil || c.ID != "a2" {
		t.Fatalf("cursor should jump to first match, got %+v", c)
	}

	press(t, m, "enter", "L") // keep filter, act on the match
	if m.mode != modeViewer || m.target.ID != "a2" {
		t.Fatal("L after filtering should open logs for the match")
	}
	press(t, m, "q")
	if m.tree.Filter() != "db" {
		t.Fatal("filter should persist after closing the viewer")
	}

	press(t, m, "esc")
	if m.tree.Filter() != "" || !strings.Contains(screen(m), "buildkit") {
		t.Fatal("esc should clear the filter")
	}

	press(t, m, "f", "f") // f also starts filtering; then it is text
	if !m.filtering || m.tree.Filter() != "f" {
		t.Fatalf("f: filtering %v filter %q", m.filtering, m.tree.Filter())
	}
	press(t, m, "esc")

	press(t, m, "/", "j", "k") // vim keys are text while typing
	if m.tree.Filter() != "jk" {
		t.Fatalf("filter = %q, want jk", m.tree.Filter())
	}
	press(t, m, "backspace", "backspace", "x", "y", "z")
	if !strings.Contains(screen(m), `No containers match "xyz"`) {
		t.Fatalf("empty result message missing:\n%s", screen(m))
	}
	press(t, m, "esc")
	if m.filtering || m.tree.Filter() != "" {
		t.Fatal("esc while typing should clear and leave filter mode")
	}
}

func TestMultiRuneInput(t *testing.T) {
	m, _ := setup(t)
	press(t, m, "/")
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("bu")})
	m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")})
	if m.tree.Filter() != "bu " {
		t.Fatalf("filter = %q, want %q", m.tree.Filter(), "bu ")
	}
	press(t, m, "esc", "j", "L", "/")
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("oops 3"), Paste: true})
	if m.viewer.input != "oops 3" {
		t.Fatalf("search input = %q", m.viewer.input)
	}
}
