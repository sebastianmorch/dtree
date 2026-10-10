package ui

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/sebastianmorch/dtree/internal/tree"
)

// field is one row of the info panel. color styles one wrapped line of the
// value; it gets fragments, so it must not rely on seeing the whole value.
type field struct {
	label, value string
	color        func(string) string
}

// containerState is shown on the right of the info panel's top border.
func containerState(c *tree.Container) string {
	st := styleDim
	switch {
	case c.Health() == tree.Degraded:
		st = styleYellow
	case c.Running():
		st = styleGreen
	}
	return st.Render("● " + c.State)
}

func projectState(p *tree.Project) string {
	st := styleGreen
	switch p.Health() {
	case tree.Degraded:
		st = styleYellow
	case tree.Failing:
		st = styleRed
	}
	return st.Render(fmt.Sprintf("● %d/%d running", p.Running(), len(p.Containers)))
}

var reSeparator = regexp.MustCompile(`, | · `)

// colorList dims the ", " and " · " separators and styles each item with
// item (nil leaves items plain). "+N anonymous" style notes are dimmed.
func colorList(s string, item func(string) string) string {
	var b strings.Builder
	last := 0
	emit := func(it string) {
		switch {
		case it == "":
		case strings.HasPrefix(it, "+"):
			b.WriteString(styleDim.Render(it))
		case item != nil:
			b.WriteString(item(it))
		default:
			b.WriteString(it)
		}
	}
	for _, loc := range reSeparator.FindAllStringIndex(s, -1) {
		emit(s[last:loc[0]])
		b.WriteString(styleDim.Render(s[loc[0]:loc[1]]))
		last = loc[1]
	}
	emit(s[last:])
	return b.String()
}

// colorPath dims a path's directories so the file or folder name stands out.
func colorPath(s string) string {
	i := strings.LastIndex(s, "/")
	if i < 0 || i == len(s)-1 {
		return s
	}
	return styleDim.Render(s[:i+1]) + s[i+1:]
}

// colorPorts highlights the host side of published container ports:
// "8080->80" shows 8080 in the accent colour, unpublished ports stay plain.
func colorPorts(s string) string {
	return colorList(s, func(item string) string {
		host, cport, ok := strings.Cut(item, "->")
		if !ok {
			return item
		}
		return styleAccent.Render(host) + styleDim.Render("->") + cport
	})
}

// colorHostPorts highlights a project's list of host ports ("443, 8080-8082").
func colorHostPorts(s string) string {
	return colorList(s, func(item string) string { return styleAccent.Render(item) })
}

var reHealth = regexp.MustCompile(`\((healthy|unhealthy|health: starting)\)`)

// statusColor colours Docker's status text: the state part green when
// running, yellow otherwise, and the health note by its value.
func statusColor(c *tree.Container) func(string) string {
	main := styleYellow
	if c.Running() {
		main = styleGreen
	}
	return func(s string) string {
		loc := reHealth.FindStringIndex(s)
		if loc == nil {
			return main.Render(s)
		}
		hs := styleGreen
		switch c.HealthCheck() {
		case "unhealthy":
			hs = styleRed
		case "starting":
			hs = styleYellow
		}
		return main.Render(s[:loc[0]]) + hs.Render(s[loc[0]:loc[1]]) + main.Render(s[loc[1]:])
	}
}

// colorStates colours the project state summary ("3 running · 1 exited ·
// 1 ✅") by state.
func colorStates(s string) string {
	return colorList(s, func(item string) string {
		_, state, _ := strings.Cut(item, " ")
		switch state {
		case "running":
			if strings.HasPrefix(item, "0 ") {
				return styleDim.Render(item)
			}
			return styleGreen.Render(item)
		case "paused", "created", "exited":
			return styleYellow.Render(item)
		case "restarting", "dead", "removing":
			return styleRed.Render(item)
		}
		return item
	})
}
