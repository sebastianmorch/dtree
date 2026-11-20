package ui

import (
	"context"
	"fmt"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/sebastianmorch/dtree/internal/tree"
)

// lifecycleTimeout bounds a start, stop or restart. Stopping waits for the
// container's stop timeout (10s by default, but configurable) before Docker
// kills it.
const lifecycleTimeout = 2 * time.Minute

// action is a container state change started with S or R.
type action int

const (
	actStart action = iota
	actStop
	actRestart
)

// progress is shown in the row while the action runs.
func (a action) progress() string {
	return [...]string{"starting…", "stopping…", "restarting…"}[a]
}

func (a action) done() string {
	return [...]string{"started", "stopped", "restarted"}[a]
}

func (a action) verb() string {
	return [...]string{"start", "stop", "restart"}[a]
}

// lifecycleFailure is a container the action failed on.
type lifecycleFailure struct {
	name string
	err  error
}

type lifecycleMsg struct {
	ids      []string
	name     string // the container's or project's name
	project  bool
	act      action
	failures []lifecycleFailure
}

// toggleRunning stops the selected container if it is running and starts it
// otherwise. On a project it stops the running containers if there are any,
// and starts all of them otherwise.
func (m *Model) toggleRunning() tea.Cmd {
	r, ok := m.tree.Selected()
	if !ok {
		return nil
	}
	if c := m.tree.Container(r); c != nil {
		if active(*c) {
			return m.lifecycle(c.DisplayName(), false, actStop, []tree.Container{*c})
		}
		return m.lifecycle(c.DisplayName(), false, actStart, []tree.Container{*c})
	}
	p := m.tree.Project(r)
	if p.Name == tree.Standalone {
		return m.setStatus("standalone containers are not a project · select a container", true)
	}
	var running []tree.Container
	for _, c := range p.Containers {
		if active(c) {
			running = append(running, c)
		}
	}
	if len(running) > 0 {
		return m.lifecycle(p.Name, true, actStop, running)
	}
	return m.lifecycle(p.Name, true, actStart, p.Containers)
}

// restartSelected restarts the selected container, or every listed container
// of the selected project.
func (m *Model) restartSelected() tea.Cmd {
	r, ok := m.tree.Selected()
	if !ok {
		return nil
	}
	if c := m.tree.Container(r); c != nil {
		return m.lifecycle(c.DisplayName(), false, actRestart, []tree.Container{*c})
	}
	p := m.tree.Project(r)
	if p.Name == tree.Standalone {
		return m.setStatus("standalone containers are not a project · select a container", true)
	}
	return m.lifecycle(p.Name, true, actRestart, p.Containers)
}

// active reports whether S should stop c rather than start it.
func active(c tree.Container) bool {
	return c.Running() || c.State == "restarting" || c.State == "paused"
}

// lifecycle runs act on cs in parallel in the background. Their rows show the
// action until it finishes; the events stream then updates their state.
// Containers that are already busy with an action are left out.
func (m *Model) lifecycle(name string, project bool, act action, cs []tree.Container) tea.Cmd {
	var todo []tree.Container
	for _, c := range cs {
		if _, busy := m.pending[c.ID]; !busy {
			todo = append(todo, c)
		}
	}
	if len(todo) == 0 {
		if a, busy := m.pending[cs[0].ID]; busy && len(cs) == 1 {
			return m.setStatus(name+" is already "+a.progress(), true)
		}
		return m.setStatus(name+" is busy", true)
	}
	ids := make([]string, len(todo))
	for i, c := range todo {
		ids[i] = c.ID
		m.pending[c.ID] = act
	}
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), lifecycleTimeout)
		defer cancel()
		var (
			wg       sync.WaitGroup
			mu       sync.Mutex
			failures []lifecycleFailure
		)
		for _, c := range todo {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var err error
				switch act {
				case actStart:
					err = client.Start(ctx, c.ID)
				case actStop:
					err = client.Stop(ctx, c.ID)
				case actRestart:
					err = client.Restart(ctx, c.ID)
				}
				if err != nil {
					mu.Lock()
					failures = append(failures, lifecycleFailure{name: c.DisplayName(), err: err})
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		return lifecycleMsg{ids: ids, name: name, project: project, act: act, failures: failures}
	}
}

func (m *Model) lifecycleDone(msg lifecycleMsg) tea.Cmd {
	for _, id := range msg.ids {
		delete(m.pending, id)
	}
	if n := len(msg.failures); n > 0 {
		f := msg.failures[0]
		text := "could not " + msg.act.verb() + " " + f.name + ": " + f.err.Error()
		if msg.project {
			text = fmt.Sprintf("could not %s %d of %d in %s · %s: %s",
				msg.act.verb(), n, len(msg.ids), msg.name, f.name, f.err.Error())
		}
		return tea.Batch(m.list(), m.setStatus(text, true))
	}
	text := msg.act.done() + " " + msg.name
	if msg.project {
		text += fmt.Sprintf(" (%d %s)", len(msg.ids), plural(len(msg.ids), "container"))
	}
	if msg.act == actStop && !m.showAll {
		text += " · press a to show stopped containers"
	}
	return tea.Batch(m.list(), m.setStatus(text, false))
}

// projectPending returns the action running on any of p's containers.
func (m *Model) projectPending(p *tree.Project) (action, bool) {
	for _, c := range p.Containers {
		if a, ok := m.pending[c.ID]; ok {
			return a, true
		}
	}
	return 0, false
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}
