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
	if err != nil || !slices.Equal(reread.Global, []string{"df -h", "ps aux"}) {
		t.Fatalf("reread %v, %v", reread.Global, err)
	}
	if saved, err := c.Toggle("df -h"); err != nil || saved {
		t.Fatalf("second toggle: %v, %v", saved, err)
	}
	if !slices.Equal(c.Global, []string{"ps aux"}) {
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
	if !slices.Equal(c.Global, []string{"uptime", "df -h"}) {
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
	if !slices.Equal(c.Global, []string{"df -h", "uptime"}) {
		t.Fatalf("got %v", c.Global)
	}
}
