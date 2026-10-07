// Command dtree shows Docker containers grouped by compose project, with
// vim-style navigation, a log viewer, inspect and shell access.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"dtree/internal/docker"
	"dtree/internal/ui"
)

var version = "dev"

func main() {
	var all, showVersion bool
	flag.BoolVar(&all, "a", false, "show all containers, not only running ones")
	flag.BoolVar(&all, "all", false, "show all containers, not only running ones")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.Parse()

	if showVersion {
		fmt.Println("dtree", version)
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
