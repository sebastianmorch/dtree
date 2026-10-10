package ui

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/sebastianmorch/dtree/internal/tree"
)

const (
	maxNameWidth  = 32
	maxImageWidth = 40
	infoLabel     = 12 // width of the "entrypoint  " label column
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
	avail := max(3, m.height-1) // the last row is the hint bar
	info := m.infoPanel(avail)
	listHeight := max(1, avail-len(info)-2)
	list := panel{
		title:    m.treeTitle(),
		topRight: m.connStatus(),
		border:   styleFocus,
		width:    m.width,
		height:   listHeight,
	}.render(m.treeBody(listHeight))
	lines := append(list, info...)
	footer := m.treeFooter()
	switch v := m.popupViewer(); {
	case m.prompting:
		lines = overlay(lines, m.promptBox(), m.width)
	case v != nil:
		lines = overlay(lines, v.box(), m.width)
		footer = v.footerWidth(m.width)
	}
	return strings.Join(append(lines, footer), "\n")
}

// listWidth is the content width inside the bordered list.
func (m *Model) listWidth() int { return max(0, m.width-4) }

// infoPanel renders the box below the list for the selected row: the
// container's or project's details. It takes at most half of the screen and
// is hidden on very short terminals.
func (m *Model) infoPanel(avail int) []string {
	r, ok := m.tree.Selected()
	if !ok || !m.loaded || m.height < minInfoHeight {
		return nil
	}
	var title, state string
	var fields []field
	if c := m.tree.Container(r); c != nil {
		title, state = c.DisplayName(), containerState(c)
		fields = containerInfo(c, m.details[c.ID])
	} else {
		p := m.tree.Project(r)
		title, state = p.Name, projectState(p)
		fields = projectInfo(p)
	}

	var lines []string
	valueWidth := max(10, m.listWidth()-infoLabel)
	for _, f := range fields {
		label := padRight(f.label, infoLabel)
		for _, part := range wrapValue(f.value, valueWidth) {
			if f.color != nil {
				part = f.color(part)
			}
			lines = append(lines, styleLabel.Render(label)+part)
			label = strings.Repeat(" ", infoLabel)
		}
	}
	if limit := avail/2 - 2; len(lines) > limit {
		if limit < 1 {
			return nil
		}
		lines = lines[:limit]
		lines[limit-1] = styleDim.Render("…")
	}
	return panel{
		title:    styleProject.Render(title),
		topRight: state,
		border:   styleBorder,
		width:    m.width,
		height:   len(lines),
	}.render(lines)
}

func (m *Model) treeTitle() string {
	scope := "running"
	if m.showAll {
		scope = "all containers"
	}
	title := styleLogo.Render("dtree") + " " + styleDim.Render(scope)
	if f := m.tree.Filter(); f != "" {
		title += styleDim.Render(" · filter: ") + styleAccent.Render(f)
	}
	return title
}

func (m *Model) connStatus() string {
	if !m.connected {
		return styleRed.Render("●") + styleDim.Render(" disconnected")
	}
	return styleGreen.Render("●") + styleDim.Render(" connected")
}

var treeHints = [][2]string{
	{"j/k", "move"}, {"h/l", "fold"}, {"enter", "shell"}, {"space", "logs"},
	{"i", "inspect"}, {"x", "run"}, {"/", "filter"}, {"a", "all"}, {"?", "help"}, {"q", "quit"},
}

func (m *Model) treeFooter() string {
	if m.prompting {
		return m.promptFooter()
	}
	if m.filtering {
		input := " " + styleAccent.Render("/") + m.tree.Filter() + styleAccent.Render("█") + "  "
		hints := hintBar([][2]string{{"enter", "keep"}, {"esc", "clear"}, {"↑/↓", "move"}},
			max(0, m.width-ansi.StringWidth(input)))
		return ansi.Truncate(input+hints, m.width, "…")
	}
	if m.status != "" {
		if m.statusErr {
			return ansi.Truncate(" "+styleError.Render("✗ "+m.status), m.width, "…")
		}
		return ansi.Truncate(" "+styleAccent.Render("› ")+m.status, m.width, "…")
	}
	hints := treeHints
	if m.tree.Filter() != "" {
		hints = append([][2]string{{"esc", "clear filter"}}, hints...)
	}
	return hintBar(hints, m.width)
}

// columns holds the widths of the compact container row; 0 hides a column.
type columns struct {
	name, image, status int
}

// layout sizes the columns to their content, then shrinks them to fit the
// list: long names first (down to 24 cells, so one long name doesn't cost
// every row its image), then the image (down to 16), then the name (down to
// 12), then the image again until it is hidden. The info panel shows the
// details in full, so nothing is lost for good.
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

	// gutter(2) + indent(2) + dot(1) + space(1) + name, then 2 spaces before each column.
	over := 6 + c.name + 2 + c.image + 2 + c.status - m.listWidth()
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

	width := m.listWidth()
	out := make([]string, 0, height)
	for i := m.treeOffset; i < len(rows) && len(out) < height; i++ {
		r := rows[i]
		p := painter{selected: i == cursor}
		gutter := p.paint(styleDim, "  ")
		if p.selected {
			gutter = p.paint(styleAccent, "┃ ")
		}
		var line string
		if r.IsProject() {
			line = gutter + m.projectRow(r, projWidth, p)
		} else {
			c := m.tree.Container(r)
			line = gutter + containerRow(c, cols, p, m.jobs[c.ID])
		}
		line = ansi.Truncate(line, width, "…")
		if p.selected {
			line += p.paint(styleDim, strings.Repeat(" ", max(0, width-ansi.StringWidth(line))))
		}
		out = append(out, line)
	}
	return out
}

func (m *Model) projectRow(r tree.Row, nameWidth int, p painter) string {
	pr := m.tree.Project(r)
	arrow := "▾"
	if m.tree.IsCollapsed(pr.Name) {
		arrow = "▸"
	}
	count := fmt.Sprintf("%d/%d running", pr.Running(), len(pr.Containers))
	if m.tree.Filter() != "" {
		count = fmt.Sprintf("%d/%d matching", m.tree.Matching(r), len(pr.Containers))
	}
	countStyle := styleDim
	switch pr.Health() {
	case tree.Degraded:
		countStyle = styleYellow
	case tree.Failing:
		countStyle = styleRed
	}
	nameStyle := styleProject
	if p.selected {
		nameStyle = nameStyle.Foreground(colorAccent)
	}
	return p.paint(styleAccent, arrow+" ") + p.paint(nameStyle, padRight(pr.Name, nameWidth)) +
		p.paint(styleDim, "  ") + p.paint(countStyle, count)
}

// containerRow renders a container; j is its last quick command, if any.
func containerRow(c *tree.Container, cols columns, p painter, j *job) string {
	dotStyle := styleDim
	switch {
	case c.Health() == tree.Degraded:
		dotStyle = styleYellow
	case c.Running():
		dotStyle = styleGreen
	}
	nameStyle := lipgloss.NewStyle()
	if p.selected {
		nameStyle = styleProject
	}

	var b strings.Builder
	b.WriteString(p.paint(dotStyle, "  ●") + p.paint(styleDim, " "))
	b.WriteString(p.paint(nameStyle, fit(c.DisplayName(), cols.name)))
	if cols.image > 0 {
		b.WriteString(p.paint(styleDim, "  ") + imageText(fit(shortImage(c.Image), cols.image), p))
	}
	statusStyle := styleDim
	if !c.Running() {
		statusStyle = styleYellow
	}
	b.WriteString(p.paint(styleDim, "  ") + p.paint(statusStyle, shortStatus(*c)))
	if icon := healthIcon(*c); icon != "" {
		b.WriteString(p.paint(styleDim, " "+icon))
	}
	if j != nil {
		b.WriteString(p.paint(styleDim, "  ") + j.rowBadge(p))
	}
	return b.String()
}

// imageText colours an image reference so its parts are easy to tell
// apart: the namespace dim, the repository name cyan and the tag (or
// digest) purple. It works on already-fitted text, so a cut tag or trailing
// padding is fine.
func imageText(s string, p painter) string {
	text := strings.TrimRight(s, " ")
	pad := s[len(text):]
	name, tag := text, ""
	if i := strings.LastIndexAny(text, ":@"); i > strings.LastIndex(text, "/") {
		name, tag = text[:i], text[i:]
	}
	ns := ""
	if i := strings.LastIndex(name, "/"); i >= 0 {
		ns, name = name[:i+1], name[i+1:]
	}
	out := p.paint(styleDim, ns) + p.paint(styleImage, name)
	if tag != "" {
		out += p.paint(styleDim, tag[:1]) + p.paint(styleTag, tag[1:])
	}
	return out + p.paint(styleDim, pad)
}

// containerInfo lists the selected container's details for the info panel.
// d holds the inspect-only fields; they are left out until it has loaded.
func containerInfo(c *tree.Container, d *containerDetails) []field {
	id := c.ID
	if len(id) > 12 {
		id = id[:12]
	}
	fields := []field{
		{"name", c.Name + " · " + id, func(s string) string {
			return colorList(s, func(item string) string {
				if item == id {
					return styleDim.Render(item)
				}
				return item
			})
		}},
		{"image", c.Image, func(s string) string { return imageText(s, painter{}) }},
		{"status", c.Status, statusColor(c)},
	}
	if c.Ports != "" {
		fields = append(fields, field{"ports", c.Ports, colorPorts})
	}
	if d != nil {
		if len(d.Entrypoint) > 0 {
			fields = append(fields, field{"entrypoint", shellJoin(d.Entrypoint), nil})
		}
		if len(d.Cmd) > 0 {
			fields = append(fields, field{"cmd", shellJoin(d.Cmd), nil})
		}
		if d.WorkingDir != "" {
			fields = append(fields, field{"workdir", d.WorkingDir, colorPath})
		}
	}
	return fields
}

// projectInfo summarises a project for the info panel. It only uses the
// listed containers, so with "running only" exited containers are not
// counted. Empty fields are left out.
func projectInfo(p *tree.Project) []field {
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

	var fields []field
	add := func(label, value string, color func(string) string) {
		if value != "" {
			fields = append(fields, field{label, value, color})
		}
	}
	plainItems := func(s string) string { return colorList(s, nil) }
	add("dir", homeRelative(dir), colorPath)
	for i, f := range files {
		if rel, ok := strings.CutPrefix(f, dir+"/"); ok && dir != "" {
			files[i] = rel
		} else {
			files[i] = homeRelative(f)
		}
	}
	add("file", strings.Join(files, ", "), func(s string) string { return colorList(s, colorPath) })
	add("state", stateSummary(states, health), colorStates)
	add("ports", portList(ports), colorHostPorts)
	add("network", strings.Join(networks, ", "), plainItems)
	add("volumes", volumeList(volumes), plainItems)
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
		{"x", "run a command (output in a popup)"},
		{"X", "show the last command's output"},
		{"a", "toggle running / all containers"},
		{"r", "refresh"},
		{"q / ctrl+c", "quit"},
	}},
	{"Logs, inspect & output", [][2]string{
		{"j/k / ↓/↑", "scroll"},
		{"^d / ^u", "half page down / up"},
		{"g / G", "top / bottom (G resumes follow)"},
		{"/  n  N", "search, next, previous"},
		{"f", "toggle follow (logs, output)"},
		{"t", "toggle timestamps (logs)"},
		{"r", "refresh (inspect)"},
		{"w", "toggle line wrap"},
		{"q / esc", "back · close popup"},
		{"ctrl+c", "stop command (popup), again to kill"},
		{"i", "type into the command (popup), esc back"},
	}},
}

func (m *Model) helpView() string {
	var cols []string
	for _, sec := range helpSections {
		keyWidth := 0
		for _, k := range sec.keys {
			keyWidth = max(keyWidth, ansi.StringWidth(k[0]))
		}
		lines := []string{styleProject.Render(sec.title)}
		for _, k := range sec.keys {
			lines = append(lines, styleKey.Render(padRight(k[0], keyWidth+2))+k[1])
		}
		cols = append(cols, strings.Join(lines, "\n"))
	}
	// Sections side by side when they fit, stacked otherwise.
	body := lipgloss.JoinHorizontal(lipgloss.Top, cols[0], "   ", cols[1])
	if lipgloss.Width(body)+4 > m.width {
		body = cols[0] + "\n\n" + cols[1]
	}
	lines := strings.Split(body, "\n")
	lines = append(lines, "", styleDim.Render("press any key to close"))
	if limit := max(1, m.height-2); len(lines) > limit {
		lines = lines[:limit]
		lines[limit-1] = styleDim.Render("… (taller window shows more)")
	}
	box := panel{
		title:  styleLogo.Render("dtree") + " " + styleDim.Render("keys"),
		border: styleFocus,
		width:  min(m.width, lipgloss.Width(body)+4),
		height: len(lines),
	}.render(lines)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, strings.Join(box, "\n"))
}
