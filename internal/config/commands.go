// Package config reads and writes dtree's settings file.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Commands are the saved quick commands (x). Global ones are suggested in
// every container.
type Commands struct {
	Global []Command `yaml:"global,omitempty"`

	// Other keeps keys this version doesn't know, so saving doesn't drop
	// them.
	Other map[string]any `yaml:",inline"`

	path string // "" keeps the commands in memory only
}

// Command is a saved command. Alias, when set, is shown in the prompt's
// suggestions instead of the command.
//
// In the file a command without an alias is a plain string; one with an
// alias is a mapping with cmd and alias keys.
type Command struct {
	Cmd   string `yaml:"cmd"`
	Alias string `yaml:"alias,omitempty"`
}

// Label is what the prompt shows for the command.
func (c Command) Label() string {
	if c.Alias != "" {
		return c.Alias
	}
	return c.Cmd
}

func (c *Command) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*c = Command{Cmd: n.Value}
		return nil
	}
	type plain Command // without the methods, so Decode doesn't recurse
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	if p.Cmd == "" {
		return fmt.Errorf("line %d: saved command without cmd", n.Line)
	}
	*c = Command(p)
	return nil
}

func (c Command) MarshalYAML() (any, error) {
	if c.Alias == "" {
		return c.Cmd, nil
	}
	type plain Command
	return plain(c), nil
}

// DefaultPath is $XDG_CONFIG_HOME/dtree/commands.yaml, falling back to
// ~/.config when XDG_CONFIG_HOME is unset.
func DefaultPath() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "dtree", "commands.yaml"), nil
}

// LoadCommands reads the commands file at path. A missing file is not an
// error: it is created when the first command is saved.
func LoadCommands(path string) (*Commands, error) {
	c := &Commands{path: path}
	if path == "" {
		return c, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Path is where the commands are saved.
func (c *Commands) Path() string { return c.path }

// Saved reports whether cmd is a saved global command.
func (c *Commands) Saved(cmd string) bool { return c.index(cmd) >= 0 }

// Find returns the saved global command cmd.
func (c *Commands) Find(cmd string) (Command, bool) {
	if i := c.index(cmd); i >= 0 {
		return c.Global[i], true
	}
	return Command{}, false
}

// Resolve returns the command a prompt input stands for: the command of the
// saved one whose alias it is (ignoring case and surrounding spaces),
// otherwise the input itself, trimmed.
func (c *Commands) Resolve(input string) string {
	input = strings.TrimSpace(input)
	if c.Saved(input) {
		return input
	}
	for _, s := range c.Global {
		if s.Alias != "" && strings.EqualFold(s.Alias, input) {
			return s.Cmd
		}
	}
	return input
}

func (c *Commands) index(cmd string) int {
	return slices.IndexFunc(c.Global, func(s Command) bool { return s.Cmd == cmd })
}

// Toggle saves cmd as a global command, or forgets it if it is saved, and
// reports whether it is now saved.
func (c *Commands) Toggle(cmd string) (bool, error) {
	saved := c.Saved(cmd)
	err := c.change(func(cur *Commands) error {
		if saved = !cur.Saved(cmd); saved {
			cur.Global = append(cur.Global, Command{Cmd: cmd})
		} else {
			cur.Global = slices.DeleteFunc(cur.Global, func(s Command) bool { return s.Cmd == cmd })
		}
		return nil
	})
	if err != nil {
		return c.Saved(cmd), err
	}
	return saved, nil
}

// Put replaces the saved command old with cmd, keeping its place in the
// list. With old "" (or no longer saved) cmd is added at the end. Spaces
// around the command and alias are trimmed.
func (c *Commands) Put(old string, cmd Command) error {
	cmd.Cmd, cmd.Alias = strings.TrimSpace(cmd.Cmd), strings.TrimSpace(cmd.Alias)
	if cmd.Cmd == "" {
		return errors.New("the command is empty")
	}
	return c.change(func(cur *Commands) error {
		i := cur.index(old)
		if j := cur.index(cmd.Cmd); j >= 0 && j != i {
			return fmt.Errorf("%q is already saved", cmd.Cmd)
		}
		for j, s := range cur.Global {
			if j != i && cmd.Alias != "" && strings.EqualFold(s.Alias, cmd.Alias) {
				return fmt.Errorf("the alias %q is already used by %q", cmd.Alias, s.Cmd)
			}
		}
		if i < 0 {
			cur.Global = append(cur.Global, cmd)
		} else {
			cur.Global[i] = cmd
		}
		return nil
	})
}

// change applies edit to the commands and saves them. The file is read again
// first so changes made to it since dtree started are kept. Nothing changes
// when edit or saving fails.
func (c *Commands) change(edit func(cur *Commands) error) error {
	cur := &Commands{Global: slices.Clone(c.Global), Other: c.Other, path: c.path}
	if c.path != "" {
		var err error
		if cur, err = LoadCommands(c.path); err != nil {
			return err
		}
	}
	if err := edit(cur); err != nil {
		return err
	}
	if err := cur.save(); err != nil {
		return err
	}
	*c = *cur
	return nil
}

// save writes the file through a temporary file, so a failed write doesn't
// leave it half written.
func (c *Commands) save() error {
	if c.path == "" {
		return nil
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(c); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	data := buf.Bytes()
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(c.path), ".commands-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), c.path)
}
