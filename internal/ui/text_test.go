package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestSanitize(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"plain", "hello world", "hello world"},
		{"tabs", "a\tb", "a    b"},
		{"keeps sgr and resets", "\x1b[31mred\x1b[0m", "\x1b[31mred\x1b[0m\x1b[0m"},
		{"drops cursor movement", "a\x1b[2Jb\x1b[1;1Hc", "abc"},
		{"drops osc title", "\x1b]0;title\x07text", "text"},
		{"carriage return keeps final state", "10%\r50%\r100%", "100%"},
		{"drops other control chars", "a\x00b\x07c", "abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitize(tt.in); got != tt.want {
				t.Fatalf("sanitize(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestToYAMLKeepsOrderAndTypes(t *testing.T) {
	raw := `{"Id":"abc","State":{"Running":true,"Pid":42},"Args":[],"Labels":{},` +
		`"Env":["A=1","B=two"],"Port":"8080","Empty":null,"Html":"<b>"}`
	got, err := toYAML([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"Id: abc",
		"State:",
		"  Running: true",
		"  Pid: 42",
		"Args: []",
		"Labels: {}",
		"Env:",
		"  - A=1",
		"  - B=two",
		`Port: "8080"`,
		"Empty: null",
		"Html: <b>",
	}, "\n")
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestHighlightYAMLPreservesText(t *testing.T) {
	text := strings.Join([]string{
		"Id: abc",
		"State:",
		"  Running: true",
		"Env:",
		"  - A=1",
		"  - - nested",
		`"weird: key": "8080"`,
		"Script: |",
		"  echo: not a key",
		"Next: 1",
	}, "\n")
	lines := highlightYAML(text)
	if got := ansi.Strip(strings.Join(lines, "\n")); got != text {
		t.Fatalf("highlighting changed text:\n%s", got)
	}
}

func TestSplitYAMLKey(t *testing.T) {
	tests := []struct {
		in, key, value string
		ok             bool
	}{
		{"Id: abc", "Id", "abc", true},
		{"State:", "State", "", true},
		{"http://x", "", "", false},
		{`"a: b": c`, `"a: b"`, "c", true},
		{"com.docker.compose.project: local", "com.docker.compose.project", "local", true},
	}
	for _, tt := range tests {
		k, v, ok := splitYAMLKey(tt.in)
		if k != tt.key || v != tt.value || ok != tt.ok {
			t.Errorf("splitYAMLKey(%q) = %q, %q, %v", tt.in, k, v, ok)
		}
	}
}
