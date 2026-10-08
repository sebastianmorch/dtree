package ui

import (
	"regexp"
	"strings"

	"dtree/internal/tree"
)

// shortImage drops the registry host (and Docker Hub's "library/") so the
// compact row shows "test-hub:branch_master" instead of
// "594489062793.dkr.ecr.eu-central-1.amazonaws.com/test-hub:branch_master".
// A digest is dropped when a tag is present.
func shortImage(image string) string {
	if first, rest, ok := strings.Cut(image, "/"); ok &&
		(strings.ContainsAny(first, ".:") || first == "localhost") {
		image = rest
	}
	image = strings.TrimPrefix(image, "library/")
	if name, _, ok := strings.Cut(image, "@"); ok && strings.Contains(name, ":") {
		image = name
	}
	return image
}

var (
	reUp     = regexp.MustCompile(`^Up (.+?)(?: \(.*\))?$`)
	reExited = regexp.MustCompile(`^Exited \(-?\d+\) (.+) ago$`)
	reAmount = regexp.MustCompile(`^(\d+|About an?|Less than a) (second|minute|hour|day|week|month|year)s?$`)
)

var durationUnits = map[string]string{
	"second": "s", "minute": "m", "hour": "h", "day": "d", "week": "w", "month": "mo", "year": "y",
}

// shortStatus condenses Docker's status text: "Up 16 hours (healthy)" ->
// "16h", "Exited (1) 5 minutes ago" -> "exited 5m". The full text is shown in
// the selected container's details.
func shortStatus(c tree.Container) string {
	switch c.State {
	case "running":
		if m := reUp.FindStringSubmatch(c.Status); m != nil {
			return shortDuration(m[1])
		}
	case "exited":
		if m := reExited.FindStringSubmatch(c.Status); m != nil {
			return "exited " + shortDuration(m[1])
		}
		return "exited"
	}
	if c.State != "" {
		return c.State // paused, restarting, created, removing, dead
	}
	return c.Status
}

// shortDuration turns Docker's human durations ("16 hours", "About a
// minute", "Less than a second") into "16h", "1m", "<1s".
func shortDuration(d string) string {
	m := reAmount.FindStringSubmatch(d)
	if m == nil {
		return d
	}
	n := m[1]
	switch {
	case strings.HasPrefix(n, "About"):
		n = "1"
	case strings.HasPrefix(n, "Less"):
		n = "<1"
	}
	return n + durationUnits[m[2]]
}

// healthIcon is shown after the status for containers with a healthcheck.
func healthIcon(c tree.Container) string {
	switch c.HealthCheck() {
	case "healthy":
		return "✅"
	case "starting":
		return "⏳"
	case "unhealthy":
		return "❌"
	}
	return ""
}

// compactStatus is the short status plus the health icon, if any.
func compactStatus(c tree.Container) string {
	s := shortStatus(c)
	if icon := healthIcon(c); icon != "" {
		s += " " + icon
	}
	return s
}
