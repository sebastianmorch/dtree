package ui

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/sebastianmorch/dtree/internal/config"
	"github.com/sebastianmorch/dtree/internal/docker"
	"github.com/sebastianmorch/dtree/internal/tree"
)

type fakeClient struct {
	running, all []tree.Container
	logs         []docker.LogLine
	logOpts      docker.LogOptions
	inspect      string
	inspects     int
	events       chan struct{}
	eventErrs    chan error

	execOut   string // raw terminal output of each command
	execExit  int
	execHold  chan struct{} // when set, commands run until it is closed or stopped
	ignoreInt bool          // commands ignore Ctrl+C
	execs     []string      // "id: command" of each Exec call

	mu         sync.Mutex
	interrupts []bool // force flag of each Interrupt call
	input      []byte // everything typed into commands
}

// typed waits briefly for input to reach the fake, then returns it.
func (f *fakeClient) typed(want int) string {
	for range 20 {
		f.mu.Lock()
		got := string(f.input)
		f.mu.Unlock()
		if len(got) >= want {
			return got
		}
		<-timeAfter()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return string(f.input)
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
	f.inspects++
	return []byte(f.inspect), nil
}

func (f *fakeClient) Exec(_ context.Context, id string, opts docker.ExecOptions) (*docker.ExecSession, error) {
	f.execs = append(f.execs, id+": "+opts.Command)
	lines := make(chan []byte, 1)
	result := make(chan docker.ExecResult, 1)
	if f.execOut != "" {
		lines <- []byte(f.execOut)
	}
	hold, exit, ignoreInt := f.execHold, f.execExit, f.ignoreInt
	stop := make(chan int, 2)
	go func() {
		if hold != nil {
			select {
			case <-hold:
			case exit = <-stop:
			}
		}
		close(lines)
		result <- docker.ExecResult{ExitCode: exit}
	}()
	interrupt := func(force bool) error {
		f.mu.Lock()
		f.interrupts = append(f.interrupts, force)
		f.mu.Unlock()
		switch {
		case force:
			stop <- 137
		case !ignoreInt:
			stop <- 130
		}
		return nil
	}
	input := func(p []byte) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.input = append(f.input, p...)
		return nil
	}
	return &docker.ExecSession{Output: lines, Input: input, Result: result, Interrupt: interrupt}, nil
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

// late holds the results of commands that blocked past runWithTimeout, for
// settle. setup resets it so no message reaches another test's model.
var late []chan tea.Msg

// runWithTimeout returns nil for commands that block (event waits, ticks).
func runWithTimeout(c tea.Cmd) tea.Msg {
	done := make(chan tea.Msg, 1)
	go func() { done <- c() }()
	select {
	case msg := <-done:
		return msg
	case <-timeAfter():
		late = append(late, done)
		return nil
	}
}

// settle delivers results of blocked commands that have finished since, such
// as output reads of a command that was stopped.
func settle(t *testing.T, m *Model) {
	t.Helper()
	for progress := true; progress; {
		progress = false
		pending := late
		late = nil
		for _, ch := range pending {
			select {
			case msg := <-ch:
				progress = true
				run(t, m, func() tea.Msg { return msg })
			case <-timeAfter():
				late = append(late, ch)
			}
		}
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
		case "ctrl+c":
			msg = tea.KeyMsg{Type: tea.KeyCtrlC}
		case "ctrl+s":
			msg = tea.KeyMsg{Type: tea.KeyCtrlS}
		case "tab":
			msg = tea.KeyMsg{Type: tea.KeyTab}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		_, cmd := m.Update(msg)
		run(t, m, cmd)
	}
}

func setup(t *testing.T) (*Model, *fakeClient) {
	t.Helper()
	late = nil
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
	for _, want := range []string{"▾ shop", "2/2 running", "api", "shop:dev", "2h", "▾ (standalone)", "buildkit", "connected"} {
		if !strings.Contains(s, want) {
			t.Errorf("screen missing %q:\n%s", want, s)
		}
	}
	if strings.Index(s, "shop") > strings.Index(s, "(standalone)") {
		t.Error("standalone should be listed last")
	}
	if strings.Contains(s, "8080->8080") || strings.Contains(s, "status") {
		t.Errorf("details should only show for a selected container:\n%s", s)
	}
}

func TestInfoPanelForContainer(t *testing.T) {
	m, f := setup(t)
	f.running[0].Image = "594489062793.dkr.ecr.eu-central-1.amazonaws.com/shop:dev"
	f.running[0].Status = "Up 2 hours (healthy)"
	press(t, m, "r", "j")
	s := screen(m)
	for _, want := range []string{
		"┃   ● api ",
		"2h ✅",
		"─ api ─",
		"│ name        shop-api-1 · a1",
		" image       594489062793.dkr.ecr.eu-central-1.amazonaws.com/shop:dev",
		" status      Up 2 hours (healthy)",
		" ports       8080->8080",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("screen missing %q:\n%s", want, s)
		}
	}
	// The panel sits below the list, directly above the footer.
	lines := strings.Split(s, "\n")
	if !strings.HasPrefix(lines[len(lines)-3], "│ ports") {
		t.Errorf("panel should end right above the footer:\n%s", s)
	}
	press(t, m, "j") // db has no ports
	s = screen(m)
	if !strings.Contains(s, "─ db ─") || !strings.Contains(s, "shop-db-1") || strings.Contains(s, " ports ") {
		t.Errorf("panel did not follow the cursor to db:\n%s", s)
	}
}

func TestInfoPanelShowsInspectDetails(t *testing.T) {
	m, f := setup(t)
	f.inspect = `{"Config":{"Entrypoint":["docker-entrypoint.sh"],"Cmd":["sh","-c","npm run start"],"WorkingDir":"/app"}}`
	m.details = map[string]*containerDetails{}
	press(t, m, "j")
	s := screen(m)
	for _, want := range []string{
		"│ entrypoint  docker-entrypoint.sh",
		"│ cmd         sh -c 'npm run start'",
		"│ workdir     /app",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("screen missing %q:\n%s", want, s)
		}
	}
	n := f.inspects
	press(t, m, "j", "k") // back to api: cached, only db is fetched
	if f.inspects != n+1 {
		t.Errorf("inspected %d times, want %d", f.inspects, n+1)
	}
}

func TestShellJoin(t *testing.T) {
	got := shellJoin([]string{"sh", "-c", "echo 'hi' && run", "", "--port=80"})
	if want := `sh -c 'echo '\''hi'\'' && run' '' --port=80`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestInfoPanelForProject(t *testing.T) {
	m, f := setup(t)
	home, _ := os.UserHomeDir()
	for i := range f.running[:2] {
		f.running[i].WorkingDir = home + "/code/shop"
		f.running[i].ConfigFiles = []string{home + "/code/shop/docker-compose.yml"}
		f.running[i].Networks = []string{"shop_default"}
	}
	f.running[0].HostPorts = []int{8081, 8080, 8082, 443}
	f.running[0].Status = "Up 2 hours (healthy)"
	f.running[1].Volumes = []string{"shop_db"}
	f.all = []tree.Container{{ID: "x1", Name: "shop-worker-1", Project: "shop", Service: "worker", State: "exited", Status: "Exited (1) 5 minutes ago"}}
	press(t, m, "a") // include the exited container
	s := screen(m)
	for _, want := range []string{
		"─ shop ─",
		" dir         ~/code/shop",
		" file        docker-compose.yml",
		" state       2 running · 1 exited · 1 ✅",
		" ports       443, 8080-8082",
		" network     shop_default",
		" volumes     shop_db",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("screen missing %q:\n%s", want, s)
		}
	}
	press(t, m, "G", "h") // standalone project: no dir, file or volumes
	s = screen(m)
	if !strings.Contains(s, "─ (standalone) ─") || strings.Contains(s, " dir ") || !strings.Contains(s, " state       1 running") {
		t.Errorf("standalone panel:\n%s", s)
	}
}

func TestInfoPanelLimits(t *testing.T) {
	m, f := setup(t)
	f.running[0].Ports = strings.Repeat("8080->80, ", 40)
	press(t, m, "r", "j")
	lines := strings.Split(screen(m), "\n")
	if len(lines) != 20 {
		t.Fatalf("screen has %d lines, want 20", len(lines))
	}
	panel := 0
	for i, l := range lines {
		if strings.HasPrefix(l, "╭─ api") {
			panel = len(lines) - 1 - i
		}
	}
	if panel == 0 || panel > 9 { // 19 rows above the hint bar, the panel gets at most half
		t.Fatalf("panel takes %d rows:\n%s", panel, strings.Join(lines, "\n"))
	}
	if !strings.Contains(screen(m), "┃   ● api") {
		t.Fatal("selected row must stay visible above the panel")
	}

	m.Update(tea.WindowSizeMsg{Width: 100, Height: 10})
	if strings.Contains(screen(m), "─ api") {
		t.Fatal("panel should be hidden on very short terminals")
	}
}

func TestNarrowLayoutKeepsImageAndWrapsInfo(t *testing.T) {
	m, f := setup(t)
	f.running[0].Image = "594489062793.dkr.ecr.eu-central-1.amazonaws.com/test-authorization-service:branch_master"
	f.running[0].Name = "tracking-code-creator-worker-local"
	f.running[0].Ports = "80->80, 443->443, 6006->6006, 8081-8083->8081-8083"
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 24})
	press(t, m, "r", "j")
	lines := strings.Split(screen(m), "\n")
	for _, l := range lines {
		if w := ansi.StringWidth(l); w > 60 {
			t.Errorf("line wider than terminal (%d): %q", w, l)
		}
	}
	s := strings.Join(lines, "\n")
	for _, want := range []string{"8081-8083->8081-8083", "test-authorization-service:branch_master"} {
		if !strings.Contains(s, want) {
			t.Errorf("screen missing %q:\n%s", want, s)
		}
	}
}

func TestScrollKeepsCursorVisible(t *testing.T) {
	m, f := setup(t)
	for i := 0; i < 30; i++ {
		f.running = append(f.running, tree.Container{
			ID: fmt.Sprintf("x%02d", i), Name: fmt.Sprintf("svc-%02d", i), Image: "img", State: "running", Status: "Up 1 hour",
		})
	}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 16})
	press(t, m, "r", "G")
	s := screen(m)
	if len(strings.Split(s, "\n")) != 16 {
		t.Fatalf("screen height wrong:\n%s", s)
	}
	if !strings.Contains(s, "┃   ● svc-29") || !strings.Contains(s, "─ svc-29") {
		t.Fatalf("selected row and panel not visible:\n%s", s)
	}
	press(t, m, "g")
	if !strings.Contains(screen(m), "▾ shop") {
		t.Fatalf("g should scroll back to top:\n%s", screen(m))
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
	if len(rows) != 6 || rows[1] != "│ "+strings.Repeat("x", 10)+" │" { // border, 3 rows, border, hints
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

func TestClearScreenExecClearsBeforeRunning(t *testing.T) {
	var out strings.Builder
	c := &clearScreenExec{Cmd: exec.Command("echo", "hi")}
	c.SetStdout(&out)
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	if out.String() != clearScreen+"hi\n" {
		t.Fatalf("output %q", out.String())
	}
}

func TestClearScreenExecPausesAfterCommand(t *testing.T) {
	var out strings.Builder
	// A file, like the terminal: the command inherits it instead of exec
	// copying all of it into the command.
	in, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	_, _ = w.WriteString("xy\rrest")
	w.Close()
	c := &clearScreenExec{Cmd: exec.Command("sh", "-c", "echo hi; exit 3"), pause: true}
	c.SetStdout(&out)
	c.SetStdin(in)
	if err := c.Run(); err == nil {
		t.Fatal("expected the exit error")
	}
	if got := ansi.Strip(out.String()); !strings.Contains(got, "hi\n\r\n── exited with code 3 · press enter to return to dtree ──") {
		t.Fatalf("output %q", got)
	}
	if rest, _ := io.ReadAll(in); string(rest) != "rest" {
		t.Fatalf("should read up to enter, left %q", rest)
	}
}

func TestShellArgs(t *testing.T) {
	for _, tc := range []struct {
		cmd  string
		stay bool
		want string
	}{
		{"", false, shellScript},
		{"top", false, "top"},
		{"cd /app # go", true, "cd /app # go\n" + shellScript},
	} {
		got := shellArgs("a1", tc.cmd, tc.stay)
		want := []string{"exec", "-it", "a1", "sh", "-c", tc.want}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("shellArgs(%q, %v) = %q", tc.cmd, tc.stay, got)
		}
	}
}

func TestPromptRunsInTerminal(t *testing.T) {
	m, f := setup(t)
	press(t, m, "j", "x")
	typeText(m, "top")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	if cmd == nil || m.prompting || len(f.execs) != 0 {
		t.Fatalf("ctrl+o should hand the terminal to the command, not the popup (execs %q)", f.execs)
	}
	press(t, m, "x", "down")
	if m.promptText != "top" {
		t.Fatalf("ctrl+o should add to the history, got %q", m.promptText)
	}
	press(t, m, "esc")

	m.Update(execDoneMsg{name: "api", cmd: "top", err: exitError(t, 2)})
	if s := screen(m); !strings.Contains(s, "top in api exited with code 2") {
		t.Fatalf("exit code not shown:\n%s", s)
	}
}

func exitError(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
	if err == nil {
		t.Fatal("expected an exit error")
	}
	return err
}

func typeText(m *Model, text string) {
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)})
}

func TestQuickCommand(t *testing.T) {
	m, f := setup(t)
	f.execOut = "total 0\r\nls: oops\r\n"
	f.execExit = 2
	press(t, m, "j", "x")
	if !m.prompting || !strings.Contains(screen(m), "╭─ run › shop/api ─") || !strings.Contains(screen(m), "│ $ █") {
		t.Fatalf("prompt not shown:\n%s", screen(m))
	}
	typeText(m, "ls -la")
	press(t, m, "j") // typed, not a move
	if m.promptText != "ls -laj" {
		t.Fatalf("prompt text %q", m.promptText)
	}
	typeText(m, strings.Repeat("y", 100)) // longer than the box: the end stays visible
	s := screen(m)
	if !strings.Contains(s, "│ $ …yyy") || !strings.Contains(s, "yyy█ │") {
		t.Fatalf("long input should scroll:\n%s", s)
	}
	m.promptText = "ls -laj"
	press(t, m, "backspace", "enter")
	if len(f.execs) != 1 || f.execs[0] != "a1: ls -la" {
		t.Fatalf("execs %q", f.execs)
	}
	s = screen(m)
	for _, want := range []string{"run › shop/api $ ls -la", "total 0", "ls: oops", "exited with code 2", "✗ EXIT 2"} {
		if !strings.Contains(s, want) {
			t.Errorf("popup missing %q:\n%s", want, s)
		}
	}
	if v := m.popupViewer(); v.renderLine(1) != "ls: oops" {
		t.Errorf("stderr in command output should not be coloured: %q", v.renderLine(1))
	}
	for _, l := range strings.Split(s, "\n") {
		if w := ansi.StringWidth(l); w > 100 {
			t.Errorf("line wider than terminal (%d): %q", w, l)
		}
	}

	press(t, m, "q")
	if m.popup != "" || !strings.Contains(screen(m), "✗ ls -la") {
		t.Fatalf("closed popup should leave the status in the list:\n%s", screen(m))
	}
	press(t, m, "X")
	if m.popup != "a1" {
		t.Fatal("X should reopen the output")
	}
	press(t, m, "esc", "j", "x", "down") // the last command is the first suggestion
	if m.promptText != "ls -la" {
		t.Fatalf("history gave %q", m.promptText)
	}
	press(t, m, "esc")
	if m.prompting || len(f.execs) != 1 {
		t.Fatal("esc should cancel the prompt")
	}
}

func TestPromptSuggestions(t *testing.T) {
	m, f := setup(t)
	path := filepath.Join(t.TempDir(), "commands.yaml")
	if err := os.WriteFile(path, []byte("global:\n  - df -h\n  - ps aux\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmds, err := config.LoadCommands(path)
	if err != nil {
		t.Fatal(err)
	}
	m.SetCommands(cmds)
	m.history = []string{"ls", "ps aux"}

	press(t, m, "j", "x")
	s := screen(m)
	// Recent commands first, then saved ones not run yet; saved ones are starred.
	iPs, iLs, iDf := strings.Index(s, "★ ps aux"), strings.Index(s, "  ls "), strings.Index(s, "★ df -h")
	if iPs < 0 || iLs < 0 || iDf < 0 || !(iPs < iLs && iLs < iDf) {
		t.Fatalf("suggestions not shown in order:\n%s", s)
	}

	typeText(m, "D")
	s = screen(m)
	if !strings.Contains(s, "★ df -h") || strings.Contains(s, "ps aux") {
		t.Fatalf("typing should filter (case-insensitively):\n%s", s)
	}
	press(t, m, "tab")
	if m.promptText != "df -h" {
		t.Fatalf("tab completed %q", m.promptText)
	}

	press(t, m, "ctrl+u", "down", "down")
	if m.promptText != "ls" {
		t.Fatalf("down down picked %q", m.promptText)
	}
	press(t, m, "up", "up")
	if m.promptText != "" || m.suggestIdx != -1 {
		t.Fatalf("up past the first suggestion should restore the input, got %q", m.promptText)
	}

	press(t, m, "down", "down", "ctrl+s")
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "- ls") {
		t.Fatalf("ctrl+s should save:\n%s", data)
	}
	if s := screen(m); !strings.Contains(s, "★ ls ") || !strings.Contains(s, "★ saved") {
		t.Fatalf("saved command should be starred:\n%s", s)
	}
	press(t, m, "down", "ctrl+s") // forget df -h, which never ran
	if cmds.Saved("df -h") || m.suggestIdx != -1 || m.promptText != "df -h" {
		t.Fatalf("forget: saved=%v idx=%d text=%q", cmds.Saved("df -h"), m.suggestIdx, m.promptText)
	}
	press(t, m, "enter")
	if len(f.execs) != 1 || f.execs[0] != "a1: df -h" {
		t.Fatalf("execs %q", f.execs)
	}
}

func TestPromptSaveError(t *testing.T) {
	m, _ := setup(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "commands.yaml")
	cmds, _ := config.LoadCommands(path)
	m.SetCommands(cmds)
	if err := os.WriteFile(path, []byte("global: [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	press(t, m, "j", "x")
	typeText(m, "df -h")
	press(t, m, "ctrl+s")
	if !strings.Contains(screen(m), "could not save") || cmds.Saved("df -h") {
		t.Fatalf("save error not shown:\n%s", screen(m))
	}
}

func TestQuickCommandKeepsRunningInBackground(t *testing.T) {
	m, f := setup(t)
	f.execHold = make(chan struct{})
	defer close(f.execHold)
	press(t, m, "j", "x")
	typeText(m, "sleep 9")
	press(t, m, "enter")
	if !strings.Contains(screen(m), "RUNNING") {
		t.Fatalf("popup should show the command running:\n%s", screen(m))
	}
	press(t, m, "q", "j", "x") // run another command on db meanwhile
	typeText(m, "true")
	press(t, m, "enter", "q")
	s := screen(m)
	if !strings.Contains(s, "⟳ sleep 9") || !strings.Contains(s, "⟳ true") {
		t.Fatalf("both commands should show as running:\n%s", s)
	}
	press(t, m, "k", "x")
	if m.prompting || m.popup != "a1" {
		t.Fatal("x on a container with a running command should show its popup")
	}
}

func TestQuickCommandFinishedInBackground(t *testing.T) {
	m, f := setup(t)
	f.execHold = make(chan struct{})
	press(t, m, "j", "x")
	typeText(m, "make")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	press(t, m, "q") // close the popup before the command ends
	close(f.execHold)
	run(t, m, cmd)
	s := screen(m)
	if !strings.Contains(s, "✓ make") || !strings.Contains(s, "make in api finished") {
		t.Fatalf("finished command should be reported:\n%s", s)
	}
}

func TestQuickCommandOnStoppedOrProject(t *testing.T) {
	m, f := setup(t)
	press(t, m, "x")
	if m.prompting || !strings.Contains(screen(m), "select a container first") {
		t.Fatalf("x on project:\n%s", screen(m))
	}
	f.all = []tree.Container{{ID: "x1", Name: "shop-worker-1", Project: "shop", Service: "worker", State: "exited", Status: "Exited (1) 5 minutes ago"}}
	press(t, m, "a", "j", "j", "j", "x")
	if m.prompting || !strings.Contains(screen(m), "worker is not running") {
		t.Fatalf("x on stopped container:\n%s", screen(m))
	}
	press(t, m, "X")
	if !strings.Contains(screen(m), "no command has run in worker") {
		t.Fatalf("X without output:\n%s", screen(m))
	}
}

func TestStopQuickCommand(t *testing.T) {
	m, f := setup(t)
	f.execHold = make(chan struct{})
	defer close(f.execHold)
	f.ignoreInt = true
	press(t, m, "j", "x")
	typeText(m, "sleep 9")
	press(t, m, "enter")
	if s := screen(m); !strings.Contains(s, "^c stop") {
		t.Fatalf("popup should offer to stop the command:\n%s", s)
	}
	press(t, m, "ctrl+c")
	if s := screen(m); !strings.Contains(s, "STOPPING") || !strings.Contains(s, "^c kill") {
		t.Fatalf("ctrl+c should interrupt and offer to kill:\n%s", s)
	}
	press(t, m, "ctrl+c") // ignored Ctrl+C, so kill it
	settle(t, m)
	s := screen(m)
	for _, want := range []string{"■ STOPPED", "stopped (exit code 137)"} {
		if !strings.Contains(s, want) {
			t.Errorf("popup missing %q:\n%s", want, s)
		}
	}
	if fmt.Sprint(f.interrupts) != "[false true]" {
		t.Fatalf("interrupts %v, want ctrl+c then kill", f.interrupts)
	}
	press(t, m, "q")
	if !strings.Contains(screen(m), "■ sleep 9") {
		t.Fatalf("list should show the command as stopped:\n%s", screen(m))
	}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC}) // nothing running: quits
	if cmd == nil || cmd() != tea.Quit() {
		t.Fatal("ctrl+c without a running command should quit")
	}
}

func TestInputMode(t *testing.T) {
	m, f := setup(t)
	f.execHold = make(chan struct{})
	f.execOut = "Continue? [Y/n] "
	press(t, m, "j", "x")
	typeText(m, "apt install vim")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	run(t, m, cmd)
	if s := screen(m); !strings.Contains(s, "i input") || strings.Contains(s, "INPUT") {
		t.Fatalf("navigation mode should offer input mode:\n%s", s)
	}
	press(t, m, "j") // navigation: scrolls, not sent
	press(t, m, "i")
	s := screen(m)
	for _, want := range []string{"⌨ INPUT", "keys go to the command", "esc scroll mode", "Continue? [Y/n]"} {
		if !strings.Contains(s, want) {
			t.Errorf("input mode missing %q:\n%s", want, s)
		}
	}
	if !strings.Contains(m.View(), "Continue? [Y/n] \x1b[7m \x1b[27m") {
		t.Errorf("cursor should be drawn after the prompt:\n%q", m.View())
	}
	press(t, m, "y", "enter", "q", "up", "backspace")
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("pasted text"), Paste: true})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b"), Alt: true})
	want := "y\rq\x1b[A\x7f\x04pasted text\x1bb"
	if got := f.typed(len(want)); got != want {
		t.Fatalf("typed %q, want %q", got, want)
	}
	if m.popup == "" {
		t.Fatal("q in input mode must go to the command, not close the popup")
	}

	press(t, m, "esc")
	if s := screen(m); strings.Contains(s, "INPUT") || strings.Contains(m.View(), "\x1b[7m \x1b[27m") {
		t.Fatalf("esc should return to navigation and hide the cursor:\n%s", s)
	}
	press(t, m, "k")
	if got := f.typed(len(want) + 1); got != want {
		t.Fatalf("navigation keys must not be sent, typed %q", got)
	}

	press(t, m, "i")
	close(f.execHold) // the command ends while typing
	settle(t, m)
	if m.jobs["a1"].typing || strings.Contains(screen(m), "INPUT") {
		t.Fatalf("input mode should end with the command:\n%s", screen(m))
	}
	press(t, m, "i", "q") // nothing to type into: i does nothing, q closes
	if m.popup != "" {
		t.Fatal("q should close the popup of a finished command")
	}
}

func TestKeyBytes(t *testing.T) {
	for _, tc := range []struct {
		msg  tea.KeyMsg
		want string
	}{
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("æ")}, "æ"},
		{tea.KeyMsg{Type: tea.KeySpace}, " "},
		{tea.KeyMsg{Type: tea.KeyTab}, "\t"},
		{tea.KeyMsg{Type: tea.KeyCtrlA}, "\x01"},
		{tea.KeyMsg{Type: tea.KeyLeft, Alt: true}, "\x1b\x1b[D"},
		{tea.KeyMsg{Type: tea.KeyDelete}, "\x1b[3~"},
		{tea.KeyMsg{Type: tea.KeyF12}, ""},
	} {
		if got := string(keyBytes(tc.msg)); got != tc.want {
			t.Errorf("%v: got %q, want %q", tc.msg, got, tc.want)
		}
	}
}

func TestFilterFromCommandLine(t *testing.T) {
	late = nil
	f := newFake()
	m := New(f, false)
	m.SetFilter("db")
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	run(t, m, m.Init())

	s := screen(m)
	if m.filtering || !strings.Contains(s, "filter: db") || strings.Contains(s, "buildkit") {
		t.Fatalf("filter from the command line not applied:\n%s", s)
	}
	if c := m.tree.SelectedContainer(); c == nil || c.ID != "a2" {
		t.Fatalf("cursor should start on the first match, got %+v", c)
	}
	press(t, m, "esc")
	if m.tree.Filter() != "" || !strings.Contains(screen(m), "buildkit") {
		t.Fatal("esc should clear a filter from the command line")
	}
}
