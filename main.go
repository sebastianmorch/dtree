// Command dtree shows Docker containers grouped by compose project, with
// vim-style navigation, a log viewer, inspect and shell access.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime/debug"

	tea "github.com/charmbracelet/bubbletea"

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
	flag.BoolVar(&all, "a", false, "show all containers, not only running ones")
	flag.BoolVar(&all, "all", false, "show all containers, not only running ones")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.Parse()

	if showVersion {
		fmt.Println("dtree", buildVersion())
		return
	}

	client, err := docker.New(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "dtree: %v\nIs Docker running?\n", err)
		os.Exit(1)
	}
	defer client.Close()

	m := ui.New(client, all)
	defer m.Close()
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "dtree: %v\n", err)
		os.Exit(1)
	}
}
