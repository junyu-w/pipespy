package main

import (
	"bytes"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func runForTest(t *testing.T, cmdline, stdin string) *Pipeline {
	t.Helper()
	p, err := NewPipeline(cmdline, posixShell(t))
	if err != nil {
		t.Fatal(err)
	}
	var src *strings.Reader
	if stdin != "" {
		src = strings.NewReader(stdin)
		p.Run(src)
	} else {
		p.Run(nil)
	}
	waitDone(t, p)
	return p
}

func key(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

// checkFrame asserts the frame fills the terminal exactly, without overflow.
func checkFrame(t *testing.T, frame string, w, h int) {
	t.Helper()
	lines := strings.Split(frame, "\n")
	if len(lines) != h {
		t.Errorf("frame has %d lines, want %d", len(lines), h)
	}
	for i, l := range lines {
		if lw := ansi.StringWidth(l); lw > w {
			t.Errorf("line %d is %d cells wide (max %d): %q", i, lw, w, ansi.Strip(l))
		}
	}
}

func TestViewRendersAndNavigates(t *testing.T) {
	initStyles()
	p := runForTest(t, `sort | tr a-z A-Z | sh -c 'cat; echo note >&2'`, "pear\napple\nfig\n")
	m := newModel(p, posixShell(t), false)

	for _, size := range [][2]int{{140, 36}, {100, 28}, {64, 16}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m.Update(tickMsg{})
		checkFrame(t, m.View(), size[0], size[1])
	}

	m.Update(tea.WindowSizeMsg{Width: 140, Height: 36})
	plain := ansi.Strip(m.View())
	for _, want := range []string{"PIPELINE", "STAGE 1/3", "INPUT", "OUTPUT", "pear", "apple", "✓ done"} {
		if !strings.Contains(plain, want) {
			t.Errorf("first frame missing %q", want)
		}
	}

	m.Update(key("]"))
	m.Update(key("]"))
	plain = ansi.Strip(m.View())
	for _, want := range []string{"STAGE 3/3", "STDERR", "note", "PEAR"} {
		if !strings.Contains(plain, want) {
			t.Errorf("stage 3 frame missing %q", want)
		}
	}

	m.Update(key("x")) // hex view
	if plain = ansi.Strip(m.View()); !strings.Contains(plain, "00000000") {
		t.Error("hex view did not render offsets")
	}
}

func TestEditAndRerunReplaysStdin(t *testing.T) {
	initStyles()
	p := runForTest(t, "sort", "b\na\n")
	m := newModel(p, posixShell(t), false)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m.Update(key("i"))
	m.input.SetValue("sort -r | tr a-z A-Z")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	waitDone(t, m.pl)
	if got := string(m.pl.Links[2].All()); strings.ReplaceAll(got, "\r", "") != "B\nA\n" {
		t.Errorf("rerun output = %q", got)
	}
}

func TestReport(t *testing.T) {
	p := runForTest(t, "sort | uniq -c", "b\na\nb\n")
	var out, sum bytes.Buffer
	code := runReport(p, &out, &sum)
	if code != 0 || !strings.Contains(out.String(), "2 b") {
		t.Errorf("code=%d out=%q", code, out.String())
	}
	for _, want := range []string{"2 stages", "exit 0", "uniq -c", "3 ln/6 B"} {
		if !strings.Contains(sum.String(), want) {
			t.Errorf("summary missing %q:\n%s", want, sum.String())
		}
	}
}
