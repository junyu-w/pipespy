package main

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

var (
	cAccent  = lipgloss.AdaptiveColor{Light: "#7C3AED", Dark: "#A78BFA"}
	cCyan    = lipgloss.AdaptiveColor{Light: "#0E7490", Dark: "#67E8F9"}
	cGreen   = lipgloss.AdaptiveColor{Light: "#15803D", Dark: "#86EFAC"}
	cRed     = lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#FCA5A5"}
	cAmber   = lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FCD34D"}
	cText    = lipgloss.AdaptiveColor{Light: "#1F2937", Dark: "#E5E7EB"}
	cSub     = lipgloss.AdaptiveColor{Light: "#4B5563", Dark: "#9CA3AF"}
	cMuted   = lipgloss.AdaptiveColor{Light: "#9CA3AF", Dark: "#6B7280"}
	cFaint   = lipgloss.AdaptiveColor{Light: "#D1D5DB", Dark: "#3A3F4B"}
	cSelBg   = lipgloss.AdaptiveColor{Light: "#EDE9FE", Dark: "#262238"}
	cBadgeFg = lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#16131F"}
	cChipBg  = lipgloss.AdaptiveColor{Light: "#E5E7EB", Dark: "#2A2E37"}
)

// Styles are built after the renderer is bound to the TTY (see main).
var S struct {
	Text, Sub, Muted, Faint, Accent, Cyan, Green, Red, Amber lipgloss.Style
	Bold, Badge, Chip, ChipOn, Key                           lipgloss.Style
}

func initStyles() {
	f := func(c lipgloss.TerminalColor) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }
	S.Text, S.Sub, S.Muted, S.Faint = f(cText), f(cSub), f(cMuted), f(cFaint)
	S.Accent, S.Cyan, S.Green, S.Red, S.Amber = f(cAccent), f(cCyan), f(cGreen), f(cRed), f(cAmber)
	S.Bold = f(cText).Bold(true)
	S.Badge = lipgloss.NewStyle().Foreground(cBadgeFg).Background(cAccent).Bold(true).Padding(0, 1)
	S.Chip = lipgloss.NewStyle().Foreground(cMuted).Background(cChipBg).Padding(0, 1)
	S.ChipOn = lipgloss.NewStyle().Foreground(cBadgeFg).Background(cCyan).Bold(true).Padding(0, 1)
	S.Key = f(cAccent).Bold(true)
}

// fit truncates or pads a (possibly styled) string to exactly w cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if ansi.StringWidth(s) > w {
		s = ansi.Truncate(s, w, "…")
	}
	if pad := w - ansi.StringWidth(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

func trunc(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if ansi.StringWidth(s) > w {
		return ansi.Truncate(s, w, "…")
	}
	return s
}

// box draws a rounded panel with the title embedded in the top border and
// an optional scrollbar thumb (rows [thumbA, thumbB)) on the right edge.
func box(title, info string, body []string, w, h int, focused bool, thumbA, thumbB int) string {
	if w < 4 || h < 2 {
		return ""
	}
	bc := S.Faint
	if focused {
		bc = S.Accent
	}
	iw := w - 2
	t := " " + title + " "
	i := ""
	if info != "" {
		i = " " + info + " "
	}
	fill := w - 4 - ansi.StringWidth(t) - ansi.StringWidth(i)
	if fill < 0 {
		i = ""
		fill = w - 4 - ansi.StringWidth(t)
		if fill < 0 {
			t = trunc(t, w-4)
			fill = w - 4 - ansi.StringWidth(t)
		}
	}
	var b strings.Builder
	b.WriteString(bc.Render("╭─") + t + bc.Render(strings.Repeat("─", fill)) + i + bc.Render("─╮"))
	rows := h - 2
	for r := 0; r < rows; r++ {
		line := ""
		if r < len(body) {
			line = body[r]
		}
		right := bc.Render("│")
		if r >= thumbA && r < thumbB {
			right = S.Accent.Render("┃")
		}
		b.WriteString("\n" + bc.Render("│") + fit(line, iw) + right)
	}
	b.WriteString("\n" + bc.Render("╰"+strings.Repeat("─", iw)+"╯"))
	return b.String()
}

// thumb computes a scrollbar thumb for a viewport of `view` rows at `off`
// over `total` rows. Returns an empty range when everything fits.
func thumb(total, view, off int) (int, int) {
	if total <= view || view <= 0 {
		return 0, 0
	}
	size := view * view / total
	if size < 1 {
		size = 1
	}
	pos := off * (view - size) / max(1, total-view)
	return pos, pos + size
}

var sparks = []rune("▁▂▃▄▅▆▇█")

func sparkline(h []int64, style lipgloss.Style) string {
	var mx int64
	for _, v := range h {
		mx = max(mx, v)
	}
	var b strings.Builder
	for _, v := range h {
		if v == 0 || mx == 0 {
			b.WriteString(S.Faint.Render("▁"))
			continue
		}
		idx := int(v * int64(len(sparks)-1) / mx)
		b.WriteString(style.Render(string(sparks[idx])))
	}
	return b.String()
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-Z\\-_]`)

// visible renders raw line bytes as plain, terminal-safe text: escapes are
// stripped, tabs expanded, control characters shown as '·', and the result
// is the window of columns [x, x+w).
func visible(raw []byte, x, w int) string {
	s := string(raw)
	if strings.IndexByte(s, 0x1b) >= 0 {
		s = ansiRe.ReplaceAllString(s, "")
	}
	var out strings.Builder
	col := 0
	emit := func(r rune, rw int) {
		if col >= x && col+rw <= x+w {
			out.WriteRune(r)
		}
		col += rw
	}
	for _, r := range s {
		if col >= x+w {
			break
		}
		switch {
		case r == '\t':
			for n := 4 - col%4; n > 0; n-- {
				emit(' ', 1)
			}
			continue
		case r < 32 || r == 127:
			r = '·'
		}
		rw := runewidth.RuneWidth(r)
		if rw == 0 {
			continue
		}
		emit(r, rw)
	}
	return out.String()
}

func hexRow(b []byte, off, bpr int) string {
	var hx, as strings.Builder
	for i := 0; i < bpr; i++ {
		if i == bpr/2 {
			hx.WriteString(" ")
		}
		if i >= len(b) {
			hx.WriteString("   ")
			continue
		}
		c := b[i]
		st := S.Text
		ch := string(rune(c))
		switch {
		case c == 0:
			st, ch = S.Faint, "·"
		case c == ' ' || c == '\n' || c == '\t' || c == '\r':
			st, ch = S.Cyan, map[byte]string{' ': " ", '\n': "↵", '\t': "→", '\r': "←"}[c]
		case c < 32 || c == 127:
			st, ch = S.Amber, "•"
		case c > 127:
			st, ch = S.Accent, "•"
		}
		hx.WriteString(st.Render(fmt.Sprintf("%02x", c)) + " ")
		as.WriteString(st.Render(ch))
	}
	return S.Muted.Render(fmt.Sprintf("%08x", off)) + "  " + hx.String() + " " + as.String()
}

func humanBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	case n < 1<<30:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.2f GB", float64(n)/(1<<30))
}

func humanCount(n int64) string {
	switch {
	case n < 10000:
		return fmt.Sprintf("%d", n)
	case n < 1000000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%.1fM", float64(n)/1e6)
}

func humanDur(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%dµs", d.Microseconds())
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.2fs", d.Seconds())
	}
	return d.Truncate(time.Second).String()
}

func plural(n int64, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
