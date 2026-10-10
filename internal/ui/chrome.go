package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// panel draws content inside a rounded border, with a title and optional
// text on the right of the top edge and on the right of the bottom edge.
// Titles are passed pre-styled.
type panel struct {
	title, topRight, bottomRight string
	border                       style
	width                        int // outer width, including the border
	height                       int // content rows; lines are padded or cut to it
}

func (p panel) innerWidth() int { return max(0, p.width-4) }

func (p panel) render(lines []string) []string {
	if p.width < 6 {
		return lines
	}
	inner := p.innerWidth()
	out := make([]string, 0, p.height+2)
	out = append(out, p.edge("╭", "╮", p.title, p.topRight))
	for i := 0; i < p.height; i++ {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		out = append(out, p.border.Render("│")+" "+fit(line, inner)+" "+p.border.Render("│"))
	}
	return append(out, p.edge("╰", "╯", "", p.bottomRight))
}

// edge renders a top or bottom border line: ╭─ title ─────── right ─╮.
// The right text is dropped first, then the title is cut, when space runs out.
func (p panel) edge(lc, rc, title, right string) string {
	b := p.border
	space := p.width - 4 // the corners and the "─" next to each
	if right != "" && ansi.StringWidth(right)+2 > space-ansi.StringWidth(title)-3 {
		right = ""
	}
	left := b.Render(lc + "─")
	if title != "" {
		title = ansi.Truncate(title, max(0, space-2), "…")
		left += " " + title + " "
	}
	tail := b.Render("─" + rc)
	if right != "" {
		tail = " " + right + " " + tail
	}
	fill := p.width - ansi.StringWidth(left) - ansi.StringWidth(tail)
	return left + b.Render(strings.Repeat("─", max(0, fill))) + tail
}

// hintBar renders "key action" pairs that fit in width; pairs that don't
// fit are left out rather than cut in the middle.
func hintBar(pairs [][2]string, width int) string {
	var b strings.Builder
	used := 0
	for _, p := range pairs {
		w := ansi.StringWidth(p[0]) + 1 + ansi.StringWidth(p[1]) + 2
		if used+w > width {
			break
		}
		b.WriteString(" " + styleKey.Render(p[0]) + " " + styleDim.Render(p[1]) + " ")
		used += w
	}
	return b.String()
}

// painter renders row segments, adding the selection background when the
// row is selected so colours survive on the highlighted row.
type painter struct{ selected bool }

func (p painter) paint(st style, s string) string {
	if p.selected {
		st = st.Background(colorSelBg)
	}
	return st.Render(s)
}
