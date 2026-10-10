package ui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/sebastianmorch/dtree/internal/docker"
	"github.com/sebastianmorch/dtree/internal/tree"
)

const (
	maxHistory     = 50
	maxBadgeCmd    = 24 // cells of the command shown next to a container row
	minPopupWidth  = 60 // below this the popup takes the full width
	minPopupHeight = 16 // below this the popup takes the full height
)

// job is a quick command run in a container with x. It keeps running and
// collecting output when its popup is closed, so the popup can be reopened.
type job struct {
	gen     int
	name    string // container display name
	cmd     string
	running bool
	exit    int
	err     error
	viewer  *viewer
	term    *term // interprets the output; the viewer shows its rows
	cancel  context.CancelFunc

	// interrupt stops the command; nil until it has started. stops counts
	// ctrl+c presses: the first interrupts it, later ones kill it.
	interrupt func(force bool) error
	stops     int
}

// write feeds command output to the terminal and shows the result.
func (j *job) write(data []byte) {
	_, _ = j.term.Write(data)
	j.viewer.replaceTail(j.term.flush())
}

// marker adds a status line below the output.
func (j *job) marker(text string) {
	j.term.marker(text)
	j.viewer.replaceTail(j.term.flush())
}

func (j *job) failed() bool { return j.err != nil || j.exit != 0 }

// stopped reports whether the command ended because the user stopped it.
func (j *job) stopped() bool { return !j.running && j.stops > 0 && j.failed() }

// sync shows the job's state in its viewer.
func (j *job) sync() {
	j.viewer.state = j.badge()
	j.viewer.stopHint = ""
	if j.running {
		j.viewer.stopHint = "stop"
		if j.stops > 0 {
			j.viewer.stopHint = "kill"
		}
	}
}

// badge is the job's state for the popup's top border.
func (j *job) badge() string {
	switch {
	case j.running && j.stops > 0:
		return styleYellow.Render("●") + " " + styleBadge.Foreground(colorYellow).Render("STOPPING")
	case j.running:
		return styleYellow.Render("●") + " " + styleBadge.Foreground(colorYellow).Render("RUNNING")
	case j.stopped():
		return styleYellow.Render("■") + " " + styleBadge.Foreground(colorYellow).Render("STOPPED")
	case j.err != nil:
		return styleRed.Render("✗") + " " + styleBadge.Foreground(colorRed).Render("ERROR")
	case j.exit != 0:
		return styleRed.Render("✗") + " " + styleBadge.Foreground(colorRed).Render(fmt.Sprintf("EXIT %d", j.exit))
	default:
		return styleGreen.Render("✓") + " " + styleBadge.Foreground(colorGreen).Render("EXIT 0")
	}
}

// rowBadge is the job's state shown after a container's status in the list.
func (j *job) rowBadge(p painter) string {
	icon, st := "✓", styleGreen
	switch {
	case j.running:
		icon, st = "⟳", styleYellow
	case j.stopped():
		icon, st = "■", styleYellow
	case j.failed():
		icon, st = "✗", styleRed
	}
	return p.paint(st, icon+" ") + p.paint(styleDim, ansi.Truncate(j.cmd, maxBadgeCmd, "…"))
}

type (
	execStartMsg struct {
		id, name string
		gen      int
		session  *docker.ExecSession
		err      error
	}
	stopFailedMsg struct {
		id  string
		gen int
		err error
	}
	execBatchMsg struct {
		id     string
		gen    int
		ch     <-chan []byte
		resCh  <-chan docker.ExecResult
		data   []byte
		done   bool
		result docker.ExecResult
	}
)

// startPrompt asks for a command to run in the selected container, or shows
// the popup if a command is still running there.
func (m *Model) startPrompt() tea.Cmd {
	c := m.tree.SelectedContainer()
	if c == nil {
		return m.setStatus("select a container first", true)
	}
	if j := m.jobs[c.ID]; j != nil && j.running {
		m.openPopup(c.ID)
		return nil
	}
	if !c.Running() {
		return m.setStatus(c.DisplayName()+" is not running", true)
	}
	m.prompting, m.promptTarget = true, *c
	m.promptText, m.historyIdx = "", len(m.history)
	return nil
}

// handlePromptKey edits the command line. Up and down walk the history.
func (m *Model) handlePromptKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "ctrl+c":
		return m.quit()
	case "esc":
		m.prompting = false
	case "enter":
		cmd := strings.TrimSpace(m.promptText)
		if cmd == "" {
			return nil
		}
		m.prompting = false
		return m.runJob(m.promptTarget, cmd)
	case "up", "ctrl+p":
		if m.historyIdx > 0 {
			m.historyIdx--
			m.promptText = m.history[m.historyIdx]
		}
	case "down", "ctrl+n":
		if m.historyIdx < len(m.history) {
			m.historyIdx++
			m.promptText = ""
			if m.historyIdx < len(m.history) {
				m.promptText = m.history[m.historyIdx]
			}
		}
	case "backspace":
		if r := []rune(m.promptText); len(r) > 0 {
			m.promptText = string(r[:len(r)-1])
		}
	case "ctrl+u":
		m.promptText = ""
	default:
		if text, ok := typedText(msg); ok {
			m.promptText += text
		}
	}
	return nil
}

func (m *Model) addHistory(cmd string) {
	if n := len(m.history); n > 0 && m.history[n-1] == cmd {
		return
	}
	m.history = append(m.history, cmd)
	if len(m.history) > maxHistory {
		m.history = m.history[1:]
	}
}

// runJob starts cmd in c and opens its popup. A finished job of the same
// container is replaced.
func (m *Model) runJob(c tree.Container, cmd string) tea.Cmd {
	m.addHistory(cmd)
	m.jobGen++
	gen := m.jobGen
	ctx, cancel := context.WithCancel(context.Background())
	title := c.DisplayName()
	if c.Project != "" {
		title = c.Project + "/" + title
	}
	width, height := m.popupWidth()-4, m.popupHeight()-2
	v := newViewer(viewExec, title, width, height)
	v.cmd, v.follow = cmd, true
	j := &job{gen: gen, name: c.DisplayName(), cmd: cmd, running: true, viewer: v, term: newTerm(width, height), cancel: cancel}
	j.sync()
	m.jobs[c.ID] = j
	m.openPopup(c.ID)

	client, id, name := m.client, c.ID, c.DisplayName()
	opts := docker.ExecOptions{Command: cmd, Width: width, Height: height}
	return func() tea.Msg {
		s, err := client.Exec(ctx, id, opts)
		return execStartMsg{id: id, name: name, gen: gen, session: s, err: err}
	}
}

// job returns the container's job if msg belongs to it.
func (m *Model) job(id string, gen int) *job {
	if j := m.jobs[id]; j != nil && j.gen == gen {
		return j
	}
	return nil
}

func (m *Model) execStarted(msg execStartMsg) tea.Cmd {
	j := m.job(msg.id, msg.gen)
	if j == nil {
		return nil
	}
	if msg.err != nil {
		return m.finishJob(msg.id, j, docker.ExecResult{ExitCode: -1, Err: msg.err})
	}
	j.viewer.loading = false
	j.interrupt = msg.session.Interrupt
	read := readExec(msg.id, msg.gen, msg.session.Output, msg.session.Result)
	if j.stops > 0 { // ctrl+c was pressed while it was starting
		return tea.Batch(read, m.sendStop(msg.id, j, j.stops > 1))
	}
	return read
}

// stopJob handles ctrl+c on a running job: the first press interrupts the
// command, later ones kill it.
func (m *Model) stopJob(id string, j *job) tea.Cmd {
	j.stops++
	j.sync()
	if j.interrupt == nil {
		return nil // sent once the command has started
	}
	return m.sendStop(id, j, j.stops > 1)
}

func (m *Model) sendStop(id string, j *job, force bool) tea.Cmd {
	if force {
		j.marker("── killing ──")
	}
	interrupt, gen := j.interrupt, j.gen
	return func() tea.Msg {
		if err := interrupt(force); err != nil {
			return stopFailedMsg{id: id, gen: gen, err: err}
		}
		return nil
	}
}

func (m *Model) stopFailed(msg stopFailedMsg) tea.Cmd {
	if j := m.job(msg.id, msg.gen); j != nil && j.running {
		j.marker("── could not stop: " + msg.err.Error() + " ──")
	}
	return nil
}

func (m *Model) execBatch(msg execBatchMsg) tea.Cmd {
	j := m.job(msg.id, msg.gen)
	if j == nil {
		return nil
	}
	if len(msg.data) > 0 {
		j.write(msg.data)
	}
	if !msg.done {
		return readExec(msg.id, msg.gen, msg.ch, msg.resCh)
	}
	return m.finishJob(msg.id, j, msg.result)
}

// finishJob records how the job ended. When its popup is not open the
// outcome is also shown in the status bar.
func (m *Model) finishJob(id string, j *job, r docker.ExecResult) tea.Cmd {
	j.running, j.exit, j.err = false, r.ExitCode, r.Err
	j.cancel()
	j.sync()
	switch {
	case r.Err != nil:
		j.marker("── error: " + r.Err.Error() + " ──")
	case j.stopped():
		j.marker(fmt.Sprintf("── stopped (exit code %d) ──", r.ExitCode))
	default:
		j.marker(fmt.Sprintf("── exited with code %d ──", r.ExitCode))
	}
	if m.popup == id {
		return nil
	}
	switch {
	case r.Err != nil:
		return m.setStatus(fmt.Sprintf("%s in %s failed: %s", j.cmd, j.name, r.Err), true)
	case r.ExitCode != 0:
		return m.setStatus(fmt.Sprintf("%s in %s exited with code %d – X shows the output", j.cmd, j.name, r.ExitCode), true)
	default:
		return m.setStatus(fmt.Sprintf("%s in %s finished – X shows the output", j.cmd, j.name), false)
	}
}

// readExec reads the next batch of output; once the output ends it waits for
// the exit code.
func readExec(id string, gen int, output <-chan []byte, result <-chan docker.ExecResult) tea.Cmd {
	return func() tea.Msg {
		data, done := readChunks(output)
		msg := execBatchMsg{id: id, gen: gen, ch: output, resCh: result, data: data, done: done}
		if done {
			msg.result = <-result
		}
		return msg
	}
}

func (m *Model) openPopup(id string) {
	j := m.jobs[id]
	if j == nil {
		return
	}
	m.popup = id
	j.viewer.setSize(m.popupWidth()-4, m.popupHeight()-2)
}

// showLastOutput reopens the popup of the selected container's last command.
func (m *Model) showLastOutput() tea.Cmd {
	c := m.tree.SelectedContainer()
	if c == nil {
		return m.setStatus("select a container first", true)
	}
	if m.jobs[c.ID] == nil {
		return m.setStatus("no command has run in "+c.DisplayName()+" yet – press x", true)
	}
	m.openPopup(c.ID)
	return nil
}

func (m *Model) popupViewer() *viewer {
	if j := m.jobs[m.popup]; j != nil {
		return j.viewer
	}
	return nil
}

// handlePopupKey passes keys to the output viewer. ctrl+c stops a running
// command instead of quitting.
func (m *Model) handlePopupKey(msg tea.KeyMsg) tea.Cmd {
	if j := m.jobs[m.popup]; j.running && msg.String() == "ctrl+c" && !j.viewer.searching {
		return m.stopJob(m.popup, j)
	}
	switch m.popupViewer().updateKey(msg) {
	case actQuit:
		return m.quit()
	case actClose:
		m.popup = ""
	}
	return nil
}

// pruneJobs drops jobs of containers that are gone, except an open popup's.
func (m *Model) pruneJobs(cs []tree.Container) {
	keep := make(map[string]bool, len(cs))
	for _, c := range cs {
		keep[c.ID] = true
	}
	for id, j := range m.jobs {
		if !keep[id] && id != m.popup {
			j.cancel()
			delete(m.jobs, id)
		}
	}
}

// stopJobs interrupts running commands when dtree quits; ending the stream
// alone would leave them running in their containers.
func (m *Model) stopJobs() {
	for _, j := range m.jobs {
		if j.running && j.interrupt != nil {
			_ = j.interrupt(false)
		}
		j.cancel()
	}
}

// The popup's outer size, border included. It sits over the tree view, which
// has the hint bar below it.
func (m *Model) popupWidth() int {
	if m.width < minPopupWidth {
		return m.width
	}
	return m.width - m.width/5
}

func (m *Model) popupHeight() int {
	rows := max(3, m.height-1)
	if rows < minPopupHeight {
		return rows
	}
	return rows - rows/4
}

// overlay draws box centred over base, dimming base so the box stands out.
func overlay(base, box []string, width int) []string {
	boxWidth := 0
	for _, l := range box {
		boxWidth = max(boxWidth, ansi.StringWidth(l))
	}
	x := max(0, (width-boxWidth)/2)
	y := max(0, (len(base)-len(box))/2)
	out := make([]string, len(base))
	for i, l := range base {
		plain := ansi.Strip(l)
		if i < y || i >= y+len(box) {
			out[i] = styleDim.Render(plain)
			continue
		}
		left := padRight(ansi.Truncate(plain, x, ""), x)
		right := ansi.TruncateLeft(plain, x+boxWidth, "")
		out[i] = styleDim.Render(left) + box[i-y] + styleDim.Render(right)
	}
	return out
}

// promptBox is the popup the command is typed into. When the input is wider
// than the box its start scrolls out of view so the cursor stays visible.
func (m *Model) promptBox() []string {
	title := m.promptTarget.DisplayName()
	if m.promptTarget.Project != "" {
		title = m.promptTarget.Project + "/" + title
	}
	box := panel{
		title:  styleAccent.Render("run") + styleDim.Render(" › ") + styleProject.Render(title),
		border: styleFocus,
		width:  m.popupWidth(),
		height: 1,
	}
	input := m.promptText
	if over := ansi.StringWidth(input) + 3 - box.innerWidth(); over > 0 {
		input = ansi.TruncateLeft(input, over+1, "…")
	}
	return box.render([]string{styleAccent.Render("$ ") + input + styleAccent.Render("█")})
}

func (m *Model) promptFooter() string {
	return hintBar([][2]string{{"enter", "run"}, {"esc", "cancel"}, {"↑/↓", "history"}, {"^u", "clear"}}, m.width)
}

// readChunks blocks for output, then takes whatever else is buffered so busy
// output renders in batches. done reports that the channel is closed.
func readChunks(ch <-chan []byte) (data []byte, done bool) {
	data, ok := <-ch
	if !ok {
		return nil, true
	}
	for len(data) < 1<<20 {
		select {
		case more, ok := <-ch:
			if !ok {
				return data, true
			}
			data = append(data, more...)
		default:
			return data, false
		}
	}
	return data, false
}
