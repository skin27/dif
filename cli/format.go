package cli

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"dif/api"
)

// ANSI colors, used only when the console supports them.
const (
	red    = "\x1b[31m"
	green  = "\x1b[32m"
	yellow = "\x1b[33m"
	gray   = "\x1b[90m"
	reset  = "\x1b[0m"
)

// paint wraps s in color when on is set.
func paint(s, color string, on bool) string {
	if !on {
		return s
	}
	return color + s + reset
}

// status formats a flow state as "● STARTED", colored by state when color is set.
func status(s api.State, color bool) string {
	c := map[api.State]string{api.Started: green, api.Paused: yellow, api.Stopped: gray}[s]
	return paint("● "+strings.ToUpper(string(s)), c, color)
}

// uptime formats d in at most two units: 9s, 2m 05s, 1h 03m, 2d 04h.
func uptime(d time.Duration) string {
	s := int64(d / time.Second)
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm %02ds", s/60, s%60)
	case s < 86400:
		return fmt.Sprintf("%dh %02dm", s/3600, s%3600/60)
	}
	return fmt.Sprintf("%dd %02dh", s/86400, s%86400/3600)
}

// renderTable formats rows in aligned columns, three spaces apart. A header,
// if any, is underlined; a footer, if any, gets a rule above it. Columns with
// right set are right-aligned, for numbers. Widths ignore ANSI color codes.
func renderTable(header []string, rows [][]string, right []bool, footer []string) string {
	all := append([][]string{header, footer}, rows...)
	var widths []int
	for _, row := range all {
		for i, cell := range row {
			if i == len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], width(cell))
		}
	}
	total := 0
	for _, w := range widths {
		total += w + 3
	}
	rule := strings.Repeat("─", max(total-3, 0))

	var lines []string
	line := func(row []string) {
		var b strings.Builder
		for i, cell := range row {
			if i > 0 {
				b.WriteString("   ")
			}
			pad := strings.Repeat(" ", widths[i]-width(cell))
			if i < len(right) && right[i] {
				b.WriteString(pad + cell)
			} else {
				b.WriteString(cell + pad)
			}
		}
		lines = append(lines, strings.TrimRight(b.String(), " "))
	}
	if header != nil {
		line(header)
		lines = append(lines, rule)
	}
	for _, row := range rows {
		line(row)
	}
	if footer != nil {
		lines = append(lines, rule)
		line(footer)
	}
	return strings.Join(lines, "\n")
}

// width returns the number of characters s shows, not counting ANSI escape sequences.
func width(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\x1b' {
			if j := strings.IndexByte(s[i:], 'm'); j >= 0 {
				i += j
				continue
			}
		}
		if utf8.RuneStart(s[i]) {
			n++
		}
	}
	return n
}

// plural returns "1 flow" or "n flows".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// indent prefixes every line of s with prefix.
func indent(s, prefix string) string {
	return prefix + strings.ReplaceAll(s, "\n", "\n"+prefix)
}

// firstSentence returns the first sentence of s, without its period.
func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSuffix(s, ".")
}

// shorten cuts s to at most n characters at a word boundary, ending it with "...".
func shorten(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)[:n-3]
	if i := strings.LastIndex(string(r), " "); i > 0 {
		return strings.TrimRight(string(r)[:i], ",;:") + "..."
	}
	return string(r) + "..."
}
