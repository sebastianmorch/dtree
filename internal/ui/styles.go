package ui

import "github.com/charmbracelet/lipgloss"

type style = lipgloss.Style

var (
	colorGreen  = lipgloss.AdaptiveColor{Light: "#1a7f37", Dark: "#3fb950"}
	colorYellow = lipgloss.AdaptiveColor{Light: "#9a6700", Dark: "#d29922"}
	colorRed    = lipgloss.AdaptiveColor{Light: "#cf222e", Dark: "#f85149"}
	colorGrey   = lipgloss.AdaptiveColor{Light: "#6e7781", Dark: "#8b949e"}
	colorBlue   = lipgloss.AdaptiveColor{Light: "#0969da", Dark: "#58a6ff"}
	colorPurple = lipgloss.AdaptiveColor{Light: "#8250df", Dark: "#d2a8ff"}
	colorCyan   = lipgloss.AdaptiveColor{Light: "#1b7c83", Dark: "#76e3ea"}
	colorSelBg  = lipgloss.AdaptiveColor{Light: "#ddf4ff", Dark: "#264f78"}

	styleTitle    = lipgloss.NewStyle().Bold(true).Foreground(colorBlue)
	styleDim      = lipgloss.NewStyle().Foreground(colorGrey)
	styleProject  = lipgloss.NewStyle().Bold(true)
	styleSelected = lipgloss.NewStyle().Bold(true).Background(colorSelBg)
	styleGreen    = lipgloss.NewStyle().Foreground(colorGreen)
	styleYellow   = lipgloss.NewStyle().Foreground(colorYellow)
	styleRed      = lipgloss.NewStyle().Foreground(colorRed)
	styleError    = lipgloss.NewStyle().Foreground(colorRed).Bold(true)
	styleKey      = lipgloss.NewStyle().Bold(true).Foreground(colorBlue)
	styleBadge    = lipgloss.NewStyle().Bold(true).Padding(0, 1)
	styleMatch    = lipgloss.NewStyle().Background(colorYellow).Foreground(lipgloss.Color("#000000"))
	styleCurMatch = lipgloss.NewStyle().Background(colorRed).Foreground(lipgloss.Color("#ffffff")).Bold(true)
	styleMarker   = lipgloss.NewStyle().Foreground(colorGrey).Italic(true)

	styleYAMLKey    = lipgloss.NewStyle().Foreground(colorBlue)
	styleYAMLString = lipgloss.NewStyle().Foreground(colorGreen)
	styleYAMLNumber = lipgloss.NewStyle().Foreground(colorCyan)
	styleYAMLBool   = lipgloss.NewStyle().Foreground(colorPurple)
	styleYAMLNull   = lipgloss.NewStyle().Foreground(colorGrey)
	styleYAMLPunct  = lipgloss.NewStyle().Foreground(colorGrey)
)
