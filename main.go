package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"golang.org/x/term"
)

// version is stamped at build time: -ldflags "-X main.version=1.2.3"
var version = "dev"

const usageText = `pipespy — inspect the data flowing through every pipe of a shell pipeline

Usage:
  pipespy [flags] '<cmd1> | <cmd2> | ...'
  <producer> | pipespy [flags] '<cmd1> | <cmd2> | ...'

Each stage runs in its own shell. pipespy sits on every pipe, records what
flows through it, and shows a live view of each stage's input, output,
stderr, exit status and timing.

If pipespy's stdout is redirected, the final output is written there when
you quit, so it can be dropped into the middle of a longer pipeline.

Examples:
  pipespy 'cat /etc/passwd | grep -v "^#" | cut -d: -f7 | sort | uniq -c | sort -rn'
  ps aux | pipespy 'awk "{print \$1}" | sort | uniq -c | sort -rn | head'
  pipespy 'seq 1 100000 | grep 7 | wc -l'

Flags:
`

func main() {
	shell := flag.String("shell", envOr("PIPESPY_SHELL", "/bin/sh"), "shell used to run each stage")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Usage = func() {
		fmt.Fprint(os.Stderr, usageText)
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVersion {
		fmt.Println("pipespy", version)
		return
	}
	args := flag.Args()
	if len(args) == 0 {
		flag.Usage()
		os.Exit(2)
	}
	cmdline := joinArgs(args)

	pl, err := NewPipeline(cmdline, *shell)
	if err != nil {
		fatal(err.Error())
	}

	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		fatal("pipespy needs an interactive terminal (could not open /dev/tty)")
	}
	defer tty.Close()

	// Bind styling to the terminal, not to stdout, which may be a pipe.
	r := lipgloss.NewRenderer(tty, termenv.WithColorCache(true))
	lipgloss.SetDefaultRenderer(r)
	initStyles()

	stdinPiped := !term.IsTerminal(int(os.Stdin.Fd()))
	stdoutPiped := !term.IsTerminal(int(os.Stdout.Fd()))

	var src io.Reader
	if stdinPiped {
		src = os.Stdin
	}
	pl.Run(src)

	m := newModel(pl, *shell, stdoutPiped)
	prog := tea.NewProgram(m,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
		tea.WithInput(tty),
		tea.WithOutput(tty),
	)
	final, err := prog.Run()
	fm, _ := final.(*model)
	if fm == nil {
		fm = m
	}
	fm.pl.Kill()
	if err != nil {
		fatal(err.Error())
	}

	if stdoutPiped {
		last := fm.pl.Links[len(fm.pl.Links)-1]
		os.Stdout.Write(last.All())
		if last.Stats().Truncated {
			fmt.Fprintf(os.Stderr, "pipespy: final output exceeded %s; output was truncated\n", humanBytes(maxCapture))
		}
	}
	os.Exit(fm.pl.ExitCode())
}

// joinArgs accepts either a single quoted pipeline or the pipeline split
// across arguments with literal '|' tokens: pipespy ls -l '|' wc -l
func joinArgs(args []string) string {
	if len(args) == 1 {
		return args[0]
	}
	parts := make([]string, len(args))
	for i, a := range args {
		if a == "|" || isShellSafe(a) {
			parts[i] = a
		} else {
			parts[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
	}
	return strings.Join(parts, " ")
}

func isShellSafe(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=:,+@%", r)) {
			return false
		}
	}
	return true
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "pipespy:", msg)
	os.Exit(1)
}
