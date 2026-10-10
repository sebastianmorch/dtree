package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/sebastianmorch/dtree/internal/docker"
)

// maxLogLines caps the log buffer; the oldest lines are dropped beyond it.
const maxLogLines = 10_000

type viewKind int

const (
	viewLogs viewKind = iota
	viewInspect
	viewExec // output of a quick command, shown in a popup
)

// viewerAction tells the root model what to do after a key press.
type viewerAction int

const (
	actNone viewerAction = iota
	actClose
	actQuit
	actRestartLogs // timestamps toggled
	actRefetch     // re-run inspect
)

type viewLine struct {
	text   string // sanitized, may contain SGR colour codes
	plain  string // text without escape codes, used for search
	stderr bool
	marker bool // a status line inserted by dtree, not container output
}

// viewer is a scrollable pane for logs, inspect and command output.
// Scrolling is by logical line; wrapped lines span several screen rows.
type viewer struct {
	kind     viewKind
	title    string
	cmd      string // the command, for viewExec
	state    string // pre-rendered status badge, for viewExec
	stopHint string // ctrl+c's action while a command runs, for viewExec

	lines  []viewLine
	offset int // index of the first visible line

	follow     bool
	wrap       bool
	timestamps bool
	loading    bool

	searching bool
	input     string
	query     string
	match     int // line index of the current search match, -1 if none
	message   string

	width, height int // height is the number of content rows
}

func newViewer(kind viewKind, title string, width, height int) *viewer {
	return &viewer{kind: kind, title: title, match: -1, loading: true, width: width, height: height}
}

// streaming reports whether output arrives over time, so follow applies.
func (v *viewer) streaming() bool { return v.kind != viewInspect }

func (v *viewer) setSize(width, height int) {
	v.width, v.height = width, height
	v.clampOffset()
}

func newViewLine(text string, stderr bool) viewLine {
	t := sanitize(text)
	return viewLine{text: t, plain: ansi.Strip(t), stderr: stderr}
}

// appendLogs adds streamed lines, trimming the buffer to maxLogLines.
func (v *viewer) appendLogs(lines []docker.LogLine) {
	v.loading = false
	for _, l := range lines {
		v.lines = append(v.lines, newViewLine(l.Text, l.Stderr))
	}
	v.trim()
	if v.follow {
		v.offset = v.maxOffset()
	}
}

// replaceTail applies a term's changes: it drops the lines the term dropped
// from the top, then replaces the lines from index from on.
func (v *viewer) replaceTail(dropped, from int, lines []viewLine) {
	v.loading = false
	if dropped > 0 {
		dropped = min(dropped, len(v.lines))
		v.lines = v.lines[dropped:]
		v.offset = max(0, v.offset-dropped)
		v.match = max(-1, v.match-dropped)
		from = max(0, from)
	}
	v.lines = append(v.lines[:min(from, len(v.lines))], lines...)
	if v.match >= len(v.lines) {
		v.match = -1
	}
	if v.follow {
		v.offset = v.maxOffset()
	}
	v.clampOffset()
}

func (v *viewer) appendMarker(text string) {
	v.loading = false
	v.lines = append(v.lines, viewLine{text: text, plain: text, marker: true})
	v.trim()
	if v.follow {
		v.offset = v.maxOffset()
	}
}

func (v *viewer) trim() {
	drop := len(v.lines) - maxLogLines
	if drop <= 0 {
		return
	}
	copy(v.lines, v.lines[drop:])
	v.lines = v.lines[:maxLogLines]
	v.offset = max(0, v.offset-drop)
	if v.match >= 0 {
		v.match -= drop
		if v.match < 0 {
			v.match = -1
		}
	}
}

// setContent replaces the content with pre-rendered lines (inspect output),
// keeping the scroll position.
func (v *viewer) setContent(lines []string) {
	v.loading = false
	v.lines = v.lines[:0]
	for _, l := range lines {
		v.lines = append(v.lines, viewLine{text: l, plain: ansi.Strip(l)})
	}
	if v.match >= len(v.lines) {
		v.match = -1
	}
	v.clampOffset()
}

func (v *viewer) reset() {
	v.lines = nil
	v.offset = 0
	v.match = -1
	v.loading = true
}

// rowsFor is the number of screen rows a line occupies.
func (v *viewer) rowsFor(l viewLine) int {
	if !v.wrap || v.width <= 0 {
		return 1
	}
	w := ansi.StringWidth(l.text)
	if w == 0 {
		return 1
	}
	return (w + v.width - 1) / v.width
}

// maxOffset is the offset at which the last line sits at the bottom.
func (v *viewer) maxOffset() int {
	if !v.wrap {
		return max(0, len(v.lines)-v.height)
	}
	rows := 0
	for i := len(v.lines) - 1; i >= 0; i-- {
		rows += v.rowsFor(v.lines[i])
		if rows > v.height {
			return i + 1
		}
	}
	return 0
}

func (v *viewer) clampOffset() {
	v.offset = max(0, min(v.offset, v.maxOffset()))
}

func (v *viewer) scroll(n int) {
	if n < 0 && v.streaming() {
		v.follow = false
	}
	v.offset += n
	v.clampOffset()
}

func (v *viewer) gotoBottom() {
	v.offset = v.maxOffset()
	if v.streaming() {
		v.follow = true
	}
}

// updateKey handles a key press, taking typed text directly from the message
// while searching so multi-rune input (fast typing, paste) is not dropped.
func (v *viewer) updateKey(msg tea.KeyMsg) viewerAction {
	if v.searching {
		if text, ok := typedText(msg); ok {
			v.input += text
			return actNone
		}
	}
	return v.update(msg.String())
}

func (v *viewer) update(key string) viewerAction {
	if v.searching {
		return v.updateSearch(key)
	}
	v.message = ""
	half := max(1, v.height/2)
	switch key {
	case "ctrl+c":
		return actQuit
	case "q", "esc":
		return actClose
	case "j", "down":
		v.scroll(1)
	case "k", "up":
		v.scroll(-1)
	case "ctrl+d", "pgdown":
		v.scroll(half)
	case "ctrl+u", "pgup":
		v.scroll(-half)
	case "g", "home":
		v.scroll(-len(v.lines))
	case "G", "end":
		v.gotoBottom()
	case "/":
		v.searching = true
		v.input = ""
	case "n":
		v.findNext(v.match+1, 1)
	case "N":
		v.findNext(v.match-1, -1)
	case "w":
		v.wrap = !v.wrap
		if v.follow {
			v.offset = v.maxOffset()
		}
		v.clampOffset()
	case "f":
		if v.streaming() {
			v.follow = !v.follow
			if v.follow {
				v.offset = v.maxOffset()
			}
		}
	case "t":
		if v.kind == viewLogs {
			v.timestamps = !v.timestamps
			return actRestartLogs
		}
	case "r":
		if v.kind == viewInspect {
			return actRefetch
		}
	}
	return actNone
}

func (v *viewer) updateSearch(key string) viewerAction {
	switch key {
	case "ctrl+c":
		return actQuit
	case "esc":
		v.searching = false
	case "enter":
		v.searching = false
		v.query = v.input
		v.match = -1
		if v.query != "" {
			v.findNext(v.offset, 1)
		}
	case "backspace":
		if r := []rune(v.input); len(r) > 0 {
			v.input = string(r[:len(r)-1])
		}
	default:
		if r := []rune(key); len(r) == 1 && r[0] >= 0x20 {
			v.input += key
		}
	}
	return actNone
}

// findNext searches from line start in direction dir (1 or -1), wrapping.
func (v *viewer) findNext(start, dir int) {
	if v.query == "" {
		v.message = "no search pattern"
		return
	}
	n := len(v.lines)
	if n == 0 {
		v.message = "pattern not found: " + v.query
		return
	}
	q := strings.ToLower(v.query)
	start = ((start % n) + n) % n
	for i := 0; i < n; i++ {
		idx := ((start+i*dir)%n + n) % n
		if strings.Contains(strings.ToLower(v.lines[idx].plain), q) {
			v.match = idx
			if v.streaming() {
				v.follow = false
			}
			if idx < v.offset || idx >= v.offset+v.height {
				v.offset = idx - v.height/3
			}
			v.clampOffset()
			return
		}
	}
	v.match = -1
	v.message = "pattern not found: " + v.query
}

func (v *viewer) renderLine(i int) string {
	l := v.lines[i]
	switch {
	case l.marker:
		return styleMarker.Render(l.text)
	case v.query != "" && strings.Contains(strings.ToLower(l.plain), strings.ToLower(v.query)):
		hl := styleMatch
		if i == v.match {
			hl = styleCurMatch
		}
		return highlightMatches(l.plain, v.query, hl)
	// Commands often write progress and info to stderr, so command output
	// is left uncoloured; the exit badge tells whether it failed.
	case l.stderr && l.text == l.plain && v.kind != viewExec:
		return styleRed.Render(l.text)
	default:
		return l.text
	}
}

// highlightMatches highlights case-insensitive occurrences of q in s.
func highlightMatches(s, q string, hl style) string {
	lower, lq := strings.ToLower(s), strings.ToLower(q)
	if len(lower) != len(s) { // case folding changed byte lengths; highlight whole line
		return hl.Render(s)
	}
	var b strings.Builder
	for {
		i := strings.Index(lower, lq)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		b.WriteString(hl.Render(s[i : i+len(q)]))
		s, lower = s[i+len(q):], lower[i+len(q):]
	}
}

func (v *viewer) heading() string {
	switch v.kind {
	case viewInspect:
		return styleAccent.Render("inspect") + styleDim.Render(" › ") + styleProject.Render(v.title)
	case viewExec:
		return styleAccent.Render("run") + styleDim.Render(" › ") + styleProject.Render(v.title) +
			styleAccent.Render(" $ ") + v.cmd
	default:
		return styleAccent.Render("logs") + styleDim.Render(" › ") + styleProject.Render(v.title)
	}
}

// badges are the mode indicators on the right of the top border.
func (v *viewer) badges() string {
	var badges []string
	if v.kind == viewExec {
		badges = append(badges, v.state)
		if !v.follow {
			badges = append(badges, styleDim.Render("paused"))
		}
	}
	if v.kind == viewLogs {
		if v.follow {
			badges = append(badges, styleGreen.Render("●")+" "+styleBadge.Foreground(colorGreen).Render("FOLLOWING"))
		} else {
			badges = append(badges, styleYellow.Render("❚❚")+" "+styleBadge.Foreground(colorYellow).Render("PAUSED"))
		}
		if v.timestamps {
			badges = append(badges, styleDim.Render("timestamps"))
		}
	}
	if v.wrap {
		badges = append(badges, styleDim.Render("wrap"))
	}
	return strings.Join(badges, styleDim.Render(" · "))
}

func (v *viewer) footer() string { return v.footerWidth(v.width + 4) }

// footerWidth renders the hint bar for the given screen width.
func (v *viewer) footerWidth(width int) string {
	if v.searching {
		return " " + styleAccent.Render("/") + v.input + styleAccent.Render("█")
	}
	if v.message != "" {
		return ansi.Truncate(" "+styleError.Render("✗ "+v.message), width, "…")
	}
	hints := [][2]string{{"j/k", "scroll"}, {"^d/^u", "page"}, {"g/G", "top/bottom"}, {"/", "search"}, {"n/N", "next/prev"}, {"w", "wrap"}}
	switch v.kind {
	case viewLogs:
		hints = append(hints, [2]string{"f", "follow"}, [2]string{"t", "timestamps"})
	case viewInspect:
		hints = append(hints, [2]string{"r", "refresh"})
	case viewExec:
		// The popup can't be told apart from the list, so keep "close" in view.
		first := [][2]string{{"q", "close"}}
		if v.stopHint != "" {
			first = append(first, [2]string{"^c", v.stopHint})
		}
		return hintBar(append(first, append(hints, [2]string{"f", "follow"})...), width)
	}
	return hintBar(append(hints, [2]string{"q", "back"}), width)
}

func (v *viewer) position() string {
	if len(v.lines) == 0 {
		return ""
	}
	return styleDim.Render(fmt.Sprintf("%d/%d", min(v.offset+1, len(v.lines)), len(v.lines)))
}

func (v *viewer) view() string {
	return strings.Join(v.box(), "\n") + "\n" + v.footer()
}

// box renders the bordered content without the hint bar.
func (v *viewer) box() []string {
	rows := make([]string, 0, v.height)
	switch {
	case v.loading && v.kind == viewExec:
		rows = append(rows, styleDim.Render("Starting…"))
	case v.loading:
		rows = append(rows, styleDim.Render("Loading…"))
	case len(v.lines) == 0:
		rows = append(rows, styleDim.Render("(no output)"))
	}
	for i := v.offset; i < len(v.lines) && len(rows) < v.height; i++ {
		s := v.renderLine(i)
		if !v.wrap {
			rows = append(rows, ansi.Truncate(s, v.width, "…"))
			continue
		}
		for _, part := range strings.Split(ansi.Hardwrap(s, v.width, true), "\n") {
			if len(rows) == v.height {
				break
			}
			rows = append(rows, part)
		}
	}
	return panel{
		title:       v.heading(),
		topRight:    v.badges(),
		bottomRight: v.position(),
		border:      styleFocus,
		width:       v.width + 4,
		height:      v.height,
	}.render(rows)
}
