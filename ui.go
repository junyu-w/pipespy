package main

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const (
	focusSide = iota
	focusIn
	focusOut
	focusErr
)

var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type tickMsg time.Time

func tick() tea.Cmd {
	return tea.Tick(80*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

type scroll struct {
	y, x   int
	follow bool
}

type lineSet struct {
	bytes int64
	set   map[string]struct{}
}

type model struct {
	pl      *Pipeline
	shell   string
	toPipe  bool // our stdout is not a terminal
	w, h    int
	sel     int
	focus   int
	sc      [4]scroll
	showErr bool
	diff    bool
	hexFlip bool
	sync    bool
	editing bool
	input   textinput.Model
	frame   int
	ticking bool
	flash   string
	flashAt time.Time
	sets    map[*Capture]lineSet
	sideOff int
	sideMap []int // visible sidebar row -> stage index (-1 for none)
}

func newModel(pl *Pipeline, shell string, toPipe bool) *model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.TextStyle = S.Text
	ti.Cursor.Style = S.Accent
	return &model{
		pl: pl, shell: shell, toPipe: toPipe, focus: focusOut,
		showErr: true, diff: true, input: ti, ticking: true,
		sets: map[*Capture]lineSet{},
	}
}

func (m *model) Init() tea.Cmd { return tick() }

// ---------------------------------------------------------------- layout

type layout struct {
	sideW, rightW, bodyH int
	inW, inH, outW, outH int
	errH, panesH         int
	stacked              bool
	rightX, panesY, errY int
}

func (m *model) layout() layout {
	var l layout
	l.bodyH = max(m.h-2, 3)
	l.sideW = min(max(m.w*3/10, 30), 46)
	if m.w < 80 {
		l.sideW = min(26, m.w/2)
	}
	l.rightW = m.w - l.sideW - 1
	l.rightX = l.sideW + 1
	if m.showErr && len(m.pl.Stages) > 0 {
		if n := m.pl.Stages[m.sel].Stderr.Stats().StoredLines; n > 0 {
			l.errH = min(n, 6) + 2
		}
	}
	l.panesH = l.bodyH - 1 - l.errH
	l.panesY = 2
	l.errY = l.panesY + l.panesH
	if l.rightW >= 72 {
		l.inW = (l.rightW - 1) / 2
		l.outW = l.rightW - 1 - l.inW
		l.inH, l.outH = l.panesH, l.panesH
	} else {
		l.stacked = true
		l.inW, l.outW = l.rightW, l.rightW
		l.inH = l.panesH / 2
		l.outH = l.panesH - l.inH
	}
	return l
}

func (m *model) paneCap(p int) *Capture {
	switch p {
	case focusIn:
		return m.pl.Links[m.sel]
	case focusOut:
		return m.pl.Links[m.sel+1]
	case focusErr:
		return m.pl.Stages[m.sel].Stderr
	}
	return nil
}

func (m *model) paneSize(p int) (w, h int) {
	l := m.layout()
	switch p {
	case focusIn:
		return l.inW, l.inH
	case focusOut:
		return l.outW, l.outH
	case focusErr:
		return l.rightW, l.errH
	}
	return 0, 0
}

func (m *model) isHex(c *Capture) bool { return c.Stats().Binary != m.hexFlip }

func bytesPerRow(innerW int) int {
	if innerW >= 78 {
		return 16
	}
	return 8
}

// rows returns the total number of display rows and the viewport height.
func (m *model) rows(p int) (total, view int) {
	c := m.paneCap(p)
	w, h := m.paneSize(p)
	st := c.Stats()
	if m.isHex(c) {
		bpr := bytesPerRow(w - 2)
		return (st.StoredBytes + bpr - 1) / bpr, h - 2
	}
	return st.StoredLines, h - 2
}

func (m *model) clamp(p int) {
	total, view := m.rows(p)
	s := &m.sc[p]
	if s.follow {
		s.y = total - view
	}
	s.y = max(0, min(s.y, total-view))
	s.x = max(0, s.x)
}

func (m *model) scrollBy(p, dy int) {
	targets := []int{p}
	if m.sync && (p == focusIn || p == focusOut) {
		targets = []int{focusIn, focusOut}
	}
	for _, t := range targets {
		m.sc[t].y += dy
		if dy < 0 {
			m.sc[t].follow = false
		}
		m.clamp(t)
	}
}

func (m *model) panBy(p, dx int) {
	targets := []int{p}
	if m.sync && (p == focusIn || p == focusOut) {
		targets = []int{focusIn, focusOut}
	}
	for _, t := range targets {
		m.sc[t].x = max(0, m.sc[t].x+dx)
	}
}

func (m *model) selectStage(i int) {
	i = max(0, min(i, len(m.pl.Stages)-1))
	if i != m.sel {
		m.sel = i
		m.sc = [4]scroll{}
	}
}

func (m *model) setFlash(s string) {
	m.flash, m.flashAt = s, time.Now()
}

func (m *model) focusOrder() []int {
	f := []int{focusSide, focusIn, focusOut}
	if m.layout().errH > 0 {
		f = append(f, focusErr)
	}
	return f
}

func (m *model) cycleFocus(d int) {
	order := m.focusOrder()
	idx := 0
	for i, f := range order {
		if f == m.focus {
			idx = i
		}
	}
	m.focus = order[(idx+d+len(order))%len(order)]
}

// ---------------------------------------------------------------- update

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.input.Width = max(10, m.w-8)
		return m, nil

	case tickMsg:
		m.frame++
		for _, p := range []int{focusIn, focusOut, focusErr} {
			if m.sc[p].follow {
				m.clamp(p)
			}
		}
		if !m.pl.Done() || (m.flash != "" && time.Since(m.flashAt) < 4*time.Second) {
			return m, tick()
		}
		m.ticking = false
		return m, nil

	case tea.MouseMsg:
		return m, m.mouse(msg)

	case tea.KeyMsg:
		if m.editing {
			return m.updateEdit(msg)
		}
		return m.key(msg)
	}
	return m, nil
}

func (m *model) wake() tea.Cmd {
	if m.ticking {
		return nil
	}
	m.ticking = true
	return tick()
}

func (m *model) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	_, view := m.rows(max(m.focus, focusIn))
	half := max(1, view/2)
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "tab":
		m.cycleFocus(1)
	case "shift+tab":
		m.cycleFocus(-1)
	case "up", "k":
		if m.focus == focusSide {
			m.selectStage(m.sel - 1)
		} else {
			m.scrollBy(m.focus, -1)
		}
	case "down", "j":
		if m.focus == focusSide {
			m.selectStage(m.sel + 1)
		} else {
			m.scrollBy(m.focus, 1)
		}
	case "[", "K", "shift+up":
		m.selectStage(m.sel - 1)
	case "]", "J", "shift+down":
		m.selectStage(m.sel + 1)
	case "pgup", "ctrl+u", "b":
		if m.focus != focusSide {
			m.scrollBy(m.focus, -half)
		}
	case "pgdown", "ctrl+d", " ":
		if m.focus != focusSide {
			m.scrollBy(m.focus, half)
		}
	case "left", "h":
		if m.focus == focusSide {
			break
		}
		m.panBy(m.focus, -8)
	case "right", "l":
		if m.focus == focusSide {
			m.focus = focusIn
			break
		}
		m.panBy(m.focus, 8)
	case "g", "home":
		if m.focus != focusSide {
			m.scrollBy(m.focus, -1<<30)
		} else {
			m.selectStage(0)
		}
	case "G", "end":
		if m.focus == focusSide {
			m.selectStage(len(m.pl.Stages) - 1)
			break
		}
		m.sc[m.focus].follow = true
		m.clamp(m.focus)
		if m.sync && m.focus != focusErr {
			o := focusIn + focusOut - m.focus
			m.sc[o].follow = true
			m.clamp(o)
		}
	case "d":
		m.diff = !m.diff
		m.setFlash(onOff("diff highlighting", m.diff))
		return m, m.wake()
	case "x":
		m.hexFlip = !m.hexFlip
		m.sc[focusIn], m.sc[focusOut] = scroll{}, scroll{}
	case "s":
		m.sync = !m.sync
		if m.sync {
			m.sc[focusOut].y = m.sc[focusIn].y
			m.clamp(focusIn)
			m.clamp(focusOut)
		}
		m.setFlash(onOff("synced scrolling", m.sync))
		return m, m.wake()
	case "e":
		m.showErr = !m.showErr
		if !m.showErr && m.focus == focusErr {
			m.focus = focusOut
		}
	case "r":
		return m, m.restart(m.pl.Cmdline)
	case "i", ":", "/", "enter":
		m.editing = true
		m.input.SetValue(m.pl.Cmdline)
		m.input.CursorEnd()
		return m, m.input.Focus()
	default:
		if s := msg.String(); len(s) == 1 && s[0] >= '1' && s[0] <= '9' {
			m.selectStage(int(s[0] - '1'))
		}
	}
	return m, nil
}

func onOff(what string, on bool) string {
	if on {
		return what + " on"
	}
	return what + " off"
}

func (m *model) updateEdit(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.editing = false
		m.input.Blur()
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	case "enter":
		m.editing = false
		m.input.Blur()
		return m, m.restart(m.input.Value())
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *model) restart(cmdline string) tea.Cmd {
	var src io.Reader
	note := ""
	if m.pl.HasStdin {
		st := m.pl.Links[0].Stats()
		if !st.Done {
			m.setFlash("can't re-run yet: stdin is still streaming")
			return m.wake()
		}
		if st.Truncated {
			m.setFlash("can't re-run: stdin exceeded the capture limit")
			return m.wake()
		}
		if st.CutShort {
			note = " (stdin was cut short by stage 1; replaying what was read)"
		}
		src = bytes.NewReader(m.pl.Links[0].All())
	}
	pl, err := NewPipeline(cmdline, m.shell)
	if err != nil {
		m.setFlash("✕ " + err.Error())
		return m.wake()
	}
	m.pl.Kill()
	pl.Run(src)
	m.pl = pl
	m.sc = [4]scroll{}
	m.sets = map[*Capture]lineSet{}
	m.sel = min(m.sel, len(pl.Stages)-1)
	m.setFlash("↻ re-ran pipeline" + note)
	return m.wake()
}

func (m *model) mouse(msg tea.MouseMsg) tea.Cmd {
	l := m.layout()
	target := -1
	switch {
	case msg.X < l.sideW:
		target = focusSide
	case msg.Y >= l.errY && l.errH > 0:
		target = focusErr
	case l.stacked && msg.Y >= l.panesY+l.inH:
		target = focusOut
	case l.stacked:
		target = focusIn
	case msg.X >= l.rightX+l.inW+1:
		target = focusOut
	default:
		target = focusIn
	}
	if msg.Y < 1 || msg.Y > l.bodyH {
		return nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
		d := 3
		if msg.Button == tea.MouseButtonWheelUp {
			d = -3
		}
		if target == focusSide {
			m.selectStage(m.sel + d/3)
		} else {
			m.scrollBy(target, d)
		}
	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionPress {
			return nil
		}
		m.focus = target
		if target == focusSide {
			row := msg.Y - 2 // header + top border
			if row >= 0 && row < len(m.sideMap) && m.sideMap[row] >= 0 {
				m.selectStage(m.sideMap[row])
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------- view

func (m *model) View() string {
	if m.w == 0 {
		return ""
	}
	if m.w < 50 || m.h < 12 {
		return lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center,
			S.Muted.Render("pipespy needs at least 50×12"))
	}
	l := m.layout()
	for _, p := range []int{focusIn, focusOut, focusErr} {
		m.clamp(p)
	}

	side := m.viewSidebar(l.sideW, l.bodyH)
	var panes string
	in := m.viewPane(focusIn, l.inW, l.inH)
	out := m.viewPane(focusOut, l.outW, l.outH)
	if l.stacked {
		panes = in + "\n" + out
	} else {
		panes = lipgloss.JoinHorizontal(lipgloss.Top, in, " ", out)
	}
	right := m.viewStats(l.rightW) + "\n" + panes
	if l.errH > 0 {
		right += "\n" + m.viewPane(focusErr, l.rightW, l.errH)
	}
	body := lipgloss.JoinHorizontal(lipgloss.Top, side, " ", right)
	return m.viewHeader() + "\n" + body + "\n" + m.viewFooter()
}

func (m *model) overall() (icon, label string, style lipgloss.Style) {
	failed := -1
	for i, s := range m.pl.Stages {
		if s.Info().State == Failed && failed < 0 {
			failed = i
		}
	}
	switch {
	case !m.pl.Done():
		return spinner[m.frame%len(spinner)], "running", S.Amber
	case failed >= 0:
		return "✕", fmt.Sprintf("stage %d failed", failed+1), S.Red
	}
	return "✓", "done", S.Green
}

func (m *model) viewHeader() string {
	icon, label, st := m.overall()
	end := time.Now()
	if m.pl.Done() {
		end = m.pl.End()
	}
	right := st.Render(icon+" "+label) + S.Muted.Render(" · "+humanDur(end.Sub(m.pl.Start))+" ")
	left := S.Badge.Render("◆ pipespy") + " "
	n := len(m.pl.Stages)
	left += S.Sub.Render(fmt.Sprintf("%d %s", n, plural(int64(n), "stage", "stages"))) + S.Faint.Render("  │  ")
	room := m.w - ansi.StringWidth(left) - ansi.StringWidth(right) - 2
	cmd := S.Muted.Render(trunc(oneLine(m.pl.Cmdline), room))
	gap := m.w - ansi.StringWidth(left) - ansi.StringWidth(cmd) - ansi.StringWidth(right)
	return left + cmd + strings.Repeat(" ", max(1, gap)) + right
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// sidebar --------------------------------------------------------------

type sideLine struct {
	text  string
	stage int
	sel   bool
}

func (m *model) viewSidebar(w, h int) string {
	iw := w - 2
	var lines []sideLine
	add := func(s string, stage int, sel bool) { lines = append(lines, sideLine{s, stage, sel}) }

	src := "none"
	if m.pl.HasStdin {
		src = "pipe"
	}
	add(endpoint("stdin", src, iw), -1, false)
	add(m.connector(m.pl.Links[0], iw, false), -1, false)
	selRow := 0
	for i, s := range m.pl.Stages {
		sel := i == m.sel
		if sel {
			selRow = len(lines)
		}
		a, b := m.stageRows(i, s, iw, sel)
		add(a, i, sel)
		add(b, i, sel)
		add(m.connector(m.pl.Links[i+1], iw, i == len(m.pl.Stages)-1), -1, false)
	}
	dst := "terminal"
	if m.toPipe {
		dst = "pipe on exit"
	}
	add(endpoint("stdout", dst, iw), -1, false)

	view := h - 2
	if selRow < m.sideOff {
		m.sideOff = selRow
	}
	if selRow+3 > m.sideOff+view {
		m.sideOff = selRow + 3 - view
	}
	m.sideOff = max(0, min(m.sideOff, len(lines)-view))
	m.sideMap = m.sideMap[:0]
	var body []string
	for i := m.sideOff; i < len(lines) && len(body) < view; i++ {
		ln := lines[i]
		txt := fit(ln.text, iw)
		if ln.sel {
			if pad := iw - ansi.StringWidth(ln.text); pad > 0 {
				txt = ln.text + lipgloss.NewStyle().Background(cSelBg).Render(strings.Repeat(" ", pad))
			}
		}
		body = append(body, txt)
		m.sideMap = append(m.sideMap, ln.stage)
	}
	a, b := thumb(len(lines), view, m.sideOff)
	return box(paneTitle("PIPELINE", m.focus == focusSide), "", body, w, h, m.focus == focusSide, a, b)
}

func endpoint(name, desc string, w int) string {
	l := " " + S.Muted.Render("◇ ") + S.Sub.Bold(true).Render(name)
	r := S.Muted.Render(desc) + " "
	return l + strings.Repeat(" ", max(1, w-ansi.StringWidth(l)-ansi.StringWidth(r))) + r
}

func (m *model) connector(c *Capture, w int, last bool) string {
	st := c.Stats()
	live := !st.Done && time.Since(st.Last) < 300*time.Millisecond
	rail := S.Faint.Render("│")
	if live {
		rail = S.Cyan.Render([]string{"┃", "╏"}[m.frame/3%2])
	}
	end := time.Now()
	if m.pl.Done() {
		end = m.pl.End()
	}
	if c == m.pl.Links[0] && !m.pl.HasStdin {
		return "  " + rail + "  " + S.Faint.Render("no input")
	}
	spark := sparkline(c.Histogram(m.pl.Start, end, 8), S.Cyan)
	stats := humanCount(st.Lines) + " ln · " + humanBytes(st.Bytes)
	if st.Bytes == 0 {
		stats = "empty"
		if !st.Done {
			stats = "…"
		}
	}
	extra := ""
	if st.Truncated {
		extra = S.Amber.Render(" ✂")
	}
	return "  " + rail + "  " + spark + "  " + S.Muted.Render(stats) + extra
}

func (m *model) stageRows(i int, s *Stage, w int, sel bool) (string, string) {
	bg := func(st lipgloss.Style) lipgloss.Style {
		if sel {
			return st.Background(cSelBg)
		}
		return st
	}
	sp := bg(lipgloss.NewStyle()).Render
	info := s.Info()
	var icon, detail string
	dur := bg(S.Muted).Render(" · " + humanDur(info.Duration()))
	switch info.State {
	case Pending:
		icon, detail = bg(S.Muted).Render("○"), bg(S.Muted).Render("waiting")
	case Running:
		icon = bg(S.Amber).Render(spinner[m.frame%len(spinner)])
		detail = bg(S.Amber).Render("running") + dur
	case Exited:
		icon, detail = bg(S.Green).Render("●"), bg(S.Green).Render("exit 0")+dur
	case PipeClosed:
		icon, detail = bg(S.Amber).Render("◐"), bg(S.Amber).Render("SIGPIPE")+dur
	case Killed:
		icon, detail = bg(S.Muted).Render("○"), bg(S.Muted).Render("killed")
	case Failed:
		lbl := fmt.Sprintf("exit %d", info.Code)
		if info.Err != "" {
			lbl = info.Err
		}
		icon, detail = bg(S.Red).Render("✕"), bg(S.Red).Render(lbl)+dur
	}
	if n := s.Stderr.Stats().Lines; n > 0 {
		detail += bg(S.Amber).Render(fmt.Sprintf("  ⚠ %d", n))
	}
	bar := " "
	cmdStyle := S.Text
	if sel {
		bar = bg(S.Accent).Render("▌")
		cmdStyle = S.Bold
	}
	rail := bg(S.Faint).Render("│")
	num := bg(S.Muted).Render(fmt.Sprintf("%d", i+1))
	cmd := bg(cmdStyle).Render(trunc(oneLine(s.Cmd), w-6))
	return bar + sp(" ") + icon + sp(" ") + num + sp(" ") + cmd,
		bar + sp(" ") + rail + sp("   ") + detail
}

// stats strip ------------------------------------------------------------

func (m *model) viewStats(w int) string {
	in := m.pl.Links[m.sel].Stats()
	out := m.pl.Links[m.sel+1].Stats()
	chip := lipgloss.NewStyle().Foreground(cBadgeFg).Background(cAccent).Bold(true).Padding(0, 1)
	l := chip.Render(fmt.Sprintf("STAGE %d/%d", m.sel+1, len(m.pl.Stages))) + " "
	l += S.Sub.Render(humanCount(in.Lines)+" ln") + S.Muted.Render(" · "+humanBytes(in.Bytes))
	l += S.Accent.Render("  ━━▶  ")
	l += S.Text.Render(humanCount(out.Lines)+" ln") + S.Muted.Render(" · "+humanBytes(out.Bytes))
	if in.Lines > 0 && !in.Binary && !out.Binary {
		pct := float64(out.Lines-in.Lines) / float64(in.Lines) * 100
		switch {
		case out.Lines == in.Lines:
			l += S.Muted.Render("   = same lines")
		case pct < 0:
			l += S.Amber.Render(fmt.Sprintf("   ▼ %.0f%%", -pct))
		default:
			l += S.Cyan.Render(fmt.Sprintf("   ▲ %.0f%%", pct))
		}
	}
	chipFor := func(name string, on bool) string {
		if on {
			return S.ChipOn.Render(name)
		}
		return S.Chip.Render(name)
	}
	r := chipFor("diff", m.diff) + " " + chipFor("sync", m.sync) + " " + chipFor("hex", m.hexFlip)
	if ansi.StringWidth(l)+ansi.StringWidth(r)+2 > w {
		return fit(l, w)
	}
	return l + strings.Repeat(" ", w-ansi.StringWidth(l)-ansi.StringWidth(r)) + r
}

// panes ------------------------------------------------------------------

func paneTitle(name string, focused bool) string {
	if focused {
		return S.Accent.Bold(true).Render(name)
	}
	return S.Sub.Bold(true).Render(name)
}

func (m *model) setFor(c *Capture) map[string]struct{} {
	st := c.Stats()
	if !st.Done || st.Binary {
		return nil
	}
	if ls, ok := m.sets[c]; ok && ls.bytes == st.Bytes {
		return ls.set
	}
	set := c.LineSet()
	m.sets[c] = lineSet{st.Bytes, set}
	return set
}

func (m *model) viewPane(p, w, h int) string {
	c := m.paneCap(p)
	st := c.Stats()
	focused := m.focus == p
	n := len(m.pl.Stages)

	var title string
	switch p {
	case focusIn:
		from := "stdin"
		if m.sel > 0 {
			from = fmt.Sprintf("stage %d", m.sel)
		}
		title = paneTitle("INPUT", focused) + S.Muted.Render(" ← "+from)
	case focusOut:
		to := "stdout"
		if m.sel+1 < n {
			to = fmt.Sprintf("stage %d", m.sel+2)
		}
		title = paneTitle("OUTPUT", focused) + S.Muted.Render(" → "+to)
	case focusErr:
		title = S.Amber.Bold(true).Render("STDERR") + S.Muted.Render(fmt.Sprintf(" stage %d", m.sel+1))
	}

	info := S.Muted.Render(humanCount(st.Lines) + " " + plural(st.Lines, "line", "lines") + " · " + humanBytes(st.Bytes))
	if !st.Done {
		info = S.Cyan.Render([]string{"●", "○"}[m.frame/5%2]+" live ") + info
	}
	if st.Truncated {
		info += S.Amber.Render(" ✂ " + humanBytes(maxCapture))
	}

	iw := w - 2
	rows := h - 2
	sc := m.sc[p]
	var body []string
	total := 0

	if m.isHex(c) {
		bpr := bytesPerRow(iw)
		total = (st.StoredBytes + bpr - 1) / bpr
		for r := sc.y; r < total && len(body) < rows; r++ {
			body = append(body, " "+hexRow(c.Bytes(r*bpr, bpr), r*bpr, bpr))
		}
		info = S.Accent.Render("hex ") + info
	} else {
		total = st.StoredLines
		var other map[string]struct{}
		if m.diff && p != focusErr {
			if p == focusIn {
				other = m.setFor(m.pl.Links[m.sel+1])
			} else {
				other = m.setFor(m.pl.Links[m.sel])
			}
			if m.setFor(c) == nil || len(other) == 0 {
				other = nil
			}
		}
		gw := max(3, len(fmt.Sprint(total)))
		textW := iw - gw - 3
		for j, ln := range c.Lines(sc.y, rows) {
			idx := sc.y + j + 1
			mark := " "
			txt := S.Text
			if p == focusErr {
				txt = S.Amber
			}
			if other != nil {
				if _, ok := other[string(ln)]; !ok {
					if p == focusIn {
						mark, txt = S.Red.Render("▎"), S.Muted
					} else {
						mark, txt = S.Green.Render("▎"), S.Green
					}
				}
			}
			num := S.Faint.Render(fmt.Sprintf(" %*d", gw, idx))
			body = append(body, num+mark+" "+txt.Render(visible(ln, sc.x, textW)))
		}
		if sc.x > 0 {
			info = S.Muted.Render(fmt.Sprintf("col %d · ", sc.x+1)) + info
		}
	}

	if len(body) == 0 {
		msg := "∅  empty"
		switch {
		case !st.Done && p == focusOut && m.pl.Links[m.sel].Stats().Bytes > 0 &&
			m.pl.Stages[m.sel].Info().Duration() > time.Second:
			msg = spinner[m.frame%len(spinner)] + "  waiting · stage may be buffering its output"
		case !st.Done:
			msg = spinner[m.frame%len(spinner)] + "  waiting for data"
		case p == focusIn && m.sel == 0 && !m.pl.HasStdin:
			msg = "no stdin · pipe data into pipespy"
		}
		pad := max(0, rows/2-1)
		for i := 0; i < pad; i++ {
			body = append(body, "")
		}
		body = append(body, lipgloss.PlaceHorizontal(iw, lipgloss.Center, S.Muted.Render(msg)))
	}

	a, b := thumb(total, rows, sc.y)
	return box(title, info, body, w, h, focused, a, b)
}

// footer -----------------------------------------------------------------

func (m *model) viewFooter() string {
	if m.editing {
		return S.Accent.Bold(true).Render(" ❯ ") + m.input.View() +
			"  " + S.Muted.Render("⏎ run · esc cancel")
	}
	if m.flash != "" && time.Since(m.flashAt) < 4*time.Second {
		return " " + S.Cyan.Render(trunc(m.flash, m.w-2))
	}
	keys := [][2]string{
		{"↑↓", "stage/scroll"}, {"tab", "focus"}, {"[ ]", "stage"}, {"←→", "pan"},
		{"g/G", "top/end"}, {"i", "edit"}, {"r", "rerun"}, {"d", "diff"},
		{"s", "sync"}, {"x", "hex"}, {"e", "stderr"}, {"q", "quit"},
	}
	var b strings.Builder
	b.WriteString(" ")
	for i, k := range keys {
		item := S.Key.Render(k[0]) + " " + S.Muted.Render(k[1])
		if i > 0 {
			item = S.Faint.Render("  ") + item
		}
		if ansi.StringWidth(b.String())+ansi.StringWidth(item) > m.w {
			break
		}
		b.WriteString(item)
	}
	return b.String()
}
