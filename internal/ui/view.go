package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"dtree/internal/tree"
)

const (
	maxNameWidth   = 32
	maxImageWidth  = 36
	maxStatusWidth = 30
	minPortsWidth  = 10
)

func (m *Model) View() string {
	switch {
	case m.help:
		return m.helpView()
	case m.mode == modeViewer && m.viewer != nil:
		return m.viewer.view()
	default:
		return m.treeView()
	}
}

func (m *Model) treeView() string {
	bodyHeight := max(1, m.height-2)
	body := m.treeBody(bodyHeight)
	for len(body) < bodyHeight {
		body = append(body, "")
	}
	return m.treeHeader() + "\n" + strings.Join(body, "\n") + "\n" + m.treeFooter()
}

func (m *Model) treeHeader() string {
	scope := "running"
	if m.showAll {
		scope = "all containers"
	}
	if f := m.tree.Filter(); f != "" {
		scope += " · filter: " + f
	}
	conn := styleGreen.Render("●") + styleDim.Render(" connected")
	if !m.connected {
		conn = styleRed.Render("●") + styleDim.Render(" disconnected")
	}
	return joinLeftRight(styleTitle.Render("dtree")+styleDim.Render(" · "+scope), conn, m.width)
}

func (m *Model) treeFooter() string {
	if m.filtering {
		return ansi.Truncate("/"+m.tree.Filter()+"█", m.width, "…") +
			styleDim.Render("  enter keep · esc clear · ↑/↓ move")
	}
	if m.status != "" {
		st := styleDim
		if m.statusErr {
			st = styleError
		}
		return ansi.Truncate(st.Render(m.status), m.width, "…")
	}
	hints := "j/k move · h/l fold · / filter · L logs · i inspect · s shell · a all · r refresh · ? help · q quit"
	if m.tree.Filter() != "" {
		hints = "esc clear filter · " + hints
	}
	return styleDim.Render(ansi.Truncate(hints, m.width, "…"))
}

// columns holds the widths of the container row columns; 0 hides a column.
type columns struct {
	name, image, status, ports int
}

func (m *Model) layout() columns {
	var c columns
	for _, p := range m.tree.Projects() {
		for _, ct := range p.Containers {
			c.name = max(c.name, ansi.StringWidth(ct.DisplayName()))
			c.image = max(c.image, ansi.StringWidth(ct.Image))
			c.status = max(c.status, ansi.StringWidth(ct.Status))
			c.ports = max(c.ports, ansi.StringWidth(ct.Ports))
		}
	}
	c.name = min(c.name, maxNameWidth)
	c.image = min(c.image, maxImageWidth)
	c.status = min(c.status, maxStatusWidth)

	// indent(4) + dot(2) + name, then 2 spaces before each further column.
	fixed := 6 + c.name
	if c.ports > 0 && fixed+2+c.image+2+c.status+2+minPortsWidth > m.width {
		c.ports = 0
	}
	if fixed+2+c.image+2+c.status > m.width {
		c.image = 0
	}
	if c.ports > 0 {
		c.ports = m.width - (fixed + 2 + c.image + 2 + c.status + 2)
	}
	if c.image == 0 {
		// Still too narrow: shrink the status column, then the name.
		c.status = max(0, min(c.status, m.width-fixed-2))
		c.name = max(4, min(c.name, m.width-6))
	}
	return c
}

func (m *Model) treeBody(height int) []string {
	switch {
	case m.listErr != nil && !m.loaded:
		return []string{styleError.Render("Cannot list containers: " + m.listErr.Error())}
	case !m.loaded:
		return []string{styleDim.Render("Loading…")}
	case len(m.tree.Rows()) == 0 && m.tree.Filter() != "":
		return []string{styleDim.Render(fmt.Sprintf("No containers match %q.", m.tree.Filter()))}
	case len(m.tree.Rows()) == 0:
		if m.showAll {
			return []string{styleDim.Render("No containers.")}
		}
		return []string{styleDim.Render("No running containers. Press a to show all.")}
	}

	rows := m.tree.Rows()
	cursor := m.tree.Cursor()
	// Keep the cursor visible.
	if cursor < m.treeOffset {
		m.treeOffset = cursor
	}
	if cursor >= m.treeOffset+height {
		m.treeOffset = cursor - height + 1
	}
	m.treeOffset = max(0, min(m.treeOffset, len(rows)-height))

	cols := m.layout()
	projWidth := 0
	for _, p := range m.tree.Projects() {
		projWidth = max(projWidth, ansi.StringWidth(p.Name))
	}

	out := make([]string, 0, height)
	for i := m.treeOffset; i < len(rows) && len(out) < height; i++ {
		r := rows[i]
		selected := i == cursor
		var line string
		if r.IsProject() {
			line = m.projectRow(r, projWidth, selected)
		} else {
			line = containerRow(m.tree.Container(r), cols, selected)
		}
		line = ansi.Truncate(line, m.width, "…")
		if selected {
			line = styleSelected.Render(padRight(ansi.Strip(line), m.width))
		}
		out = append(out, line)
	}
	return out
}

func (m *Model) projectRow(r tree.Row, nameWidth int, selected bool) string {
	p := m.tree.Project(r)
	arrow := "▾"
	if m.tree.IsCollapsed(p.Name) {
		arrow = "▸"
	}
	count := fmt.Sprintf("%d/%d running", p.Running(), len(p.Containers))
	if m.tree.Filter() != "" {
		count = fmt.Sprintf("%d/%d matching", m.tree.Matching(r), len(p.Containers))
	}
	if selected {
		return arrow + " " + padRight(p.Name, nameWidth) + "  " + count
	}
	countStyle := styleDim
	switch p.Health() {
	case tree.Degraded:
		countStyle = styleYellow
	case tree.Failing:
		countStyle = styleRed
	}
	return styleDim.Render(arrow) + " " + styleProject.Render(padRight(p.Name, nameWidth)) + "  " + countStyle.Render(count)
}

func containerRow(c *tree.Container, cols columns, selected bool) string {
	dot := "●"
	if !selected {
		switch {
		case c.Health() == tree.Degraded:
			dot = styleYellow.Render(dot)
		case c.Running():
			dot = styleGreen.Render(dot)
		default:
			dot = styleDim.Render(dot)
		}
	}
	dim := func(s string) string {
		if selected {
			return s
		}
		return styleDim.Render(s)
	}

	var b strings.Builder
	b.WriteString("    " + dot + " ")
	b.WriteString(fit(c.DisplayName(), cols.name))
	if cols.image > 0 {
		b.WriteString("  " + dim(fit(c.Image, cols.image)))
	}
	if cols.status > 0 {
		b.WriteString("  " + fit(c.Status, cols.status))
	}
	if cols.ports > 0 && c.Ports != "" {
		b.WriteString("  " + dim(ansi.Truncate(c.Ports, cols.ports, "…")))
	}
	return b.String()
}

// fit truncates or pads s to exactly width cells.
func fit(s string, width int) string {
	return padRight(ansi.Truncate(s, width, "…"), width)
}

func padRight(s string, width int) string {
	if w := ansi.StringWidth(s); w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return s
}

var helpSections = []struct {
	title string
	keys  [][2]string
}{
	{"Tree", [][2]string{
		{"j / ↓", "next row"},
		{"k / ↑", "previous row"},
		{"l / →", "expand project"},
		{"h / ←", "collapse project / go to parent"},
		{"enter", "toggle project · shell on container"},
		{"space", "toggle project · logs on container"},
		{"g / G", "top / bottom"},
		{"/", "filter by container or project name"},
		{"esc", "clear filter"},
		{"L", "logs"},
		{"i", "inspect"},
		{"s", "shell (bash, falls back to sh)"},
		{"a", "toggle running / all containers"},
		{"r", "refresh"},
		{"q / ctrl+c", "quit"},
	}},
	{"Logs & inspect", [][2]string{
		{"j/k  ↓/↑", "scroll"},
		{"ctrl+d / ctrl+u", "half page down / up"},
		{"g / G", "top / bottom (G resumes follow)"},
		{"/  n  N", "search, next, previous"},
		{"f", "toggle follow (logs)"},
		{"t", "toggle timestamps (logs)"},
		{"r", "refresh (inspect)"},
		{"w", "toggle line wrap"},
		{"q / esc", "back"},
	}},
}

func (m *Model) helpView() string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("dtree") + styleDim.Render(" · keys") + "\n")
	for _, s := range helpSections {
		b.WriteString("\n" + styleProject.Render(s.title) + "\n")
		for _, k := range s.keys {
			b.WriteString("  " + styleKey.Render(padRight(k[0], 18)) + k[1] + "\n")
		}
	}
	b.WriteString("\n" + styleDim.Render("press any key to close"))
	return b.String()
}
