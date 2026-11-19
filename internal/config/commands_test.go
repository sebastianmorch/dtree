package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestMissingFileIsEmpty(t *testing.T) {
	c, err := LoadCommands(filepath.Join(t.TempDir(), "nope", "commands.yaml"))
	if err != nil || len(c.Global) != 0 {
		t.Fatalf("got %v, %v", c.Global, err)
	}
}

func TestToggleSavesAndForgets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dtree", "commands.yaml")
	c, err := LoadCommands(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{"df -h", "ps aux"} {
		if saved, err := c.Toggle(cmd); err != nil || !saved {
			t.Fatalf("toggle %q: %v, %v", cmd, saved, err)
		}
	}
	reread, err := LoadCommands(path)
	if err != nil || !slices.Equal(cmds(reread), []string{"df -h", "ps aux"}) {
		t.Fatalf("reread %v, %v", reread.Global, err)
	}
	if saved, err := c.Toggle("df -h"); err != nil || saved {
		t.Fatalf("second toggle: %v, %v", saved, err)
	}
	if !slices.Equal(cmds(c), []string{"ps aux"}) {
		t.Fatalf("after forgetting: %v", c.Global)
	}
}

func TestToggleKeepsOutsideChangesAndUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commands.yaml")
	c, err := LoadCommands(path)
	if err != nil {
		t.Fatal(err)
	}
	// Edited by hand (or another dtree) after this one started.
	if err := os.WriteFile(path, []byte("global:\n  - uptime\ncontainers:\n  web:\n    - rails c\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Toggle("df -h"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	for _, want := range []string{"- uptime", "- df -h", "containers:", "- rails c"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("file missing %q:\n%s", want, data)
		}
	}
	if !slices.Equal(cmds(c), []string{"uptime", "df -h"}) {
		t.Errorf("in memory: %v", c.Global)
	}
}

func TestToggleLeavesBrokenFileAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commands.yaml")
	c, _ := LoadCommands(path)
	if err := os.WriteFile(path, []byte("global: [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Toggle("df -h"); err == nil {
		t.Fatal("expected a parse error")
	}
	if data, _ := os.ReadFile(path); string(data) != "global: [\n" {
		t.Fatalf("file was overwritten: %q", data)
	}
}

func TestDefaultPathUsesXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/cfg")
	if p, _ := DefaultPath(); p != "/cfg/dtree/commands.yaml" {
		t.Fatalf("path %q", p)
	}
}

func TestToggleInMemory(t *testing.T) {
	c, _ := LoadCommands("")
	c.Toggle("df -h")
	c.Toggle("uptime")
	if !slices.Equal(cmds(c), []string{"df -h", "uptime"}) {
		t.Fatalf("got %v", c.Global)
	}
}

// cmds lists the saved commands without their aliases.
func cmds(c *Commands) []string {
	var out []string
	for _, s := range c.Global {
		out = append(out, s.Cmd)
	}
	return out
}

func TestAliasesRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commands.yaml")
	in := "global:\n  - df -h\n  - cmd: tail -f storage/logs/laravel.log\n    alias: laravel log\n"
	if err := os.WriteFile(path, []byte(in), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := LoadCommands(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []Command{{Cmd: "df -h"}, {Cmd: "tail -f storage/logs/laravel.log", Alias: "laravel log"}}
	if !slices.Equal(c.Global, want) {
		t.Fatalf("loaded %v", c.Global)
	}
	if c.Global[0].Label() != "df -h" || c.Global[1].Label() != "laravel log" {
		t.Fatalf("labels %q, %q", c.Global[0].Label(), c.Global[1].Label())
	}
	if _, err := c.Toggle("uptime"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != in+"  - uptime\n" {
		t.Fatalf("written:\n%s", data)
	}
}

func TestAliasWithoutCmdIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commands.yaml")
	if err := os.WriteFile(path, []byte("global:\n  - alias: oops\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCommands(path); err == nil || !strings.Contains(err.Error(), "without cmd") {
		t.Fatalf("got %v", err)
	}
}

func TestPut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commands.yaml")
	c, _ := LoadCommands(path)
	c.Toggle("df -h")
	c.Toggle("ps aux")

	// Editing keeps the command's place; spaces are trimmed.
	if err := c.Put("df -h", Command{Cmd: " df -h / ", Alias: " disk "}); err != nil {
		t.Fatal(err)
	}
	want := []Command{{Cmd: "df -h /", Alias: "disk"}, {Cmd: "ps aux"}}
	if !slices.Equal(c.Global, want) {
		t.Fatalf("after edit %v", c.Global)
	}
	if reread, _ := LoadCommands(path); !slices.Equal(reread.Global, want) {
		t.Fatalf("reread %v", reread.Global)
	}

	// Removing the alias.
	if err := c.Put("df -h /", Command{Cmd: "df -h /"}); err != nil || c.Global[0] != (Command{Cmd: "df -h /"}) {
		t.Fatalf("clear alias: %v, %v", c.Global, err)
	}

	// Adding a new one.
	if err := c.Put("", Command{Cmd: "uptime", Alias: "up"}); err != nil || c.Global[2] != (Command{Cmd: "uptime", Alias: "up"}) {
		t.Fatalf("add: %v, %v", c.Global, err)
	}

	for _, tc := range []struct {
		old  string
		cmd  Command
		want string
	}{
		{"ps aux", Command{Cmd: "uptime"}, "already saved"},
		{"", Command{Cmd: "ps aux"}, "already saved"},
		{"ps aux", Command{Cmd: "  ", Alias: "x"}, "empty"},
		{"ps aux", Command{Cmd: "ps aux", Alias: "UP"}, "alias \"UP\" is already used"},
	} {
		if err := c.Put(tc.old, tc.cmd); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Put(%q, %v) = %v, want %q", tc.old, tc.cmd, err, tc.want)
		}
	}
	if len(c.Global) != 3 {
		t.Fatalf("failed puts changed the list: %v", c.Global)
	}
}

func TestResolve(t *testing.T) {
	c, _ := LoadCommands("")
	c.Put("", Command{Cmd: "tail -f app.log", Alias: "logs"})
	c.Put("", Command{Cmd: "ls", Alias: "list"})
	c.Put("", Command{Cmd: "list", Alias: "x"}) // a command named like another's alias
	for in, want := range map[string]string{
		" Logs ":          "tail -f app.log",
		"tail -f app.log": "tail -f app.log",
		"list":            "list", // a saved command wins over an alias
		"df -h ":          "df -h",
		"":                "",
	} {
		if got := c.Resolve(in); got != want {
			t.Errorf("Resolve(%q) = %q, want %q", in, got, want)
		}
	}
}
