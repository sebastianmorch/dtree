// Package config reads and writes dtree's settings file.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"gopkg.in/yaml.v3"
)

// Commands are the saved quick commands (x). Global ones are suggested in
// every container.
type Commands struct {
	Global []string `yaml:"global,omitempty"`

	// Other keeps keys this version doesn't know, so saving doesn't drop
	// them.
	Other map[string]any `yaml:",inline"`

	path string // "" keeps the commands in memory only
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
func (c *Commands) Saved(cmd string) bool { return slices.Contains(c.Global, cmd) }

// Toggle saves cmd as a global command, or forgets it if it is saved, and
// reports whether it is now saved. The file is read again first so changes
// made to it since dtree started are kept.
func (c *Commands) Toggle(cmd string) (bool, error) {
	cur := &Commands{Global: slices.Clone(c.Global), Other: c.Other}
	if c.path != "" {
		var err error
		if cur, err = LoadCommands(c.path); err != nil {
			return c.Saved(cmd), err
		}
	}
	saved := !cur.Saved(cmd)
	if saved {
		cur.Global = append(cur.Global, cmd)
	} else {
		cur.Global = slices.DeleteFunc(cur.Global, func(s string) bool { return s == cmd })
	}
	if err := cur.save(); err != nil {
		return c.Saved(cmd), err
	}
	*c = *cur
	return saved, nil
}

// save writes the file through a temporary file, so a failed write doesn't
// leave it half written.
func (c *Commands) save() error {
	if c.path == "" {
		return nil
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
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
