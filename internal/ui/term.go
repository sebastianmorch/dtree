package ui

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// term interprets a command's terminal output the way a terminal would, so
// progress bars that redraw with \r or move the cursor up update their lines
// instead of adding new ones. It is line based: everything printed is kept as
// scrollback, and the "screen" the cursor can move in is the last height
// rows. Colours are kept; other modes (alternate screen, cursor visibility)
// are ignored, so full-screen programs won't render properly.
type term struct {
	width, height int

	rows      []termRow
	row, col  int // cursor; row indexes rows and may equal len(rows)
	screenTop int // first row of the screen
	saved     [2]int
	sgr       string // active SGR sequences, "" for the default style

	pending []byte // an escape sequence or character cut off by the last chunk
	parser  *ansi.Parser
	state   byte

	// Changes since the last flush: rows dropped from the top by the
	// scrollback limit, and the first row that changed.
	dropped, dirty int
}

type termRow struct {
	cells  []termCell
	marker string // a status line inserted by dtree, not command output
}

// termCell is one column. A wide character's extra columns hold empty cells.
type termCell struct {
	text, sgr string
}

func newTerm(width, height int) *term {
	return &term{width: max(1, width), height: max(1, height), parser: ansi.NewParser()}
}

// Write feeds output to the terminal. It never fails.
func (t *term) Write(p []byte) (int, error) {
	data := append(t.pending, p...)
	cut := incomplete(data)
	t.pending = append([]byte(nil), data[cut:]...)
	data = data[:cut]
	for len(data) > 0 {
		seq, width, n, state := ansi.DecodeSequence(data, t.state, t.parser)
		t.state = state
		data = data[n:]
		switch {
		case width > 0:
			t.print(string(seq), width)
		case len(seq) == 1:
			t.control(seq[0])
		case len(seq) > 1 && seq[0] == 0x1b:
			t.escape(seq)
		}
	}
	return len(p), nil
}

// incomplete returns where a trailing, unfinished escape sequence or UTF-8
// character starts, or len(b).
func incomplete(b []byte) int {
	for i := len(b) - 1; i >= 0 && i >= len(b)-3; i-- {
		if !utf8.RuneStart(b[i]) {
			continue
		}
		if !utf8.FullRune(b[i:]) {
			return i
		}
		break
	}
	esc := strings.LastIndexByte(string(b), 0x1b)
	if esc < 0 {
		return len(b)
	}
	rest := b[esc+1:]
	switch {
	case len(rest) == 0:
		return esc
	case rest[0] == '[':
		for _, c := range rest[1:] {
			if c >= 0x40 && c <= 0x7e {
				return len(b)
			}
		}
		return esc
	case rest[0] == ']' || rest[0] == 'P' || rest[0] == '_':
		if strings.ContainsRune(string(rest), 0x07) || strings.Contains(string(rest), "\x1b\\") {
			return len(b)
		}
		return esc
	}
	return len(b)
}

func (t *term) print(s string, width int) {
	if t.col+width > t.width {
		t.lineFeed()
	}
	r := t.touch()
	for len(r.cells) < t.col+width {
		r.cells = append(r.cells, termCell{text: " "})
	}
	r.cells[t.col] = termCell{text: s, sgr: t.sgr}
	for i := 1; i < width; i++ {
		r.cells[t.col+i] = termCell{sgr: t.sgr}
	}
	t.col += width
}

// touch returns the cursor's row, creating rows up to it, and marks it
// changed.
func (t *term) touch() *termRow {
	for len(t.rows) <= t.row {
		t.rows = append(t.rows, termRow{})
	}
	t.markDirty(t.row)
	return &t.rows[t.row]
}

func (t *term) markDirty(row int) { t.dirty = min(t.dirty, row) }

func (t *term) control(c byte) {
	switch c {
	case '\n', '\v', '\f':
		t.lineFeed()
	case '\r':
		t.col = 0
	case '\b':
		t.col = max(0, t.col-1)
	case '\t':
		t.col = min(t.width-1, (t.col/8+1)*8)
	}
}

// lineFeed moves to the start of the next row. Output is normally written
// with \r\n, and treating a bare \n the same avoids a staircase.
func (t *term) lineFeed() {
	t.row++
	t.col = 0
	t.screenTop = max(t.screenTop, t.row-t.height+1)
	t.trim()
}

func (t *term) escape(seq []byte) {
	cmd := ansi.Cmd(t.parser.Command())
	if seq[1] != '[' {
		switch cmd.Final() {
		case '7':
			t.saved = [2]int{t.row - t.screenTop, t.col}
		case '8':
			t.moveTo(t.saved[0], t.saved[1])
		case 'M': // reverse index
			t.row = max(t.screenTop, t.row-1)
		}
		return
	}
	if cmd.Prefix() != 0 || cmd.Intermediate() != 0 {
		return // private modes such as hiding the cursor
	}
	arg := func(i, def int) int {
		v, _ := t.parser.Param(i, def)
		return max(v, def)
	}
	switch cmd.Final() {
	case 'm':
		t.setSGR(string(seq))
	case 'A':
		t.row = max(t.screenTop, t.row-arg(0, 1))
	case 'B':
		t.row = min(t.screenTop+t.height-1, t.row+arg(0, 1))
	case 'C':
		t.col = min(t.width-1, t.col+arg(0, 1))
	case 'D':
		t.col = max(0, t.col-arg(0, 1))
	case 'E':
		t.row, t.col = min(t.screenTop+t.height-1, t.row+arg(0, 1)), 0
	case 'F':
		t.row, t.col = max(t.screenTop, t.row-arg(0, 1)), 0
	case 'G', '`':
		t.col = min(t.width-1, arg(0, 1)-1)
	case 'd':
		t.moveTo(arg(0, 1)-1, t.col)
	case 'H', 'f':
		t.moveTo(arg(0, 1)-1, arg(1, 1)-1)
	case 'K':
		t.eraseLine(arg(0, 0))
	case 'J':
		t.eraseDisplay(arg(0, 0))
	}
}

// moveTo moves the cursor to a screen position.
func (t *term) moveTo(row, col int) {
	t.row = t.screenTop + max(0, min(t.height-1, row))
	t.col = max(0, min(t.width-1, col))
}

func (t *term) setSGR(seq string) {
	switch {
	case seq == "\x1b[m" || seq == "\x1b[0m":
		t.sgr = ""
	case len(t.sgr) > 256: // keep the state bounded; the newest sequence wins
		t.sgr = seq
	default:
		t.sgr += seq
	}
}

func (t *term) eraseLine(mode int) {
	if t.row >= len(t.rows) {
		return
	}
	r := t.touch()
	switch mode {
	case 0:
		if t.col < len(r.cells) {
			r.cells = r.cells[:t.col]
		}
	case 1:
		for i := 0; i <= t.col && i < len(r.cells); i++ {
			r.cells[i] = termCell{text: " "}
		}
	case 2:
		r.cells = nil
	}
}

func (t *term) eraseDisplay(mode int) {
	switch mode {
	case 0: // from the cursor to the end
		t.eraseLine(0)
		if t.row+1 < len(t.rows) {
			t.rows = t.rows[:t.row+1]
			t.markDirty(t.row + 1)
		}
	case 2, 3: // the whole screen; the scrollback above it stays
		if t.screenTop < len(t.rows) {
			t.rows = t.rows[:t.screenTop]
			t.markDirty(t.screenTop)
		}
	}
}

// trim drops the oldest rows beyond the scrollback limit.
func (t *term) trim() {
	drop := len(t.rows) - maxLogLines
	if drop <= 0 {
		return
	}
	t.rows = t.rows[drop:]
	t.row = max(0, t.row-drop)
	t.screenTop = max(0, t.screenTop-drop)
	t.dirty = max(0, t.dirty-drop)
	t.dropped += drop
}

// marker adds a status line below the output; output continues below it.
func (t *term) marker(text string) {
	if t.row < len(t.rows) && (t.col > 0 || len(t.rows[t.row].cells) > 0) {
		t.row++
	}
	for len(t.rows) < t.row {
		t.rows = append(t.rows, termRow{})
	}
	t.rows = append(t.rows[:t.row], termRow{marker: text})
	t.markDirty(t.row)
	t.row++
	t.col = 0
	t.screenTop = max(t.screenTop, t.row-t.height+1)
	t.trim()
}

// flush returns the changes since the last flush: how many lines were
// dropped from the top, and the lines from index from on, which replace
// what the viewer had there.
func (t *term) flush() (dropped, from int, lines []viewLine) {
	dropped, from = t.dropped, min(t.dirty, len(t.rows))
	for _, r := range t.rows[from:] {
		lines = append(lines, r.render())
	}
	t.dropped, t.dirty = 0, len(t.rows)
	return dropped, from, lines
}

func (r termRow) render() viewLine {
	if r.marker != "" {
		return viewLine{text: r.marker, plain: r.marker, marker: true}
	}
	var text, plain strings.Builder
	sgr := ""
	for _, c := range r.cells {
		if c.sgr != sgr {
			if sgr != "" {
				text.WriteString("\x1b[0m")
			}
			text.WriteString(c.sgr)
			sgr = c.sgr
		}
		text.WriteString(c.text)
		plain.WriteString(c.text)
	}
	if sgr != "" {
		text.WriteString("\x1b[0m")
	}
	return viewLine{text: text.String(), plain: plain.String()}
}
