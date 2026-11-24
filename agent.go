package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"
)

// Agent-facing, non-interactive commands. Every command prints plain text by
// default and a stable JSON document with --json. Output is always bounded
// so it is safe to drop straight into a model's context.

//go:embed docs/agent-guide.md
var agentGuide string

type cli struct {
	in       io.Reader // nil when stdin is a terminal
	out, err io.Writer
}

var subcommands = map[string]func(c cli, args []string) int{
	"run":     cmdRun,
	"inspect": cmdInspect,
	"show":    cmdInspect,
	"diff":    cmdDiff,
	"trace":   cmdTrace,
	"runs":    cmdRuns,
	"guide":   cmdGuide,
}

// fail reports an error as text, or as {"error": ...} in JSON mode.
func (c cli) fail(asJSON bool, code int, format string, a ...any) int {
	msg := fmt.Sprintf(format, a...)
	if asJSON {
		c.json(map[string]string{"error": msg})
	} else {
		fmt.Fprintln(c.err, "pipespy:", msg)
	}
	return code
}

func (c cli) json(v any) {
	enc := json.NewEncoder(c.out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func newFlags(name, usage string, c cli) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(c.err)
	fs.Usage = func() {
		fmt.Fprintf(c.err, "usage: %s\n\nflags:\n", usage)
		fs.PrintDefaults()
		fmt.Fprintln(c.err, "\nrun `pipespy guide` for the full agent guide")
	}
	return fs
}

// parseInterspersed allows flags before and after positional arguments.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

// ---------------------------------------------------------------- run

type SummaryStage struct {
	StageMeta
	Input      StreamMeta `json:"input"`
	Output     StreamMeta `json:"output"`
	OutputHead []string   `json:"output_head"`
	OutputTail []string   `json:"output_tail,omitempty"`
	StderrText string     `json:"stderr_text,omitempty"`
}

type Summary struct {
	ID         string         `json:"id"`
	Saved      bool           `json:"saved"`
	Pipeline   string         `json:"pipeline"`
	Shell      string         `json:"shell"`
	Status     string         `json:"status"`
	ExitCode   int            `json:"exit_code"`
	DurationMS float64        `json:"duration_ms"`
	Stdin      *StreamMeta    `json:"stdin"`
	Output     StreamMeta     `json:"output"`
	OutputHead []string       `json:"output_head"`
	Stages     []SummaryStage `json:"stages"`
	Hints      []string       `json:"hints"`
	Next       []string       `json:"next"`
}

func cmdRun(c cli, args []string) int {
	fs := newFlags("run", "pipespy run [flags] '<cmd1> | <cmd2> | ...'", c)
	asJSON := fs.Bool("json", false, "print the summary as JSON")
	shell := fs.String("shell", envOr("PIPESPY_SHELL", defaultShell()), "shell used to run each stage")
	timeout := fs.Duration("timeout", 60*time.Second, "kill the pipeline after this long (0 = no limit)")
	sample := fs.Int("sample", 5, "lines of each stage's output to include in the summary")
	noSave := fs.Bool("no-save", false, "don't save the run for later inspect/diff/trace")
	noStdin := fs.Bool("no-stdin", false, "don't read pipespy's stdin; stage 1 gets empty input")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return 2
	}
	pl, err := NewPipeline(joinArgs(fs.Args()), *shell)
	if err != nil {
		return c.fail(*asJSON, 2, "%v", err)
	}
	if *noStdin {
		c.in = nil
	}
	pl.Run(c.in)
	timedOut := waitPipeline(pl, *timeout)

	meta := buildMeta(pl, newRunID(pl.Start), timedOut)
	saved := false
	if !*noSave {
		if _, err := saveRun(pl, meta); err != nil {
			fmt.Fprintln(c.err, "pipespy: warning: could not save run:", err)
		} else {
			saved = true
		}
	}
	sum := buildSummary(pl, meta, *sample, saved, *timeout)
	if *asJSON {
		c.json(sum)
	} else {
		printSummary(c.out, sum)
	}
	return meta.ExitCode
}

// waitPipeline waits for every stage to finish; after timeout it kills them.
// It reports whether the timeout fired.
func waitPipeline(pl *Pipeline, timeout time.Duration) bool {
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	for !pl.Done() {
		if !deadline.IsZero() && time.Now().After(deadline) {
			pl.Kill()
			grace := time.Now().Add(5 * time.Second)
			for !pl.Done() && time.Now().Before(grace) {
				time.Sleep(5 * time.Millisecond)
			}
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

func capLines(c *Capture, from, n, width int) []string {
	out := []string{}
	for _, l := range c.RawLines(from, n) {
		out = append(out, agentLine(l, width))
	}
	return out
}

func buildSummary(pl *Pipeline, meta RunMeta, sample int, saved bool, timeout time.Duration) Summary {
	n := len(pl.Stages)
	s := Summary{
		ID: meta.ID, Saved: saved, Pipeline: meta.Pipeline, Shell: meta.Shell,
		Status: meta.Status, ExitCode: meta.ExitCode, DurationMS: meta.DurationMS,
		Output: meta.Pipes[n], OutputHead: capLines(pl.Links[n], 0, max(sample, 10), 300),
		Hints: []string{}, Next: []string{},
	}
	if meta.HasStdin {
		s.Stdin = &meta.Pipes[0]
	}
	for i, sm := range meta.Stages {
		st := SummaryStage{StageMeta: sm, Input: meta.Pipes[i], Output: meta.Pipes[i+1]}
		out := pl.Links[i+1]
		st.OutputHead = capLines(out, 0, sample, 300)
		if total := out.Stats().StoredLines; total > sample {
			st.OutputTail = capLines(out, max(sample, total-sample), sample, 300)
		}
		if e := pl.Stages[i].Stderr.All(); len(e) > 0 {
			st.StderrText = clip(sanitize(string(e)), 2000)
		}
		s.Stages = append(s.Stages, st)
	}
	s.Hints, s.Next = hints(meta, saved, timeout)
	s.Hints = append(s.Hints, dataHints(pl)...)
	return s
}

// dataHints looks at the recorded data itself for semantic mistakes that
// don't show up as errors.
func dataHints(pl *Pipeline) []string {
	var out []string
	for i, st := range pl.Stages {
		if firstWord(st.Cmd) != "uniq" {
			continue
		}
		split, example := splitRuns(pl.Links[i].RawLines(0, 1<<30))
		if split > 0 {
			out = append(out, fmt.Sprintf("stage %d (%s) got unsorted input: %d %s (e.g. %q) %s in several separate runs, "+
				"so uniq counts or keeps each run separately. uniq only merges adjacent duplicates; put sort before it",
				i+1, oneLine(st.Cmd), split, plural(int64(split), "value", "values"), clip(sanitize(example), 60),
				plural(int64(split), "appears", "appear")))
		}
	}
	return out
}

// splitRuns counts distinct lines that occur in more than one run of
// adjacent duplicates.
func splitRuns(lines [][]byte) (int, string) {
	seen := map[string]int{} // line -> number of runs
	example := ""
	split := 0
	for i, l := range lines {
		if i > 0 && bytes.Equal(l, lines[i-1]) {
			continue
		}
		k := string(l)
		seen[k]++
		if seen[k] == 2 {
			split++
			if example == "" {
				example = k
			}
		}
	}
	return split, example
}

func hints(m RunMeta, saved bool, timeout time.Duration) (hints, next []string) {
	ref := m.ID
	cmd := func(f string, a ...any) string {
		return "pipespy " + fmt.Sprintf(f, a...)
	}
	add := func(s string) { hints = append(hints, s) }
	n := len(m.Stages)

	if m.Status == "timeout" {
		var running []string
		for _, s := range m.Stages {
			if s.Status == "timeout" {
				running = append(running, strconv.Itoa(s.Stage))
			}
		}
		add(fmt.Sprintf("the pipeline was killed after --timeout %s; stage(s) %s were still running. A stage reading endless input (tail -f, yes) never finishes; raise --timeout or bound the input.",
			timeout, strings.Join(running, ", ")))
	}
	for _, s := range m.Stages {
		if s.Status != "failed" {
			continue
		}
		h := fmt.Sprintf("stage %d (%s) failed with exit code %d", s.Stage, s.Command, s.ExitCode)
		if s.Error != "" {
			h += " (" + s.Error + ")"
		}
		if s.Stderr.Bytes > 0 {
			h += "; read its stderr: " + cmd("inspect %s --stage %d --stream err", ref, s.Stage)
		}
		if s.ExitCode == 1 && s.Stderr.Bytes == 0 {
			if tool := firstWord(s.Command); noMatchTools[tool] {
				h += fmt.Sprintf("; for %s, exit 1 with no stderr means \"no match\" (or \"false\"), not a crash", tool)
			}
		}
		if s.ExitCode == 127 {
			h += "; 127 means the command was not found"
		}
		add(h)
	}
	for _, s := range m.Stages {
		in, out := m.Pipes[s.Stage-1], m.Pipes[s.Stage]
		if in.Lines > 0 && out.Bytes == 0 && s.Status != "timeout" {
			add(fmt.Sprintf("data disappears at stage %d (%s): %d input lines, no output. Look at what it received: %s",
				s.Stage, s.Command, in.Lines, cmd("inspect %s --stage %d --stream in --head 20", ref, s.Stage)))
			break
		}
	}
	if !m.HasStdin && m.Pipes[1].Bytes == 0 && n > 0 && m.Stages[0].Status == "ok" {
		add("stage 1 had no stdin and produced no output; if it expects input, pipe data into pipespy (cat file | pipespy run '...') or give it a file argument")
	}
	for _, s := range m.Stages {
		if s.Status == "sigpipe" {
			add(fmt.Sprintf("stage %d (%s) was stopped early because stage %d stopped reading (SIGPIPE); normal with head, grep -m, etc.",
				s.Stage, s.Command, s.Stage+1))
		}
	}
	for k, p := range m.Pipes {
		if !p.CRLF {
			continue
		}
		where := "the pipeline's stdin"
		if k > 0 {
			where = fmt.Sprintf("the output of stage %d (%s)", k, m.Stages[k-1].Command)
		}
		add(fmt.Sprintf("%s has Windows (CRLF) line endings: every line ends in \\r (shown as \\r by inspect). Patterns anchored with $, exact field matches and the last field of cut/awk include that \\r; strip it with tr -d '\\r' or sed 's/\\r$//'", where))
		break
	}
	for k, p := range m.Pipes {
		if p.Truncated {
			add(fmt.Sprintf("pipe %d exceeded the %s capture limit; only the first %s is stored", k, humanBytes(maxCapture), humanBytes(maxCapture)))
		}
	}
	for _, s := range m.Stages {
		if s.Status == "ok" && s.Stderr.Lines > 0 {
			add(fmt.Sprintf("stage %d (%s) succeeded but wrote %d line(s) to stderr: %s",
				s.Stage, s.Command, s.Stderr.Lines, cmd("inspect %s --stage %d --stream err", ref, s.Stage)))
		}
	}

	if !saved {
		return hints, []string{"(run was not saved; drop --no-save to inspect it afterwards)"}
	}
	next = []string{
		cmd("inspect %s --stage N            # output of stage N (add --stream in|err)", ref),
		cmd("inspect %s --final --grep RE    # search the final output", ref),
		cmd("diff %s --stage N               # what stage N removed and added", ref),
		cmd("trace %s --match RE             # follow matching lines through every stage", ref),
	}
	return hints, next
}

func printSummary(w io.Writer, s Summary) {
	fmt.Fprintf(w, "run %s · %s · exit %d · %d %s · %s\n", s.ID, s.Status, s.ExitCode,
		len(s.Stages), plural(int64(len(s.Stages)), "stage", "stages"), humanDur(time.Duration(s.DurationMS*float64(time.Millisecond))))
	if s.Status == "failed" && s.ExitCode == 0 {
		var failed []string
		for _, st := range s.Stages {
			if st.Status == "failed" {
				failed = append(failed, strconv.Itoa(st.Stage))
			}
		}
		fmt.Fprintf(w, "note:     exit 0 is the last stage's status (as in a shell), but stage %s failed\n", strings.Join(failed, ", "))
	}
	fmt.Fprintf(w, "pipeline: %s\n", oneLine(s.Pipeline))
	if s.Stdin != nil && s.Stdin.Bytes == 0 {
		fmt.Fprintln(w, "stdin:    empty")
	} else if s.Stdin != nil {
		fmt.Fprintf(w, "stdin:    %s\n", flowMeta(*s.Stdin))
	} else {
		fmt.Fprintln(w, "stdin:    none")
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  #\tstatus\texit\ttime\tin\tout\tstderr\tcommand")
	for _, st := range s.Stages {
		errCol := "-"
		if st.Stderr.Lines > 0 {
			errCol = fmt.Sprintf("%d ln", st.Stderr.Lines)
		}
		fmt.Fprintf(tw, "  %d\t%s\t%d\t%s\t%s\t%s\t%s\t%s\n", st.Stage, st.Status, st.ExitCode,
			humanDur(time.Duration(st.DurationMS*float64(time.Millisecond))),
			flowMeta(st.Input), flowMeta(st.Output), errCol, clip(oneLine(st.Command), 60))
	}
	tw.Flush()

	fmt.Fprintf(w, "\nfinal output (%s):\n", flowMeta(s.Output))
	if len(s.OutputHead) == 0 {
		fmt.Fprintln(w, "  (empty)")
	}
	for i, l := range s.OutputHead {
		fmt.Fprintf(w, "%6d\t%s\n", i+1, l)
	}
	if more := s.Output.Lines - int64(len(s.OutputHead)); more > 0 {
		fmt.Fprintf(w, "     …\t%d more %s\n", more, plural(more, "line", "lines"))
	}
	for _, st := range s.Stages {
		if st.Status == "failed" && st.StderrText != "" {
			fmt.Fprintf(w, "\nstderr of stage %d:\n%s\n", st.Stage, indent(clip(st.StderrText, 600), "  "))
		}
	}
	if len(s.Hints) > 0 {
		fmt.Fprintln(w, "\nhints:")
		for _, h := range s.Hints {
			fmt.Fprintln(w, "  - "+h)
		}
	}
	if len(s.Next) > 0 {
		fmt.Fprintln(w, "\nnext:")
		for _, n := range s.Next {
			fmt.Fprintln(w, "  "+n)
		}
	}
}

func flowMeta(m StreamMeta) string {
	s := humanCount(m.Lines) + " ln · " + humanBytes(m.Bytes)
	if m.Binary {
		s += " (binary)"
	}
	if m.CRLF {
		s += " (CRLF)"
	}
	if m.Truncated {
		s += " (truncated)"
	}
	return s
}

// ---------------------------------------------------------------- inspect

type InspectLine struct {
	N    int    `json:"n"`
	Text string `json:"text"`
}

type InspectResult struct {
	Run        string        `json:"run"`
	Stage      int           `json:"stage"`
	Stream     string        `json:"stream"`
	Command    string        `json:"command"`
	TotalLines int           `json:"total_lines"`
	TotalBytes int64         `json:"total_bytes"`
	Truncated  bool          `json:"truncated,omitempty"`
	Format     string        `json:"format"` // text | hex
	Grep       string        `json:"grep,omitempty"`
	Matches    *int          `json:"matches,omitempty"`
	Lines      []InspectLine `json:"lines"`
	MoreBefore int           `json:"more_before"`
	MoreAfter  int           `json:"more_after"`
}

// streamFlags registers the flags that select one stream of a run.
type streamFlags struct {
	stage        *string
	stream       *string
	final, stdin *bool
}

func addStreamFlags(fs *flag.FlagSet) streamFlags {
	return streamFlags{
		stage:  fs.String("stage", "", "stage number (1-based), or \"last\"; default: the last stage"),
		stream: fs.String("stream", "out", "which stream of the stage: out, in or err"),
		final:  fs.Bool("final", false, "the pipeline's final output (same as --stage last --stream out)"),
		stdin:  fs.Bool("stdin", false, "the pipeline's stdin (same as --stage 1 --stream in)"),
	}
}

func (f streamFlags) resolve(r *Run) (Stream, error) {
	n := len(r.Meta.Stages)
	s := Stream{Stage: n, Kind: "out"}
	switch strings.ToLower(*f.stream) {
	case "out", "stdout", "output":
	case "in", "stdin", "input":
		s.Kind = "in"
	case "err", "stderr":
		s.Kind = "err"
	default:
		return s, fmt.Errorf("--stream must be out, in or err (got %q)", *f.stream)
	}
	switch v := strings.ToLower(*f.stage); v {
	case "", "last", "final":
	default:
		i, err := strconv.Atoi(v)
		if err != nil || i < 1 || i > n {
			return s, fmt.Errorf("--stage must be between 1 and %d (got %q)", n, *f.stage)
		}
		s.Stage = i
	}
	if *f.final {
		s = Stream{Stage: n, Kind: "out"}
	}
	if *f.stdin {
		s = Stream{Stage: 1, Kind: "in"}
	}
	return s, nil
}

func cmdInspect(c cli, args []string) int {
	fs := newFlags("inspect", "pipespy inspect [RUN] [--stage N] [--stream out|in|err] [--head N | --tail N | --range A:B] [--grep RE]", c)
	sf := addStreamFlags(fs)
	asJSON := fs.Bool("json", false, "print JSON")
	head := fs.Int("head", 0, "show the first N lines (default 50 when nothing else is selected)")
	tail := fs.Int("tail", 0, "show the last N lines")
	rng := fs.String("range", "", "show lines A:B (1-based, inclusive; A: or :B also work)")
	grep := fs.String("grep", "", "only lines matching this regular expression (Go/RE2 syntax)")
	ignoreCase := fs.Bool("i", false, "make --grep case-insensitive")
	all := fs.Bool("all", false, "show every selected line (no default limit)")
	width := fs.Int("max-width", 400, "clip lines longer than this many characters (0 = never)")
	hexMode := fs.Bool("hex", false, "show a hex dump (automatic for binary data)")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) > 1 {
		return c.fail(*asJSON, 2, "inspect takes at most one run id, got %q", pos)
	}
	r, err := loadRun(first(pos))
	if err != nil {
		return c.fail(*asJSON, 1, "%v", err)
	}
	st, err := sf.resolve(r)
	if err != nil {
		return c.fail(*asJSON, 2, "%v", err)
	}
	data, err := r.read(st)
	if err != nil {
		return c.fail(*asJSON, 1, "reading run data: %v", err)
	}
	sm := r.streamMeta(st)
	res := InspectResult{
		Run: r.Meta.ID, Stage: st.Stage, Stream: st.Kind, Command: r.Meta.Stages[st.Stage-1].Command,
		TotalBytes: sm.Bytes, Truncated: sm.Truncated, Format: "text", Lines: []InspectLine{},
	}

	var lines [][]byte
	if *hexMode || (sm.Binary && *grep == "") {
		res.Format = "hex"
		for off := 0; off < len(data); off += 16 {
			lines = append(lines, []byte(plainHexRow(data[off:min(off+16, len(data))], off)))
		}
	} else {
		lines = splitLines(data)
	}
	res.TotalLines = len(lines)

	type numbered struct {
		n int
		b []byte
	}
	sel := make([]numbered, 0, len(lines))
	for i, l := range lines {
		sel = append(sel, numbered{i + 1, l})
	}
	if *grep != "" {
		pat := *grep
		if *ignoreCase {
			pat = "(?i)" + pat
		}
		re, err := regexp.Compile(pat)
		if err != nil {
			return c.fail(*asJSON, 2, "bad --grep pattern: %v", err)
		}
		res.Grep = *grep
		kept := sel[:0]
		for _, l := range sel {
			if re.Match(l.b) {
				kept = append(kept, l)
			}
		}
		sel = kept
		m := len(sel)
		res.Matches = &m
	}
	if *rng != "" {
		a, b, err := parseRange(*rng, res.TotalLines)
		if err != nil {
			return c.fail(*asJSON, 2, "%v", err)
		}
		kept := sel[:0]
		for _, l := range sel {
			if l.n >= a && l.n <= b {
				kept = append(kept, l)
			}
		}
		sel = kept
	}
	from, to := 0, len(sel)
	switch {
	case *tail > 0:
		from = max(0, len(sel)-*tail)
	case *head > 0:
		to = min(len(sel), *head)
	case !*all && *rng == "":
		to = min(len(sel), 50)
	}
	for _, l := range sel[from:to] {
		res.Lines = append(res.Lines, InspectLine{l.n, agentLine(l.b, *width)})
	}
	res.MoreBefore, res.MoreAfter = from, len(sel)-to

	if *asJSON {
		c.json(res)
		return 0
	}
	printInspect(c.out, res, r.Meta.ID)
	return 0
}

func printInspect(w io.Writer, r InspectResult, ref string) {
	what := map[string]string{"out": "output", "in": "input", "err": "stderr"}[r.Stream]
	fmt.Fprintf(w, "run %s · stage %d %s · %s\n", r.Run, r.Stage, what, clip(oneLine(r.Command), 80))
	unit := "lines"
	if r.Format == "hex" {
		unit = "hex rows (16 bytes each)"
	}
	desc := fmt.Sprintf("%d %s · %s", r.TotalLines, unit, humanBytes(r.TotalBytes))
	if r.Matches != nil {
		desc += fmt.Sprintf(" · %d %s match /%s/", *r.Matches, plural(int64(*r.Matches), "line", "lines"), r.Grep)
	}
	if r.Truncated {
		desc += " · truncated at capture limit"
	}
	if len(r.Lines) > 0 {
		desc += fmt.Sprintf(" · showing %d-%d", r.Lines[0].N, r.Lines[len(r.Lines)-1].N)
	}
	fmt.Fprintln(w, desc)
	if len(r.Lines) == 0 {
		fmt.Fprintln(w, "(no lines)")
	}
	for _, l := range r.Lines {
		fmt.Fprintf(w, "%6d\t%s\n", l.N, l.Text)
	}
	if r.MoreAfter > 0 {
		next := r.Lines[len(r.Lines)-1].N + 1
		fmt.Fprintf(w, "… %d more selected line(s); continue with --range %d: --head %d, or narrow with --grep / --tail\n",
			r.MoreAfter, next, max(len(r.Lines), 50))
	}
	if r.MoreBefore > 0 {
		fmt.Fprintf(w, "(%d earlier selected line(s) not shown)\n", r.MoreBefore)
	}
}

func parseRange(s string, total int) (int, int, error) {
	a, b, ok := strings.Cut(s, ":")
	if !ok {
		a, b, ok = strings.Cut(s, "-")
	}
	if !ok {
		return 0, 0, fmt.Errorf("--range must look like A:B, A: or :B (got %q)", s)
	}
	lo, hi := 1, total
	var err error
	if a != "" {
		if lo, err = strconv.Atoi(a); err != nil {
			return 0, 0, fmt.Errorf("bad --range start %q", a)
		}
	}
	if b != "" {
		if hi, err = strconv.Atoi(b); err != nil {
			return 0, 0, fmt.Errorf("bad --range end %q", b)
		}
	}
	return lo, hi, nil
}

// ---------------------------------------------------------------- diff

type DiffLine struct {
	N    int    `json:"n"` // line number in the stage's input (removed) or output (added)
	Text string `json:"text"`
}

type DiffResult struct {
	Run         string     `json:"run"`
	Stage       int        `json:"stage"`
	Command     string     `json:"command"`
	InputLines  int        `json:"input_lines"`
	OutputLines int        `json:"output_lines"`
	Kept        int        `json:"kept"`
	Removed     int        `json:"removed"`
	Added       int        `json:"added"`
	Reordered   bool       `json:"reordered"`
	RemovedHead []DiffLine `json:"removed_sample"`
	AddedHead   []DiffLine `json:"added_sample"`
}

func cmdDiff(c cli, args []string) int {
	fs := newFlags("diff", "pipespy diff [RUN] --stage N [--limit N]", c)
	stage := fs.String("stage", "", "stage number (1-based) or \"last\"")
	asJSON := fs.Bool("json", false, "print JSON")
	limit := fs.Int("limit", 10, "sample size for removed and added lines")
	width := fs.Int("max-width", 300, "clip lines longer than this many characters (0 = never)")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return 2
	}
	r, err := loadRun(first(pos))
	if err != nil {
		return c.fail(*asJSON, 1, "%v", err)
	}
	stream := "out"
	f := false
	st, err := streamFlags{stage: stage, stream: &stream, final: &f, stdin: &f}.resolve(r)
	if err != nil {
		return c.fail(*asJSON, 2, "%v", err)
	}
	inData, err1 := r.pipeData(st.Stage - 1)
	outData, err2 := r.pipeData(st.Stage)
	if err := errors.Join(err1, err2); err != nil {
		return c.fail(*asJSON, 1, "reading run data: %v", err)
	}
	in, out := splitLines(inData), splitLines(outData)
	res := DiffResult{
		Run: r.Meta.ID, Stage: st.Stage, Command: r.Meta.Stages[st.Stage-1].Command,
		InputLines: len(in), OutputLines: len(out), RemovedHead: []DiffLine{}, AddedHead: []DiffLine{},
	}
	// Multiset difference: a line kept once but duplicated in the input
	// counts one kept and one removed.
	budget := map[string]int{}
	for _, l := range out {
		budget[string(l)]++
	}
	var keptIn []string
	for i, l := range in {
		k := string(l)
		if budget[k] > 0 {
			budget[k]--
			res.Kept++
			keptIn = append(keptIn, k)
			continue
		}
		res.Removed++
		if len(res.RemovedHead) < *limit {
			res.RemovedHead = append(res.RemovedHead, DiffLine{i + 1, agentLine(l, *width)})
		}
	}
	avail := map[string]int{}
	for _, l := range in {
		avail[string(l)]++
	}
	var keptOut []string
	for i, l := range out {
		k := string(l)
		if avail[k] > 0 {
			avail[k]--
			keptOut = append(keptOut, k)
			continue
		}
		res.Added++
		if len(res.AddedHead) < *limit {
			res.AddedHead = append(res.AddedHead, DiffLine{i + 1, agentLine(l, *width)})
		}
	}
	res.Reordered = strings.Join(keptIn, "\n") != strings.Join(keptOut, "\n")

	if *asJSON {
		c.json(res)
		return 0
	}
	w := c.out
	fmt.Fprintf(w, "run %s · stage %d · %s\n", res.Run, res.Stage, clip(oneLine(res.Command), 80))
	fmt.Fprintf(w, "%d lines in → %d lines out: %d kept, %d removed, %d added", res.InputLines, res.OutputLines, res.Kept, res.Removed, res.Added)
	if res.Reordered {
		fmt.Fprint(w, ", order changed")
	}
	fmt.Fprintln(w, "\n(line-based, ignoring position: a changed line counts as one removed + one added)")
	if res.Kept == 0 && res.Removed > 0 && res.Added > 0 {
		fmt.Fprintf(w, "note: no line passed through unchanged, so this stage rewrites lines rather than filtering them; "+
			"compare inputs and outputs directly with: pipespy inspect %s --stage %d --stream in --head 20  and  pipespy inspect %s --stage %d --head 20\n",
			res.Run, res.Stage, res.Run, res.Stage)
	}
	section := func(title string, total int, ls []DiffLine, where string) {
		fmt.Fprintf(w, "\n%s (%d):\n", title, total)
		if total == 0 {
			fmt.Fprintln(w, "  none")
		}
		for _, l := range ls {
			fmt.Fprintf(w, "%6d\t%s\n", l.N, l.Text)
		}
		if total > len(ls) {
			fmt.Fprintf(w, "     …\t%d more; see them with: pipespy inspect %s --stage %d --stream %s\n", total-len(ls), res.Run, res.Stage, where)
		}
	}
	section("removed (line numbers in the stage's input)", res.Removed, res.RemovedHead, "in")
	section("added (line numbers in the stage's output)", res.Added, res.AddedHead, "out")
	return 0
}

// ---------------------------------------------------------------- trace

type TracePipe struct {
	Pipe      int    `json:"pipe"` // 0 = stdin; k = output of stage k
	Label     string `json:"label"`
	Lines     int    `json:"lines"`
	Matches   int    `json:"matches"`
	FirstLine int    `json:"first_match_line,omitempty"`
	First     string `json:"first_match,omitempty"`
	// NearMiss counts lines that only match once a trailing \r or
	// whitespace is removed (CRLF data vs a $-anchored pattern).
	NearMiss int `json:"near_miss,omitempty"`
}

type TraceResult struct {
	Run       string      `json:"run"`
	Pattern   string      `json:"pattern"`
	Pipes     []TracePipe `json:"pipes"`
	FirstDrop int         `json:"first_drop_stage,omitempty"` // first stage whose output has fewer matches than its input
	LostAt    int         `json:"lost_at_stage,omitempty"`    // first stage after which no matches remain
	Verdict   string      `json:"verdict"`
}

func cmdTrace(c cli, args []string) int {
	fs := newFlags("trace", "pipespy trace [RUN] --match RE", c)
	match := fs.String("match", "", "regular expression to follow through the pipeline (required)")
	ignoreCase := fs.Bool("i", false, "case-insensitive match")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return 2
	}
	if *match == "" {
		fs.Usage()
		return 2
	}
	pat := *match
	if *ignoreCase {
		pat = "(?i)" + pat
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return c.fail(*asJSON, 2, "bad --match pattern: %v", err)
	}
	r, err := loadRun(first(pos))
	if err != nil {
		return c.fail(*asJSON, 1, "%v", err)
	}
	res := TraceResult{Run: r.Meta.ID, Pattern: *match}
	for k := range r.Meta.Pipes {
		data, err := r.pipeData(k)
		if err != nil {
			return c.fail(*asJSON, 1, "reading run data: %v", err)
		}
		label := "stdin"
		if k > 0 {
			label = fmt.Sprintf("after stage %d: %s", k, clip(oneLine(r.Meta.Stages[k-1].Command), 50))
		}
		tp := TracePipe{Pipe: k, Label: label}
		for i, l := range splitLines(data) {
			tp.Lines++
			if re.Match(l) {
				tp.Matches++
				if tp.FirstLine == 0 {
					tp.FirstLine, tp.First = i+1, agentLine(l, 200)
				}
			} else if t := bytes.TrimRight(l, " \t\r"); len(t) < len(l) && re.Match(t) {
				tp.NearMiss++
			}
		}
		res.Pipes = append(res.Pipes, tp)
	}
	startK := 0
	if !r.Meta.HasStdin {
		startK = 1 // no stdin: stage 1 creates the data
	}
	for k := startK + 1; k < len(res.Pipes); k++ {
		if res.FirstDrop == 0 && res.Pipes[k].Matches < res.Pipes[k-1].Matches {
			res.FirstDrop = k
		}
		if res.LostAt == 0 && res.Pipes[k].Matches == 0 && res.Pipes[k-1].Matches > 0 {
			res.LostAt = k
		}
	}
	last := res.Pipes[len(res.Pipes)-1]
	total := 0
	for _, p := range res.Pipes {
		total += p.Matches
	}
	cmdOf := func(k int) string { return oneLine(r.Meta.Stages[k-1].Command) }
	switch {
	case total == 0:
		res.Verdict = "no line matches anywhere in the pipeline (including its input); check the pattern or the input data"
	case res.LostAt > 0:
		res.Verdict = fmt.Sprintf("matching lines are lost at stage %d (%s): %d in, 0 out. Inspect them with: pipespy inspect %s --stage %d --stream in --grep %s",
			res.LostAt, cmdOf(res.LostAt), res.Pipes[res.LostAt-1].Matches, r.Meta.ID, res.LostAt, shellQuote(*match))
	case res.FirstDrop > 0:
		res.Verdict = fmt.Sprintf("stage %d (%s) drops some matching lines (%d → %d); %d reach the final output",
			res.FirstDrop, cmdOf(res.FirstDrop), res.Pipes[res.FirstDrop-1].Matches, res.Pipes[res.FirstDrop].Matches, last.Matches)
	default:
		res.Verdict = fmt.Sprintf("every stage keeps the matching lines; %d reach the final output", last.Matches)
	}
	for _, p := range res.Pipes {
		if p.NearMiss > 0 {
			res.Verdict += fmt.Sprintf(". Note: %d %s in %s match only after removing a trailing \\r or whitespace — "+
				"the data likely has CRLF (Windows) line endings or trailing spaces, so $-anchored patterns and exact matches fail; "+
				"strip them with tr -d '\\r' (inspect shows them as \\r)",
				p.NearMiss, plural(int64(p.NearMiss), "line", "lines"), p.Label)
			break
		}
	}
	if *asJSON {
		c.json(res)
		return 0
	}
	w := c.out
	fmt.Fprintf(w, "run %s · trace /%s/\n\n", res.Run, res.Pattern)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  pipe\tlines\tmatches\tnear misses\tfirst match")
	for _, p := range res.Pipes {
		mark := ""
		if p.Pipe == res.LostAt && res.LostAt > 0 {
			mark = "  ← lost here"
		} else if p.Pipe == res.FirstDrop && res.FirstDrop > 0 {
			mark = "  ← drops some"
		}
		fm := "-"
		if p.FirstLine > 0 {
			fm = fmt.Sprintf("%d: %s", p.FirstLine, clip(p.First, 60))
		}
		nm := "-"
		if p.NearMiss > 0 {
			nm = strconv.Itoa(p.NearMiss)
		}
		fmt.Fprintf(tw, "  %s\t%d\t%d\t%s\t%s%s\n", p.Label, p.Lines, p.Matches, nm, fm, mark)
	}
	tw.Flush()
	fmt.Fprintln(w, "\n"+res.Verdict)
	return 0
}

// ---------------------------------------------------------------- runs, guide

func cmdRuns(c cli, args []string) int {
	fs := newFlags("runs", "pipespy runs [--json] [--limit N]", c)
	asJSON := fs.Bool("json", false, "print JSON")
	limit := fs.Int("limit", 20, "how many recent runs to list")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	runs, err := listRuns()
	if err != nil {
		return c.fail(*asJSON, 1, "%v", err)
	}
	if len(runs) > *limit {
		runs = runs[:*limit]
	}
	if *asJSON {
		if runs == nil {
			runs = []RunMeta{}
		}
		c.json(runs)
		return 0
	}
	if len(runs) == 0 {
		fmt.Fprintln(c.out, "no saved runs; start one with: pipespy run '<pipeline>'")
		return 0
	}
	tw := tabwriter.NewWriter(c.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "id\tage\tstatus\texit\tstages\tpipeline")
	for _, m := range runs {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%s\n", m.ID, ago(m.StartedAt), m.Status, m.ExitCode, len(m.Stages), clip(oneLine(m.Pipeline), 70))
	}
	tw.Flush()
	return 0
}

func cmdGuide(c cli, _ []string) int {
	fmt.Fprint(c.out, agentGuide)
	return 0
}

// ---------------------------------------------------------------- helpers

// Tools whose exit status 1 means "no match"/"false" rather than an error.
var noMatchTools = map[string]bool{
	"grep": true, "egrep": true, "fgrep": true, "zgrep": true, "rg": true, "ag": true,
	"findstr": true, "diff": true, "cmp": true, "test": true, "[": true,
}

func firstWord(cmd string) string {
	f := strings.Fields(cmd)
	for len(f) > 0 && strings.Contains(f[0], "=") { // skip VAR=value prefixes
		f = f[1:]
	}
	if len(f) == 0 {
		return ""
	}
	w := strings.ToLower(f[0])
	if i := strings.LastIndexAny(w, `/\`); i >= 0 {
		w = w[i+1:]
	}
	return strings.TrimSuffix(w, ".exe")
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

// sanitize makes text safe to show to a model or a terminal: escape
// sequences are removed and other control characters are made visible.
func sanitize(s string) string {
	if strings.IndexByte(s, 0x1b) >= 0 {
		s = ansiRe.ReplaceAllString(s, "")
	}
	var b strings.Builder
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02x`, s[0])
		case r == '\t' || r == '\n':
			b.WriteRune(r)
		case r == '\r':
			b.WriteString(`\r`)
		case r < 32 || r == 127:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			b.WriteString(s[:size])
		}
		s = s[size:]
	}
	return b.String()
}

func agentLine(raw []byte, width int) string {
	s := sanitize(string(raw))
	if width > 0 {
		s = clip(s, width)
	}
	return s
}

// clip shortens s to n characters, saying how much was cut.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + fmt.Sprintf("… [+%d chars]", len(r)-n)
}

func indent(s, pre string) string {
	return pre + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n"+pre)
}

func plainHexRow(b []byte, off int) string {
	var hx, as strings.Builder
	for i := 0; i < 16; i++ {
		if i == 8 {
			hx.WriteByte(' ')
		}
		if i >= len(b) {
			hx.WriteString("   ")
			continue
		}
		fmt.Fprintf(&hx, "%02x ", b[i])
		if b[i] >= 32 && b[i] < 127 {
			as.WriteByte(b[i])
		} else {
			as.WriteByte('.')
		}
	}
	return fmt.Sprintf("%08x  %s |%s|", off, hx.String(), as.String())
}

func shellQuote(s string) string {
	if isShellSafe(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}
