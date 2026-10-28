package main

import "github.com/charmbracelet/lipgloss"

// Breakpoints of the session screen. As the terminal shrinks, parts of the
// screen are abbreviated and then dropped, down to a single line of the
// active window over the input line, leaving the slash commands to do what
// the hidden parts did.
const (
	// wideWidth and mediumWidth are the terminal widths at which the buddy
	// list is shown beside the main pane, at full or reduced width. Below
	// mediumWidth, the buddy list takes the main pane's place while it has
	// focus.
	wideWidth   = 90
	mediumWidth = 64
	// timestampWidth is the terminal width below which messages are shown
	// without timestamps.
	timestampWidth = 50
	// borderWidth and borderHeight are the terminal size below which the
	// panes are drawn without borders.
	borderWidth  = 40
	borderHeight = 12
	// headerHeight and helpHeight are the terminal heights below which the
	// header and the key help line are dropped.
	headerHeight = 8
	helpHeight   = 12
	// subLineHeight is the main pane height below which the line under the
	// window tabs, such as a buddy's online status, is dropped.
	subLineHeight = 6
)

// layout is the arrangement of the session screen for a terminal size.
type layout struct {
	header     bool
	help       bool
	borders    bool
	timestamps bool
	// buddyWidth is the inner width of the buddy list beside the main pane,
	// or 0 if the buddy list replaces the main pane while it has focus.
	buddyWidth int
}

func newLayout(width, height int) layout {
	l := layout{
		header:     height >= headerHeight,
		help:       height >= helpHeight,
		borders:    width >= borderWidth && height >= borderHeight,
		timestamps: width >= timestampWidth,
	}
	switch {
	case width >= wideWidth:
		l.buddyWidth = 26
	case width >= mediumWidth:
		l.buddyWidth = 18
	}
	return l
}

// zone is a clickable part of a screen line.
type zone struct {
	x, y, width int
	click       func()
}

// fit returns the first of candidates, from longest to shortest, that fits in
// width columns, or the last one truncated.
func fit(width int, candidates ...string) string {
	for _, c := range candidates {
		if lipgloss.Width(c) <= width {
			return c
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	return truncate(candidates[len(candidates)-1], width)
}

// truncate cuts each line of s to width columns.
func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	return lipgloss.NewStyle().MaxWidth(width).Render(s)
}

// padLines pads lines with blank lines at the top to n lines.
func padLines(lines []string, n int) []string {
	if pad := n - len(lines); pad > 0 {
		return append(make([]string, pad), lines...)
	}
	return lines
}
