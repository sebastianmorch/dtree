// Package ui is the Bubble Tea interface: a project/container tree plus
// full-screen log and inspect viewers.
package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"dtree/internal/docker"
	"dtree/internal/tree"
)

const (
	logTail        = 500
	refreshDelay   = 150 * time.Millisecond // coalesces bursts of container events
	maxBackoff     = 30 * time.Second
	statusDuration = 4 * time.Second
	requestTimeout = 10 * time.Second

	// Prefer bash, fall back to sh, in a single exec.
	shellScript = "command -v bash >/dev/null 2>&1 && exec bash || exec sh"
)

type mode int

const (
	modeTree mode = iota
	modeViewer
)

type (
	containersMsg struct {
		containers []tree.Container
		all        bool
		err        error
	}
	eventMsg       struct{ gen int }
	eventsErrMsg   struct{ gen int }
	reconnectMsg   struct{}
	refreshMsg     struct{}
	clearStatusMsg struct{ id int }
	logStreamMsg   struct {
		gen int
		ch  <-chan docker.LogLine
	}
	logBatchMsg struct {
		gen   int
		ch    <-chan docker.LogLine
		lines []docker.LogLine
		done  bool
		err   error
	}
	inspectMsg struct {
		id    string
		lines []string
		err   error
	}
	execDoneMsg struct {
		name   string
		err    error
		stderr string
	}
)

// Model is the root Bubble Tea model.
type Model struct {
	client  docker.Client
	tree    *tree.Tree
	showAll bool
	loaded  bool
	listErr error

	width, height int
	treeOffset    int // first visible tree row
	mode          mode
	viewer        *viewer
	help          bool
	filtering     bool // typing a filter query after /

	status    string
	statusErr bool
	statusID  int

	connected      bool
	eventsGen      int
	eventsCancel   context.CancelFunc
	eventsCh       <-chan struct{}
	eventsErrs     <-chan error
	refreshPending bool
	backoff        time.Duration

	logGen    int
	logCancel context.CancelFunc
	target    tree.Container // container shown in the viewer
}

func New(client docker.Client, showAll bool) *Model {
	return &Model{client: client, tree: tree.New(), showAll: showAll, width: 80, height: 24}
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.list(), m.subscribe())
}

// Close stops background streams.
func (m *Model) Close() {
	if m.eventsCancel != nil {
		m.eventsCancel()
	}
	m.stopLogs()
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width <= 0 || msg.Height <= 0 {
			return m, nil // unknown size, keep the defaults
		}
		m.width, m.height = msg.Width, msg.Height
		if m.viewer != nil {
			m.viewer.setSize(m.width, m.viewerHeight())
		}
		return m, nil

	case tea.KeyMsg:
		return m, m.handleKey(msg)

	case containersMsg:
		if msg.all != m.showAll {
			return m, nil // stale result from before the a toggle
		}
		if msg.err != nil {
			m.listErr = msg.err
			m.connected = false
			return m, nil
		}
		m.loaded, m.listErr, m.connected = true, nil, true
		m.tree.Set(msg.containers)
		return m, nil

	case eventMsg:
		if msg.gen != m.eventsGen {
			return m, nil
		}
		m.connected, m.backoff = true, 0
		cmds := []tea.Cmd{m.waitEvent()}
		if !m.refreshPending {
			m.refreshPending = true
			cmds = append(cmds, tea.Tick(refreshDelay, func(time.Time) tea.Msg { return refreshMsg{} }))
		}
		return m, tea.Batch(cmds...)

	case refreshMsg:
		m.refreshPending = false
		return m, m.list()

	case eventsErrMsg:
		if msg.gen != m.eventsGen {
			return m, nil
		}
		m.connected = false
		m.backoff = min(max(time.Second, m.backoff*2), maxBackoff)
		return m, tea.Tick(m.backoff, func(time.Time) tea.Msg { return reconnectMsg{} })

	case reconnectMsg:
		return m, tea.Batch(m.subscribe(), m.list())

	case clearStatusMsg:
		if msg.id == m.statusID {
			m.status = ""
		}
		return m, nil

	case logStreamMsg:
		if msg.gen != m.logGen || m.viewer == nil {
			return m, nil
		}
		m.viewer.loading = false
		return m, readLogs(msg.gen, msg.ch)

	case logBatchMsg:
		if msg.gen != m.logGen || m.viewer == nil {
			return m, nil
		}
		if len(msg.lines) > 0 {
			m.viewer.appendLogs(msg.lines)
		}
		switch {
		case msg.err != nil:
			m.viewer.appendMarker("── error: " + msg.err.Error() + " ──")
		case msg.done:
			m.viewer.appendMarker("── container exited ──")
		default:
			return m, readLogs(msg.gen, msg.ch)
		}
		return m, nil

	case inspectMsg:
		if m.viewer == nil || m.viewer.kind != viewInspect || m.target.ID != msg.id {
			return m, nil
		}
		if msg.err != nil {
			m.viewer.setContent([]string{styleError.Render("inspect failed: " + msg.err.Error())})
			return m, nil
		}
		m.viewer.setContent(msg.lines)
		return m, nil

	case execDoneMsg:
		return m, tea.Batch(m.execResult(msg), m.list())
	}
	return m, nil
}

func (m *Model) handleKey(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()
	if m.help {
		if key == "ctrl+c" {
			return m.quit()
		}
		m.help = false
		return nil
	}
	if m.mode == modeViewer {
		switch m.viewer.updateKey(msg) {
		case actQuit:
			return m.quit()
		case actClose:
			m.closeViewer()
		case actRestartLogs:
			m.viewer.reset()
			return m.startLogs()
		case actRefetch:
			return m.fetchInspect(m.target.ID)
		}
		return nil
	}
	if m.filtering {
		return m.handleFilterKey(msg)
	}

	switch key {
	case "q", "ctrl+c":
		return m.quit()
	case "/":
		m.filtering = true
	case "esc":
		m.tree.SetFilter("")
	case "j", "down":
		m.tree.Down()
	case "k", "up":
		m.tree.Up()
	case "l", "right":
		m.tree.Expand()
	case "h", "left":
		m.tree.Collapse()
	case "g", "home":
		m.tree.Top()
	case "G", "end":
		m.tree.Bottom()
	case "enter", " ":
		if c := m.tree.SelectedContainer(); c != nil {
			return m.openLogs(*c)
		}
		m.tree.Toggle()
	case "L", "i", "s":
		c := m.tree.SelectedContainer()
		if c == nil {
			return m.setStatus("select a container first", true)
		}
		switch key {
		case "L":
			return m.openLogs(*c)
		case "i":
			return m.openInspect(*c)
		default:
			return m.shell(*c)
		}
	case "a":
		m.showAll = !m.showAll
		return m.list()
	case "r":
		return tea.Batch(m.list(), m.setStatus("refreshed", false))
	case "?":
		m.help = true
	}
	return nil
}

// handleFilterKey edits the filter query; the tree filters as you type.
// Enter keeps the filter, Esc clears it, arrows still move the cursor.
func (m *Model) handleFilterKey(msg tea.KeyMsg) tea.Cmd {
	q := m.tree.Filter()
	switch msg.String() {
	case "ctrl+c":
		return m.quit()
	case "enter":
		m.filtering = false
		return nil
	case "esc":
		m.filtering = false
		m.tree.SetFilter("")
		return nil
	case "down", "ctrl+n":
		m.tree.Down()
		return nil
	case "up", "ctrl+p":
		m.tree.Up()
		return nil
	case "backspace":
		r := []rune(q)
		if len(r) == 0 {
			m.filtering = false
			return nil
		}
		q = string(r[:len(r)-1])
	case "ctrl+u":
		q = ""
	default:
		text, ok := typedText(msg)
		if !ok {
			return nil
		}
		q += text
	}
	m.tree.SetFilter(q)
	m.tree.FirstContainer()
	return nil
}

// typedText returns the printable text of a key press. Fast typing and
// pastes can deliver several runes in one message.
func typedText(msg tea.KeyMsg) (string, bool) {
	if msg.Alt {
		return "", false
	}
	switch msg.Type {
	case tea.KeySpace:
		return " ", true
	case tea.KeyRunes:
		var b strings.Builder
		for _, r := range msg.Runes {
			if r >= 0x20 && r != 0x7f {
				b.WriteRune(r)
			}
		}
		return b.String(), b.Len() > 0
	}
	return "", false
}

func (m *Model) quit() tea.Cmd {
	m.Close()
	return tea.Quit
}

func (m *Model) setStatus(text string, isErr bool) tea.Cmd {
	m.statusID++
	id := m.statusID
	m.status, m.statusErr = text, isErr
	return tea.Tick(statusDuration, func(time.Time) tea.Msg { return clearStatusMsg{id: id} })
}

// ---- docker commands --------------------------------------------------------

func (m *Model) list() tea.Cmd {
	client, all := m.client, m.showAll
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		cs, err := client.List(ctx, all)
		return containersMsg{containers: cs, all: all, err: err}
	}
}

// subscribe (re)starts the events stream. Stale messages from a previous
// stream are ignored by generation number.
func (m *Model) subscribe() tea.Cmd {
	if m.eventsCancel != nil {
		m.eventsCancel()
	}
	m.eventsGen++
	ctx, cancel := context.WithCancel(context.Background())
	m.eventsCancel = cancel
	m.eventsCh, m.eventsErrs = m.client.Events(ctx)
	return m.waitEvent()
}

func (m *Model) waitEvent() tea.Cmd {
	gen, ch, errs := m.eventsGen, m.eventsCh, m.eventsErrs
	return func() tea.Msg {
		select {
		case _, ok := <-ch:
			if !ok {
				return eventsErrMsg{gen: gen}
			}
			return eventMsg{gen: gen}
		case <-errs:
			return eventsErrMsg{gen: gen}
		}
	}
}

func (m *Model) viewerHeight() int { return max(1, m.height-2) }

func (m *Model) openViewer(kind viewKind, c tree.Container) {
	m.stopLogs()
	title := c.DisplayName()
	if c.Project != "" {
		title = c.Project + "/" + title
	}
	m.target = c
	m.viewer = newViewer(kind, title, m.width, m.viewerHeight())
	m.mode = modeViewer
}

func (m *Model) closeViewer() {
	m.stopLogs()
	m.viewer = nil
	m.mode = modeTree
}

func (m *Model) openLogs(c tree.Container) tea.Cmd {
	m.openViewer(viewLogs, c)
	m.viewer.follow = c.Running()
	return m.startLogs()
}

func (m *Model) stopLogs() {
	if m.logCancel != nil {
		m.logCancel()
		m.logCancel = nil
	}
}

func (m *Model) startLogs() tea.Cmd {
	m.stopLogs()
	m.logGen++
	gen := m.logGen
	ctx, cancel := context.WithCancel(context.Background())
	m.logCancel = cancel
	client, id := m.client, m.target.ID
	opts := docker.LogOptions{Tail: logTail, Follow: m.target.Running(), Timestamps: m.viewer.timestamps}
	return func() tea.Msg {
		ch, err := client.Logs(ctx, id, opts)
		if err != nil {
			return logBatchMsg{gen: gen, done: true, err: err}
		}
		return logStreamMsg{gen: gen, ch: ch}
	}
}

// readLogs blocks for one line, then drains whatever else is buffered so a
// chatty container renders in batches instead of once per line.
func readLogs(gen int, ch <-chan docker.LogLine) tea.Cmd {
	return func() tea.Msg {
		l, ok := <-ch
		if !ok {
			return logBatchMsg{gen: gen, done: true}
		}
		batch := []docker.LogLine{l}
		for len(batch) < 1000 {
			select {
			case l, ok := <-ch:
				if !ok {
					return logBatchMsg{gen: gen, lines: batch, done: true}
				}
				batch = append(batch, l)
			default:
				return logBatchMsg{gen: gen, ch: ch, lines: batch}
			}
		}
		return logBatchMsg{gen: gen, ch: ch, lines: batch}
	}
}

func (m *Model) openInspect(c tree.Container) tea.Cmd {
	m.openViewer(viewInspect, c)
	return m.fetchInspect(c.ID)
}

func (m *Model) fetchInspect(id string) tea.Cmd {
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		raw, err := client.Inspect(ctx, id)
		if err != nil {
			return inspectMsg{id: id, err: err}
		}
		text, err := toYAML(raw)
		if err != nil {
			return inspectMsg{id: id, err: err}
		}
		return inspectMsg{id: id, lines: highlightYAML(text)}
	}
}

func (m *Model) shell(c tree.Container) tea.Cmd {
	if !c.Running() {
		return m.setStatus(c.DisplayName()+" is not running", true)
	}
	cmd := exec.Command("docker", "exec", "-it", c.ID, "sh", "-c", shellScript)
	// With -t the container's output goes to stdout, so stderr only carries
	// errors from the docker CLI itself; keep a copy to show after returning.
	stderr := &bytes.Buffer{}
	cmd.Stderr = &teeWriter{w: os.Stderr, buf: stderr}
	name := c.DisplayName()
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return execDoneMsg{name: name, err: err, stderr: stderr.String()}
	})
}

func (m *Model) execResult(msg execDoneMsg) tea.Cmd {
	if msg.err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if !errors.As(msg.err, &exitErr) {
		return m.setStatus("exec failed: "+msg.err.Error(), true)
	}
	code := exitErr.ExitCode()
	detail := firstLine(msg.stderr)
	switch {
	case code == 126 || code == 127:
		// The runtime's error went to the TTY; stderr only has CLI hints.
		return m.setStatus(fmt.Sprintf("could not start a shell in %s (exit %d) – the image may not include sh", msg.name, code), true)
	case detail != "":
		return m.setStatus(fmt.Sprintf("exec %s (exit %d): %s", msg.name, code, detail), true)
	default:
		return m.setStatus(fmt.Sprintf("shell in %s exited with code %d", msg.name, code), false)
	}
}

type teeWriter struct {
	w   *os.File
	buf *bytes.Buffer
}

func (t *teeWriter) Write(p []byte) (int, error) {
	if t.buf.Len() < 4096 {
		t.buf.Write(p)
	}
	return t.w.Write(p)
}

// firstLine returns the first line of the docker CLI's error output, e.g.
// "Error response from daemon: ...", ignoring the CLI's "What's next" hints.
func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(ansi.Strip(s)), "\n")
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "What's next") {
		return ""
	}
	return line
}
