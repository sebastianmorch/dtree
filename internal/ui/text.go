package ui

import (
	"bytes"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// sanitize makes a log line safe to embed in the TUI: colour (SGR) sequences
// are kept, every other escape or control sequence is dropped, tabs are
// expanded, and carriage-return progress output keeps only its final state.
func sanitize(s string) string {
	if i := strings.LastIndexByte(s, '\r'); i >= 0 && i < len(s)-1 {
		s = s[i+1:]
	}
	var b strings.Builder
	sgr := false
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == 0x1b && i+1 < len(s) && s[i+1] == '[': // CSI
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			if j < len(s) && s[j] == 'm' {
				b.WriteString(s[i : j+1])
				sgr = true
			}
			i = j + 1
		case c == 0x1b && i+1 < len(s) && s[i+1] == ']': // OSC, ends with BEL or ESC \
			j := i + 2
			for j < len(s) && s[j] != 0x07 && !(s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\') {
				j++
			}
			if j < len(s) && s[j] == 0x07 {
				i = j + 1
			} else {
				i = j + 2
			}
		case c == 0x1b:
			i += 2
		case c == '\t':
			b.WriteString("    ")
			i++
		case c < 0x20 || c == 0x7f:
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	if sgr {
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

// toYAML converts docker inspect JSON to block-style YAML, keeping Docker's
// key order (JSON is valid YAML, so it parses straight into a yaml.Node).
func toYAML(raw []byte) (string, error) {
	var n yaml.Node
	if err := yaml.Unmarshal(raw, &n); err != nil {
		return "", err
	}
	resetStyle(&n)
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&n); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// resetStyle drops the flow/quoted styles inherited from JSON; the encoder
// re-adds quotes where a string would otherwise be read as another type.
func resetStyle(n *yaml.Node) {
	n.Style = 0
	for _, c := range n.Content {
		resetStyle(c)
	}
}

var yamlNumber = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][-+]?[0-9]+)?$`)

// highlightYAML colours keys and values of the YAML produced by toYAML.
func highlightYAML(text string) []string {
	lines := strings.Split(text, "\n")
	out := make([]string, len(lines))
	blockIndent := -1 // >= 0 while inside a literal/folded block scalar
	for i, line := range lines {
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if blockIndent >= 0 {
			if indent > blockIndent || strings.TrimSpace(line) == "" {
				out[i] = styleYAMLString.Render(line)
				continue
			}
			blockIndent = -1
		}

		var b strings.Builder
		b.WriteString(line[:indent])
		rest := line[indent:]
		for strings.HasPrefix(rest, "- ") || rest == "-" {
			b.WriteString(styleYAMLPunct.Render("-"))
			if rest == "-" {
				rest = ""
				break
			}
			b.WriteString(" ")
			rest = rest[2:]
		}

		value := rest
		if k, v, ok := splitYAMLKey(rest); ok {
			b.WriteString(styleYAMLKey.Render(k))
			b.WriteString(styleYAMLPunct.Render(":"))
			if v == "" {
				out[i] = b.String()
				continue
			}
			b.WriteString(" ")
			value = v
		}
		if strings.HasPrefix(value, "|") || strings.HasPrefix(value, ">") {
			blockIndent = indent
		}
		b.WriteString(yamlValueStyle(value).Render(value))
		out[i] = b.String()
	}
	return out
}

// splitYAMLKey splits "key: value" or "key:" into its parts.
func splitYAMLKey(s string) (key, value string, ok bool) {
	if strings.HasPrefix(s, `"`) {
		end := 1
		for end < len(s) && s[end] != '"' {
			if s[end] == '\\' {
				end++
			}
			end++
		}
		if end >= len(s) {
			return "", "", false
		}
		after := s[end+1:]
		if after == ":" {
			return s[:end+1], "", true
		}
		if strings.HasPrefix(after, ": ") {
			return s[:end+1], after[2:], true
		}
		return "", "", false
	}
	if i := strings.Index(s, ": "); i > 0 {
		return s[:i], s[i+2:], true
	}
	if strings.HasSuffix(s, ":") && len(s) > 1 {
		return s[:len(s)-1], "", true
	}
	return "", "", false
}

func yamlValueStyle(v string) style {
	switch {
	case v == "true" || v == "false":
		return styleYAMLBool
	case v == "null" || v == "~":
		return styleYAMLNull
	case v == "{}" || v == "[]" || strings.HasPrefix(v, "|") || strings.HasPrefix(v, ">"):
		return styleYAMLPunct
	case yamlNumber.MatchString(v):
		return styleYAMLNumber
	default:
		return styleYAMLString
	}
}
