package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// screenOf feeds chunks to a term and returns its rows as plain text.
func screenOf(t *testing.T, width, height int, chunks ...string) []string {
	t.Helper()
	tm := newTerm(width, height)
	v := newViewer(viewExec, "t", width, height)
	for _, c := range chunks {
		_, _ = tm.Write([]byte(c))
		v.replaceTail(tm.flush()) // sync after every chunk, as the popup does
	}
	var out []string
	for _, l := range v.lines {
		out = append(out, l.plain)
	}
	return out
}

func TestTermProgress(t *testing.T) {
	for _, tc := range []struct {
		name   string
		chunks []string
		want   []string
	}{
		{"plain lines", []string{"a\r\nb\r\n"}, []string{"a", "b"}},
		{"carriage return redraw", []string{"\rdl [#  ] 33%", "\rdl [## ] 66%", "\rdl [###] 100%", "\r\ndone\r\n"},
			[]string{"dl [###] 100%", "done"}},
		{"erase line", []string{"processing a-long-name.txt", "\r\x1b[Kok.txt\r\n"}, []string{"ok.txt"}},
		{"shorter redraw without erase keeps the tail", []string{"12345", "\rab"}, []string{"ab345"}},
		{"cursor up redraws several bars", []string{
			"layer1: 1/3\r\nlayer2: 1/3\r\n",
			"\x1b[2Alayer1: 2/3\r\nlayer2: 2/3\r\n",
			"\x1b[2Alayer1: 3/3\r\nlayer2: 3/3\r\nfinished\r\n",
		}, []string{"layer1: 3/3", "layer2: 3/3", "finished"}},
		{"cursor up stops at the screen top", []string{"old\r\n1\r\n2\r\n3\r\n", "\x1b[9AX"}, []string{"old", "1", "X", "3"}}, // the screen is 2, 3 and the blank row
		{"wraps at the width", []string{"abcdefgh\r\n"}, []string{"abcde", "fgh"}},
		{"exactly the width does not add a blank row", []string{"abcde\r\nf\r\n"}, []string{"abcde", "f"}},
		{"column and backspace", []string{"abc\bX", "\x1b[1GY", "\x1b[3CZ"}, []string{"YbX Z"}},
		{"clear screen keeps scrollback", []string{"keep\r\na\r\nb\r\nc\r\n", "\x1b[2J\x1b[Hnew"}, []string{"keep", "a", "new"}}, // a has scrolled off the screen
		{"sequence split across chunks", []string{"ab\x1b[", "2Kc\x1b", "[1Gd"}, []string{"d c"}},
		{"utf-8 split across chunks", []string{"\xc3", "\xa6\xe2\x9c", "\x93"}, []string{"æ✓"}},
		{"wide characters", []string{"日本\r\n"}, []string{"日本"}},
		{"private modes are ignored", []string{"\x1b[?25lhidden cursor\x1b[?25h\r\n"}, []string{"hidden cursor"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			width := 80
			if strings.HasPrefix(tc.name, "wraps") || strings.HasPrefix(tc.name, "exactly") {
				width = 5
			}
			height := 20
			if strings.Contains(tc.name, "screen") {
				height = 3
			}
			got := screenOf(t, width, height, tc.chunks...)
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTermUnfinishedLineShowsImmediately(t *testing.T) {
	if got := screenOf(t, 80, 20, "downloading 10%"); len(got) != 1 || got[0] != "downloading 10%" {
		t.Fatalf("got %q", got)
	}
}

func TestTermColours(t *testing.T) {
	tm := newTerm(80, 20)
	_, _ = tm.Write([]byte("\x1b[32mok\x1b[0m plain \x1b[1;31mfail\r\n"))
	_, _, lines := tm.flush()
	if got := lines[0].text; got != "\x1b[32mok\x1b[0m plain \x1b[1;31mfail\x1b[0m" {
		t.Fatalf("text %q", got)
	}
	if lines[0].plain != "ok plain fail" || ansi.Strip(lines[0].text) != lines[0].plain {
		t.Fatalf("plain %q", lines[0].plain)
	}
}

func TestTermMarkersAndScrollback(t *testing.T) {
	got := screenOf(t, 80, 20, "no newline")
	tm := newTerm(80, 20)
	v := newViewer(viewExec, "t", 80, 20)
	_, _ = tm.Write([]byte("no newline"))
	tm.marker("── exited ──")
	v.replaceTail(tm.flush())
	if len(v.lines) != 2 || v.lines[0].plain != got[0] || !v.lines[1].marker {
		t.Fatalf("lines %+v", v.lines)
	}

	_, _ = tm.Write([]byte(strings.Repeat("x\r\n", maxLogLines+5)))
	v.replaceTail(tm.flush())
	if len(v.lines) != maxLogLines || len(tm.rows) != maxLogLines {
		t.Fatalf("viewer %d, term %d lines, want %d", len(v.lines), len(tm.rows), maxLogLines)
	}
	_, _ = tm.Write([]byte("\x1b[Alast"))
	v.replaceTail(tm.flush())
	if v.lines[len(v.lines)-1].plain != "last" {
		t.Fatalf("viewer and term out of step after trimming: %q", v.lines[len(v.lines)-1].plain)
	}
}
