package ui

import (
	"context"
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/sebastianmorch/dtree/internal/config"
	"github.com/sebastianmorch/dtree/internal/docker"
	"github.com/sebastianmorch/dtree/internal/tree"
)

const (
	maxHistory     = 50
	maxSuggestions = 8  // rows of suggestions below the prompt
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

	// typing is the popup's input mode, where keys go to the command. input
	// queues them for a goroutine that writes them in order once the
	// command has started; it is closed when the command ends.
	typing bool
	input  chan []byte
}

const inputQueue = 256 // keys and pastes waiting to be written

// send queues keys for the command. When the command doesn't read them
// and the queue is full, they are dropped rather than blocking the UI.
func (j *job) send(p []byte) {
	if !j.running || len(p) == 0 {
		return
	}
	select {
	case j.input <- p:
	default:
	}
}

// writeInput writes queued keys until the queue is closed. After a failed
// write the rest are discarded.
func writeInput(queue <-chan []byte, write func([]byte) error) {
	var err error
	for p := range queue {
		if err == nil {
			err = write(p)
		}
	}
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
	j.typing = j.typing && j.running
	j.viewer.typing = j.typing
	if j.term.showCursor != j.typing {
		j.term.showCursor = j.typing
		j.viewer.replaceTail(j.term.flush())
	}
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
	m.setPromptText("")
	return nil
}

// handlePromptKey edits the command line. Up and down pick a suggestion,
// which replaces the input until something is typed; tab completes the
// highlighted (or first) suggestion so it can be edited.
func (m *Model) handlePromptKey(msg tea.KeyMsg) tea.Cmd {
	if m.edit != nil {
		return m.handleEditKey(msg)
	}
	switch msg.String() {
	case "ctrl+c":
		return m.quit()
	case "esc":
		m.prompting = false
	case "enter":
		cmd := m.promptCommand()
		if cmd == "" {
			return nil
		}
		m.prompting = false
		return m.runJob(m.promptTarget, cmd)
	case "ctrl+o", "ctrl+t":
		// Run in the full terminal, for programs that need it (top, vim);
		// ctrl+t opens a shell when the command ends.
		cmd := m.promptCommand()
		if cmd == "" {
			return nil
		}
		m.prompting = false
		m.addHistory(cmd)
		return m.shell(m.promptTarget, cmd, msg.String() == "ctrl+t")
	case "down", "ctrl+n":
		m.moveSuggestion(1)
	case "up", "ctrl+p":
		m.moveSuggestion(-1)
	case "tab":
		if list := m.suggestions(); len(list) > 0 {
			m.setPromptText(list[max(0, m.suggestIdx)].cmd)
		}
	case "ctrl+s":
		m.toggleSaved()
	case "ctrl+e":
		m.startEdit()
	case "backspace":
		m.setPromptText(dropLastRune(m.promptText))
	case "ctrl+u":
		m.setPromptText("")
	default:
		if text, ok := typedText(msg); ok {
			m.setPromptText(m.promptText + text)
		}
	}
	return nil
}

// setPromptText replaces the input as if it had been typed.
func (m *Model) setPromptText(text string) {
	m.promptText, m.promptQuery, m.suggestIdx = text, text, -1
	m.promptNote = ""
}

func dropLastRune(s string) string {
	r := []rune(s)
	return string(r[:max(0, len(r)-1)])
}

// promptCommand is the command the input stands for: a saved command's
// alias is replaced by the command.
func (m *Model) promptCommand() string { return m.commands.Resolve(m.promptText) }

// suggestion is a command offered below the prompt. label is what is shown:
// the alias of a saved command that has one, otherwise the command.
type suggestion struct {
	cmd, label string
	saved      bool
}

// suggestions lists the commands whose text or alias contains what was
// typed: this session's history, most recent first, then saved commands that
// haven't run yet.
func (m *Model) suggestions() []suggestion {
	limit := min(maxSuggestions, m.height-4) // the prompt and its border must fit too
	q := strings.ToLower(strings.TrimSpace(m.promptQuery))
	var out []suggestion
	add := func(cmd string) {
		if len(out) >= limit || slices.ContainsFunc(out, func(s suggestion) bool { return s.cmd == cmd }) {
			return
		}
		saved, ok := m.commands.Find(cmd)
		if !strings.Contains(strings.ToLower(cmd), q) && !strings.Contains(strings.ToLower(saved.Alias), q) {
			return
		}
		label := cmd
		if ok {
			label = saved.Label()
		}
		out = append(out, suggestion{cmd: cmd, label: label, saved: ok})
	}
	for _, cmd := range slices.Backward(m.history) {
		add(cmd)
	}
	for _, c := range m.commands.Global {
		add(c.Cmd)
	}
	return out
}

// moveSuggestion highlights the next (d=1) or previous (d=-1) suggestion and
// puts it in the input. Moving up past the first one restores what was typed.
func (m *Model) moveSuggestion(d int) {
	list := m.suggestions()
	i := m.suggestIdx + d
	if i < -1 || i >= len(list) {
		return
	}
	m.suggestIdx, m.promptText = i, m.promptQuery
	if i >= 0 {
		m.promptText = list[i].cmd
	}
}

// toggleSaved saves the command in the input, or forgets it if it is saved.
func (m *Model) toggleSaved() {
	cmd := m.promptCommand()
	if cmd == "" {
		return
	}
	saved, err := m.commands.Toggle(cmd)
	switch {
	case err != nil:
		m.promptNote, m.promptNoteErr = "could not save: "+err.Error(), true
	case saved:
		m.promptNote, m.promptNoteErr = "★ saved", false
	default:
		m.promptNote, m.promptNoteErr = "forgotten", false
	}
	if m.suggestIdx >= 0 {
		// A forgotten command that never ran drops out of the list.
		m.suggestIdx = slices.IndexFunc(m.suggestions(), func(s suggestion) bool { return s.cmd == m.promptText })
		if m.suggestIdx < 0 {
			m.promptQuery = m.promptText
		}
	}
}

// commandEdit is the form ctrl+e opens over the prompt to change a saved
// command and its alias, or to save the command in the prompt with one.
type commandEdit struct {
	old    string    // the saved command being changed; "" when adding one
	fields [2]string // alias, command
	field  int       // the field being typed into
}

const (
	editAlias = iota
	editCmd
)

// startEdit opens the form for the command in the prompt. A saved command
// is edited; any other command is saved with what the form holds.
func (m *Model) startEdit() {
	cmd := m.promptCommand()
	if cmd == "" {
		return
	}
	e := &commandEdit{fields: [2]string{"", cmd}}
	if saved, ok := m.commands.Find(cmd); ok {
		e.old, e.fields[editAlias] = cmd, saved.Alias
	}
	m.edit = e
	m.promptNote = ""
}

// handleEditKey edits the form's fields. tab and the arrows move between
// them, enter saves and esc goes back to the prompt unchanged.
func (m *Model) handleEditKey(msg tea.KeyMsg) tea.Cmd {
	e := m.edit
	field := &e.fields[e.field]
	switch msg.String() {
	case "ctrl+c":
		return m.quit()
	case "esc":
		m.edit = nil
		m.promptNote = ""
	case "enter":
		m.saveEdit()
	case "tab", "shift+tab", "up", "down", "ctrl+n", "ctrl+p":
		e.field = 1 - e.field
	case "backspace":
		*field = dropLastRune(*field)
	case "ctrl+u":
		*field = ""
	default:
		if text, ok := typedText(msg); ok {
			*field += text
		}
	}
	return nil
}

// saveEdit saves the form. The edited command is put in the prompt.
func (m *Model) saveEdit() {
	e := m.edit
	cmd := config.Command{Alias: e.fields[editAlias], Cmd: e.fields[editCmd]}
	if err := m.commands.Put(e.old, cmd); err != nil {
		m.promptNote, m.promptNoteErr = "could not save: "+err.Error(), true
		return
	}
	m.edit = nil
	m.setPromptText(strings.TrimSpace(cmd.Cmd))
	m.promptNote, m.promptNoteErr = "★ saved", false
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
	j := &job{
		gen: gen, name: c.DisplayName(), cmd: cmd, running: true, viewer: v, term: newTerm(width, height),
		cancel: cancel, input: make(chan []byte, inputQueue),
	}
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
	go writeInput(j.input, msg.session.Input)
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
	close(j.input)
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

// handlePopupKey handles keys in the output popup. In navigation mode they
// scroll and search, i switches to input mode and ctrl+c stops a running
// command instead of quitting. In input mode every key goes to the command
// except esc, which switches back, and ctrl+c.
func (m *Model) handlePopupKey(msg tea.KeyMsg) tea.Cmd {
	j := m.jobs[m.popup]
	key := msg.String()
	if j.typing {
		switch key {
		case "esc":
			j.typing = false
			j.sync()
		case "ctrl+c":
			return m.stopJob(m.popup, j)
		default:
			j.send(keyBytes(msg))
		}
		return nil
	}
	if j.running && !j.viewer.searching {
		switch key {
		case "ctrl+c":
			return m.stopJob(m.popup, j)
		case "i":
			j.typing = true
			j.viewer.gotoBottom()
			j.sync()
			return nil
		}
	}
	switch j.viewer.updateKey(msg) {
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

// promptBox is the popup the command is typed into, with the suggestions
// below it. When the input is wider than the box its start scrolls out of
// view so the cursor stays visible.
func (m *Model) promptBox() []string {
	if m.edit != nil {
		return m.editBox()
	}
	title := m.promptTarget.DisplayName()
	if m.promptTarget.Project != "" {
		title = m.promptTarget.Project + "/" + title
	}
	list := m.suggestions()
	box := panel{
		title:  styleAccent.Render("run") + styleDim.Render(" › ") + styleProject.Render(title),
		border: styleFocus,
		width:  m.popupWidth(),
		height: 1 + len(list),
	}
	m.notePrompt(&box)
	input := inputLine(styleAccent.Render("$ "), m.promptText, box.innerWidth(), true)
	if cmd := m.promptCommand(); cmd != strings.TrimSpace(m.promptText) {
		// A typed alias: show what will run.
		room := max(0, box.innerWidth()-ansi.StringWidth(input))
		input += styleDim.Render(ansi.Truncate("  → "+cmd, room, "…"))
	}
	lines := []string{input}
	width := box.innerWidth()
	for i, s := range list {
		p := painter{selected: i == m.suggestIdx}
		mark := "  "
		if s.saved {
			mark = "★ "
		}
		line := p.paint(styleYellow, mark) + p.paint(lipgloss.NewStyle(), ansi.Truncate(s.label, max(0, width-2), "…"))
		lines = append(lines, line+p.paint(styleDim, strings.Repeat(" ", max(0, width-ansi.StringWidth(line)))))
	}
	return box.render(lines)
}

// editBox is the ctrl+e form, drawn in place of the prompt.
func (m *Model) editBox() []string {
	e := m.edit
	title := "edit saved command"
	if e.old == "" {
		title = "save command"
	}
	box := panel{title: styleAccent.Render(title), border: styleFocus, width: m.popupWidth(), height: 2}
	m.notePrompt(&box)
	labels := [2]string{"alias   ", "command "}
	var lines []string
	for i, text := range e.fields {
		st := styleDim
		if i == e.field {
			st = styleAccent
		}
		lines = append(lines, inputLine(st.Render(labels[i]+"› "), text, box.innerWidth(), i == e.field))
	}
	if e.fields[editAlias] == "" && e.field != editAlias {
		lines[editAlias] += styleDim.Render("(none: the command is shown)")
	}
	return box.render(lines)
}

// notePrompt shows the note left by saving on the box's bottom border.
func (m *Model) notePrompt(box *panel) {
	if m.promptNote == "" {
		return
	}
	st := styleYellow
	if m.promptNoteErr {
		st = styleError
	}
	// edge drops text that doesn't fit beside the corners and padding.
	box.bottomRight = st.Render(ansi.Truncate(m.promptNote, max(0, box.width-9), "…"))
}

// inputLine is a text field after its prefix. When the text is wider than
// the line its start scrolls out of view so the cursor stays visible.
func inputLine(prefix, text string, width int, cursor bool) string {
	room := width - ansi.StringWidth(prefix) - 1 // the cursor takes a cell
	if over := ansi.StringWidth(text) - room; over > 0 {
		text = ansi.TruncateLeft(text, over+1, "…")
	}
	if cursor {
		text += styleAccent.Render("█")
	}
	return prefix + text
}

func (m *Model) promptFooter() string {
	if m.edit != nil {
		return hintBar([][2]string{
			{"enter", "save"}, {"tab", "next field"}, {"^u", "clear field"}, {"esc", "back"},
		}, m.width)
	}
	save, edit := "save", "alias"
	if m.commands.Saved(m.promptCommand()) {
		save, edit = "forget", "edit"
	}
	return hintBar([][2]string{
		{"enter", "run"}, {"esc", "cancel"}, {"↑/↓", "suggestions"}, {"tab", "complete"},
		{"^s", save}, {"^e", edit}, {"^o", "in terminal"}, {"^t", "then shell"}, {"^u", "clear"},
	}, m.width)
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

// keyBytes is what a terminal sends for a key press.
func keyBytes(msg tea.KeyMsg) []byte {
	var b []byte
	switch {
	case msg.Type == tea.KeyRunes:
		b = []byte(string(msg.Runes))
	case msg.Type == tea.KeySpace:
		b = []byte(" ")
	case msg.Type >= 0 && msg.Type < 0x20 || msg.Type == 0x7f:
		b = []byte{byte(msg.Type)} // control keys, enter, tab and backspace
	default:
		seq, ok := keySequences[msg.Type]
		if !ok {
			return nil
		}
		b = []byte(seq)
	}
	if msg.Alt && !msg.Paste {
		b = append([]byte{0x1b}, b...)
	}
	return b
}

var keySequences = map[tea.KeyType]string{
	tea.KeyUp: "\x1b[A", tea.KeyDown: "\x1b[B", tea.KeyRight: "\x1b[C", tea.KeyLeft: "\x1b[D",
	tea.KeyHome: "\x1b[H", tea.KeyEnd: "\x1b[F", tea.KeyPgUp: "\x1b[5~", tea.KeyPgDown: "\x1b[6~",
	tea.KeyDelete: "\x1b[3~", tea.KeyInsert: "\x1b[2~", tea.KeyShiftTab: "\x1b[Z",
	tea.KeyF1: "\x1bOP", tea.KeyF2: "\x1bOQ", tea.KeyF3: "\x1bOR", tea.KeyF4: "\x1bOS",
}
