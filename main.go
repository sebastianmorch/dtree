// Command dtree shows Docker containers grouped by compose project, with
// vim-style navigation, a log viewer, inspect and shell access.
package main

import (
	"context"
	"fmt"
	"os"
	"runtime/debug"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	flag "github.com/spf13/pflag"

	"github.com/sebastianmorch/dtree/internal/config"
	"github.com/sebastianmorch/dtree/internal/docker"
	"github.com/sebastianmorch/dtree/internal/ui"
)

// version can be set at build time with -ldflags "-X main.version=...".
var version = "dev"

// buildVersion returns version, or when it was not set, the module version
// Go recorded in the binary: the tag for "go install ...@v0.3.0", or a
// pseudo-version for a build from a checkout.
func buildVersion() string {
	if version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return version
}

func main() {
	var all, showVersion bool
	var filter string
	flag.BoolVarP(&all, "all", "a", false, "show all containers, not only running ones")
	flag.StringVarP(&filter, "filter", "f", "", "start with the tree filtered by container or project name")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	help := flag.BoolP("help", "h", false, "show this help")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: dtree [options]\n\nOptions:\n%s", flag.CommandLine.FlagUsages())
	}
	flag.Parse()

	if *help {
		flag.Usage()
		return
	}

	if showVersion {
		fmt.Println("dtree", buildVersion())
		return
	}

	commands, err := loadCommands()
	if err != nil {
		fmt.Fprintf(os.Stderr, "dtree: reading saved commands: %v\n", err)
		os.Exit(1)
	}

	client, err := docker.New(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "dtree: %v\nIs Docker running?\n", err)
		os.Exit(1)
	}
	defer client.Close()

	m := ui.New(client, all)
	m.SetFilter(filter)
	m.SetCommands(commands)
	defer m.Close()
	// Ask the terminal for its background color now: once the program runs
	// it reads stdin and swallows the reply, and lipgloss falls back to
	// COLORFGBG, which is often stale or missing, and then to dark.
	lipgloss.HasDarkBackground()
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "dtree: %v\n", err)
		os.Exit(1)
	}
}

// loadCommands reads the saved quick commands. Without a home directory they
// are kept for this session only.
func loadCommands() (*config.Commands, error) {
	path, err := config.DefaultPath()
	if err != nil {
		path = ""
	}
	return config.LoadCommands(path)
}
