package ui

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"dtree/internal/tree"
)

const (
	maxNameWidth  = 32
	maxImageWidth = 40
	infoLabel     = 9  // width of the "network  " label column
	minInfoHeight = 12 // below this terminal height the info panel is hidden
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
	panel := m.infoPanel(bodyHeight)
	listHeight := bodyHeight - len(panel)
	body := m.treeBody(listHeight)
	for len(body) < listHeight {
		body = append(body, "")
	}
	body = append(body, panel...)
	return m.treeHeader() + "\n" + strings.Join(body, "\n") + "\n" + m.treeFooter()
}

// infoPanel renders the section below the list for the selected row: a
// title rule plus the container's or project's details. It takes at most
// half of the body and is hidden on very short terminals.
func (m *Model) infoPanel(bodyHeight int) []string {
	r, ok := m.tree.Selected()
	if !ok || !m.loaded || m.height < minInfoHeight {
		return nil
	}
	var title string
	var fields [][2]string
	if c := m.tree.Container(r); c != nil {
		title = c.DisplayName()
		fields = containerInfo(c)
	} else {
		p := m.tree.Project(r)
		title = p.Name
		fields = projectInfo(p)
	}

	lines := []string{styleDim.Render("─ ") + styleProject.Render(title) + " " +
		styleDim.Render(strings.Repeat("─", max(0, m.width-ansi.StringWidth(title)-3)))}
	avail := max(10, m.width-1-infoLabel)
	for _, f := range fields {
		label := padRight(f[0], infoLabel)
		for _, part := range wrapValue(f[1], avail) {
			lines = append(lines, " "+styleDim.Render(label)+part)
			label = strings.Repeat(" ", infoLabel)
		}
	}

	if limit := bodyHeight / 2; len(lines) > limit {
		lines = lines[:limit]
		lines[limit-1] = " " + styleDim.Render("…")
	}
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, m.width, "…")
	}
	return lines
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
	hints := "j/k move · h/l fold · / or f filter · L logs · i inspect · s shell · a all · r refresh · ? help · q quit"
	if m.tree.Filter() != "" {
		hints = "esc clear filter · " + hints
	}
	return styleDim.Render(ansi.Truncate(hints, m.width, "…"))
}

// columns holds the widths of the compact container row; 0 hides a column.
type columns struct {
	name, image, status int
}

// layout sizes the columns to their content, then shrinks them to fit the
// terminal: long names first (down to 24 cells, so one long name doesn't
// cost every row its image), then the image (down to 16), then the name
// (down to 12), then the image again until it is hidden. Details of the selected container
// are shown in full below its row, so nothing is lost for good.
func (m *Model) layout() columns {
	var c columns
	for _, p := range m.tree.Projects() {
		for _, ct := range p.Containers {
			c.name = max(c.name, ansi.StringWidth(ct.DisplayName()))
			c.image = max(c.image, ansi.StringWidth(shortImage(ct.Image)))
			c.status = max(c.status, ansi.StringWidth(compactStatus(ct)))
		}
	}
	c.name = min(c.name, maxNameWidth)
	c.image = min(c.image, maxImageWidth)

	// indent(4) + dot(1) + space(1) + name, then 2 spaces before each column.
	over := 6 + c.name + 2 + c.image + 2 + c.status - m.width
	shrink := func(w *int, floor int) {
		if d := min(over, max(0, *w-floor)); d > 0 {
			*w -= d
			over -= d
		}
	}
	shrink(&c.name, 24)
	shrink(&c.image, 16)
	shrink(&c.name, 12)
	shrink(&c.image, 0)
	if c.image < 6 {
		c.image = 0
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
	// Keep the cursor visible without leaving blank space at the bottom.
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
	prefix, dot := "    ", "●"
	if selected {
		prefix = "  ▶ "
	} else {
		switch {
		case c.Health() == tree.Degraded:
			dot = styleYellow.Render(dot)
		case c.Running():
			dot = styleGreen.Render(dot)
		default:
			dot = styleDim.Render(dot)
		}
	}

	var b strings.Builder
	b.WriteString(prefix + dot + " ")
	b.WriteString(fit(c.DisplayName(), cols.name))
	if cols.image > 0 {
		img := fit(shortImage(c.Image), cols.image)
		if !selected {
			img = styleDim.Render(img)
		}
		b.WriteString("  " + img)
	}
	b.WriteString("  " + compactStatus(*c))
	return b.String()
}

// containerInfo lists the selected container's details for the info panel.
func containerInfo(c *tree.Container) [][2]string {
	id := c.ID
	if len(id) > 12 {
		id = id[:12]
	}
	fields := [][2]string{
		{"name", c.Name + " · " + id},
		{"image", c.Image},
		{"status", c.Status},
	}
	if c.Ports != "" {
		fields = append(fields, [2]string{"ports", c.Ports})
	}
	return fields
}

// projectInfo summarises a project for the info panel. It only uses the
// listed containers, so with "running only" exited containers are not
// counted. Empty fields are left out.
func projectInfo(p *tree.Project) [][2]string {
	var dir string
	var files, networks, volumes []string
	var ports []int
	states := map[string]int{}
	health := map[string]int{}
	for _, c := range p.Containers {
		if dir == "" {
			dir = c.WorkingDir
		}
		files = appendNew(files, c.ConfigFiles...)
		networks = appendNew(networks, c.Networks...)
		volumes = appendNew(volumes, c.Volumes...)
		for _, hp := range c.HostPorts {
			if !slices.Contains(ports, hp) {
				ports = append(ports, hp)
			}
		}
		states[c.State]++
		if h := c.HealthCheck(); h != "" {
			health[h]++
		}
	}
	slices.Sort(networks)
	slices.Sort(volumes)
	slices.Sort(ports)

	var fields [][2]string
	add := func(label, value string) {
		if value != "" {
			fields = append(fields, [2]string{label, value})
		}
	}
	add("dir", homeRelative(dir))
	for i, f := range files {
		if rel, ok := strings.CutPrefix(f, dir+"/"); ok && dir != "" {
			files[i] = rel
		} else {
			files[i] = homeRelative(f)
		}
	}
	add("file", strings.Join(files, ", "))
	add("state", stateSummary(states, health))
	add("ports", portList(ports))
	add("network", strings.Join(networks, ", "))
	add("volumes", volumeList(volumes))
	return fields
}

// volumeList lists named volumes and only counts anonymous ones, whose
// names are 64-character hex IDs.
func volumeList(volumes []string) string {
	var named []string
	anonymous := 0
	for _, v := range volumes {
		if isHexID(v) {
			anonymous++
		} else {
			named = append(named, v)
		}
	}
	if anonymous > 0 {
		named = append(named, fmt.Sprintf("+%d anonymous", anonymous))
	}
	return strings.Join(named, ", ")
}

func isHexID(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

func appendNew(list []string, items ...string) []string {
	for _, it := range items {
		if !slices.Contains(list, it) {
			list = append(list, it)
		}
	}
	return list
}

// stateSummary renders e.g. "14 running · 2 exited · 1 ✅ · 1 ❌". Running
// is always shown; other states and health only when non-zero.
func stateSummary(states, health map[string]int) string {
	parts := []string{fmt.Sprintf("%d running", states["running"])}
	for _, st := range []string{"paused", "restarting", "exited", "created", "dead", "removing"} {
		if n := states[st]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, st))
		}
	}
	for _, h := range []struct{ key, icon string }{{"healthy", "✅"}, {"starting", "⏳"}, {"unhealthy", "❌"}} {
		if n := health[h.key]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, h.icon))
		}
	}
	return strings.Join(parts, " · ")
}

// portList renders sorted ports with consecutive runs merged: "80, 8081-8083".
func portList(ports []int) string {
	var parts []string
	for i := 0; i < len(ports); {
		j := i
		for j+1 < len(ports) && ports[j+1] == ports[j]+1 {
			j++
		}
		if i == j {
			parts = append(parts, strconv.Itoa(ports[i]))
		} else {
			parts = append(parts, strconv.Itoa(ports[i])+"-"+strconv.Itoa(ports[j]))
		}
		i = j + 1
	}
	return strings.Join(parts, ", ")
}

// homeRelative replaces the home directory prefix with "~".
func homeRelative(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, home+"/"); ok {
		return "~/" + rest
	}
	return path
}

// wrapValue wraps s to width, breaking only after spaces, "/" and ","
// so image references and port lists split at natural points. Pieces longer than
// width are hard-broken.
func wrapValue(s string, width int) []string {
	var tokens []string
	for len(s) > 0 {
		i := strings.IndexAny(s, " /,")
		if i < 0 {
			tokens = append(tokens, s)
			break
		}
		end := i + 1
		for end < len(s) && s[end] == ' ' {
			end++
		}
		tokens = append(tokens, s[:end])
		s = s[end:]
	}

	var lines []string
	line := ""
	for _, tok := range tokens {
		if line != "" && ansi.StringWidth(line+strings.TrimRight(tok, " ")) > width {
			lines = append(lines, strings.TrimRight(line, " "))
			line = ""
		}
		for ansi.StringWidth(line+tok) > width && line == "" {
			cut := ansi.Truncate(tok, width, "")
			lines = append(lines, cut)
			tok = tok[len(cut):]
		}
		line += tok
	}
	if line = strings.TrimRight(line, " "); line != "" || len(lines) == 0 {
		lines = append(lines, line)
	}
	return lines
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
		{"/ or f", "filter by container or project name"},
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
