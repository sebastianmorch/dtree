// Package ui is the Bubble Tea interface: a project/container tree plus
// full-screen log and inspect viewers.
package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/sebastianmorch/dtree/internal/config"
	"github.com/sebastianmorch/dtree/internal/docker"
	"github.com/sebastianmorch/dtree/internal/tree"
)

const (
	logTail        = 500
	refreshDelay   = 150 * time.Millisecond // coalesces bursts of container events
	maxBackoff     = 30 * time.Second
	statusDuration = 4 * time.Second
	requestTimeout = 10 * time.Second

	// Prefer bash, fall back to sh, in a single exec.
	shellScript = "command -v bash >/dev/null 2>&1 && exec bash || exec sh"
	// Turn off XON/XOFF flow control on the container's tty, so ctrl+s
	// doesn't freeze the shell until ctrl+q.
	noFlowControl = "stty -ixon 2>/dev/null"
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
	detailsMsg struct {
		id      string
		details containerDetails
	}
	execDoneMsg struct {
		name   string
		cmd    string // the command run from the prompt; "" for a shell
		stay   bool   // a shell was opened after the command
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

	// Quick commands (x): the prompt, its suggestions, the jobs by container
	// ID and the container whose output popup is open ("" when none).
	prompting     bool
	promptText    string
	promptQuery   string // what was typed; suggestions are filtered by it
	promptTarget  tree.Container
	promptNote    string // shown on the prompt's border after ctrl+s
	promptNoteErr bool
	suggestIdx    int // highlighted suggestion, -1 for none
	history       []string
	commands      *config.Commands
	jobs          map[string]*job
	jobGen        int
	popup         string

	// details caches inspect data for the info panel by container ID. A
	// container's config never changes, so entries never go stale; nil
	// marks a request in flight.
	details map[string]*containerDetails
}

func New(client docker.Client, showAll bool) *Model {
	return &Model{
		client: client, tree: tree.New(), showAll: showAll, width: 80, height: 24,
		details: map[string]*containerDetails{}, jobs: map[string]*job{}, commands: &config.Commands{},
	}
}

// SetCommands sets the saved quick commands suggested in the prompt.
func (m *Model) SetCommands(c *config.Commands) { m.commands = c }

// SetFilter sets the tree filter, as if it had been typed after /.
func (m *Model) SetFilter(q string) { m.tree.SetFilter(q) }

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.list(), m.subscribe())
}

// Close stops background streams.
func (m *Model) Close() {
	if m.eventsCancel != nil {
		m.eventsCancel()
	}
	m.stopLogs()
	m.stopJobs()
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := m.update(msg)
	return m, tea.Batch(cmd, m.loadDetails())
}

func (m *Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width <= 0 || msg.Height <= 0 {
			return m, nil // unknown size, keep the defaults
		}
		m.width, m.height = msg.Width, msg.Height
		if m.viewer != nil {
			m.viewer.setSize(m.viewerWidth(), m.viewerHeight())
		}
		if m.popup != "" {
			m.openPopup(m.popup)
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
		first := !m.loaded
		m.loaded, m.listErr, m.connected = true, nil, true
		m.tree.Set(msg.containers)
		if first && m.tree.Filter() != "" {
			m.tree.FirstContainer() // start on the first match of a filter from the command line
		}
		m.pruneDetails(msg.containers)
		m.pruneJobs(msg.containers)
		return m, nil

	case detailsMsg:
		m.details[msg.id] = &msg.details
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

	case execStartMsg:
		return m, m.execStarted(msg)

	case execBatchMsg:
		return m, m.execBatch(msg)

	case stopFailedMsg:
		return m, m.stopFailed(msg)

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
	if m.popup != "" {
		return m.handlePopupKey(msg)
	}
	if m.prompting {
		return m.handlePromptKey(msg)
	}
	if m.filtering {
		return m.handleFilterKey(msg)
	}

	switch key {
	case "q", "ctrl+c":
		return m.quit()
	case "/", "f":
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
	case "enter":
		if c := m.tree.SelectedContainer(); c != nil {
			return m.shell(*c, "", false)
		}
		m.tree.Toggle()
	case " ":
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
			return m.shell(*c, "", false)
		}
	case "x":
		return m.startPrompt()
	case "X":
		return m.showLastOutput()
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

// The viewer's content area sits inside a border with a hint bar below.
func (m *Model) viewerWidth() int  { return max(1, m.width-4) }
func (m *Model) viewerHeight() int { return max(1, m.height-3) }

func (m *Model) openViewer(kind viewKind, c tree.Container) {
	m.stopLogs()
	title := c.DisplayName()
	if c.Project != "" {
		title = c.Project + "/" + title
	}
	m.target = c
	m.viewer = newViewer(kind, title, m.viewerWidth(), m.viewerHeight())
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

func readLogs(gen int, ch <-chan docker.LogLine) tea.Cmd {
	return func() tea.Msg {
		lines, done := readBatch(ch)
		return logBatchMsg{gen: gen, ch: ch, lines: lines, done: done}
	}
}

// readBatch blocks for one line, then drains whatever else is buffered so
// chatty output renders in batches instead of once per line. done reports
// that the channel is closed.
func readBatch(ch <-chan docker.LogLine) (batch []docker.LogLine, done bool) {
	l, ok := <-ch
	if !ok {
		return nil, true
	}
	batch = []docker.LogLine{l}
	for len(batch) < 1000 {
		select {
		case l, ok := <-ch:
			if !ok {
				return batch, true
			}
			batch = append(batch, l)
		default:
			return batch, false
		}
	}
	return batch, false
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

// loadDetails fetches the selected container's inspect data for the info
// panel unless it is cached or already being fetched.
func (m *Model) loadDetails() tea.Cmd {
	c := m.tree.SelectedContainer()
	if c == nil || m.mode != modeTree || m.popup != "" {
		return nil
	}
	if _, ok := m.details[c.ID]; ok {
		return nil
	}
	m.details[c.ID] = nil
	client, id := m.client, c.ID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		raw, err := client.Inspect(ctx, id)
		if err != nil {
			return detailsMsg{id: id} // shown without the extra fields
		}
		return detailsMsg{id: id, details: parseDetails(raw)}
	}
}

// pruneDetails drops cached details of containers that are gone.
func (m *Model) pruneDetails(cs []tree.Container) {
	keep := make(map[string]bool, len(cs))
	for _, c := range cs {
		keep[c.ID] = true
	}
	for id, d := range m.details {
		if !keep[id] && d != nil {
			delete(m.details, id)
		}
	}
}

// shell takes over the terminal for an interactive shell in c, or for a
// command from the prompt. With stay, a shell opens after the command;
// without it, dtree waits for enter so the command's output can be read.
func (m *Model) shell(c tree.Container, command string, stay bool) tea.Cmd {
	if !c.Running() {
		return m.setStatus(c.DisplayName()+" is not running", true)
	}
	cmd := exec.Command("docker", shellArgs(c.ID, command, stay)...)
	// With -t the container's output goes to stdout, so stderr only carries
	// errors from the docker CLI itself; keep a copy to show after returning.
	stderr := &bytes.Buffer{}
	cmd.Stderr = &teeWriter{w: os.Stderr, buf: stderr}
	name := c.DisplayName()
	run := &clearScreenExec{Cmd: cmd, pause: command != "" && !stay}
	return tea.Exec(run, func(err error) tea.Msg {
		return execDoneMsg{name: name, cmd: command, stay: stay, err: err, stderr: stderr.String()}
	})
}

// shellArgs are the docker arguments for shell. The command is followed by
// the shell script on a new line, so a trailing comment in it can't hide
// the script. Flow control is turned off first, on a line of its own.
func shellArgs(id, command string, stay bool) []string {
	script := shellScript
	switch {
	case command != "" && stay:
		script = command + "\n" + shellScript
	case command != "":
		script = command
	}
	return []string{"exec", "-it", id, "sh", "-c", noFlowControl + "\n" + script}
}

// clearScreenExec clears the terminal before running the command. Bubble Tea
// leaves the alternate screen for exec, which would otherwise show whatever
// was in the terminal before dtree started above the container's prompt.
// With pause it waits for enter after the command, so dtree doesn't redraw
// over the output.
type clearScreenExec struct {
	*exec.Cmd
	pause bool
}

const clearScreen = "\x1b[H\x1b[2J"

func (c *clearScreenExec) SetStdin(r io.Reader) {
	if c.Stdin == nil {
		c.Stdin = r
	}
}

func (c *clearScreenExec) SetStdout(w io.Writer) {
	if c.Stdout == nil {
		c.Stdout = w
	}
}

func (c *clearScreenExec) SetStderr(w io.Writer) {
	if c.Stderr == nil {
		c.Stderr = w
	}
}

func (c *clearScreenExec) Run() error {
	out := c.Stdout
	if out == nil {
		out = os.Stdout
	}
	_, _ = io.WriteString(out, clearScreen)
	err := c.Cmd.Run()
	if c.pause {
		_, _ = io.WriteString(out, "\r\n"+styleMarker.Render("── "+exitText(err)+" · press enter to return to dtree ──"))
		waitForEnter(c.Stdin)
	}
	return err
}

func exitText(err error) string {
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return "exited with code 0"
	case errors.As(err, &exitErr):
		return fmt.Sprintf("exited with code %d", exitErr.ExitCode())
	default:
		return err.Error()
	}
}

// waitForEnter reads until a line ends. Bubble Tea restores the terminal's
// line mode before exec, but a raw terminal sends \r, so either ends it.
func waitForEnter(r io.Reader) {
	if r == nil {
		r = os.Stdin
	}
	b := make([]byte, 1)
	for {
		if n, err := r.Read(b); err != nil || n == 1 && (b[0] == '\n' || b[0] == '\r') {
			return
		}
	}
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
	case msg.cmd != "" && !msg.stay && detail != "":
		return m.setStatus(fmt.Sprintf("exec %s (exit %d): %s", msg.name, code, detail), true)
	case msg.cmd != "" && !msg.stay:
		// Its output, errors included, was on screen until enter.
		return m.setStatus(fmt.Sprintf("%s in %s exited with code %d", msg.cmd, msg.name, code), true)
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
