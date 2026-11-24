package main

import (
	"bytes"
	"encoding/json"
	"io"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// agent runs a subcommand in-process with an isolated run store.
func agent(t *testing.T, stdin io.Reader, args ...string) (string, string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	code := subcommands[args[0]](cli{in: stdin, out: &out, err: &errb}, args[1:])
	return out.String(), errb.String(), code
}

func isolate(t *testing.T) { t.Setenv("PIPESPY_HOME", t.TempDir()) }

func TestAgentRunJSON(t *testing.T) {
	isolate(t)
	sh := posixShell(t)
	out, _, code := agent(t, strings.NewReader("b\na\nb\nc\n"), "run", "--json", "--shell", sh,
		`sort | uniq -c | grep -v " c$" | sh -c 'cat; echo careful >&2'`)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	var s Summary
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		t.Fatalf("bad json: %v\n%s", err, out)
	}
	if !s.Saved || s.Status != "ok" || len(s.Stages) != 4 || s.Stdin == nil || s.Stdin.Lines != 4 {
		t.Fatalf("summary: %+v", s)
	}
	if s.Stages[2].Input.Lines != 3 || s.Stages[2].Output.Lines != 2 {
		t.Errorf("grep stage flow: %+v", s.Stages[2])
	}
	if s.Stages[3].StderrText != "careful\n" {
		t.Errorf("stderr text = %q", s.Stages[3].StderrText)
	}
	if !strings.Contains(strings.Join(s.Hints, "\n"), "wrote 1 line(s) to stderr") {
		t.Errorf("hints: %q", s.Hints)
	}
	if len(s.OutputHead) != 2 || !strings.HasSuffix(s.OutputHead[0], "1 a") {
		t.Errorf("output head: %q", s.OutputHead)
	}
}

func TestAgentHintsPointAtVanishingData(t *testing.T) {
	isolate(t)
	out, _, code := agent(t, strings.NewReader("x 1\ny 2\n"), "run", "--shell", posixShell(t),
		`awk '{print $2}' | grep x | wc -l`)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{
		"status  exit", "failed  1", // grep found nothing
		`for grep, exit 1 with no stderr means "no match"`,
		"data disappears at stage 2 (grep x): 2 input lines, no output",
		"--stage 2 --stream in",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q:\n%s", want, out)
		}
	}
}

func TestAgentInspect(t *testing.T) {
	isolate(t)
	sh := posixShell(t)
	agent(t, nil, "run", "--shell", sh, "seq 1 120 | sed 's/^/n/'")

	out, _, _ := agent(t, nil, "inspect")
	if !strings.Contains(out, "showing 1-50") || !strings.Contains(out, "70 more") || !strings.Contains(out, "--range 51: --head 50") {
		t.Errorf("default inspect not bounded:\n%s", out)
	}
	out, _, _ = agent(t, nil, "inspect", "--stage", "1", "--tail", "2")
	if !strings.Contains(out, "   119\t119\n   120\t120\n") || !strings.Contains(out, "118 earlier") {
		t.Errorf("tail:\n%s", out)
	}
	out, _, _ = agent(t, nil, "inspect", "--range", "10:12", "--json")
	var r InspectResult
	json.Unmarshal([]byte(out), &r)
	if len(r.Lines) != 3 || r.Lines[0].N != 10 || r.Lines[0].Text != "n10" || r.TotalLines != 120 {
		t.Errorf("range json: %+v", r)
	}
	out, _, _ = agent(t, nil, "inspect", "--grep", "^n11[0-9]$", "--head", "3")
	if !strings.Contains(out, "10 lines match") || !strings.Contains(out, "   110\tn110") {
		t.Errorf("grep:\n%s", out)
	}
	_, errOut, code := agent(t, nil, "inspect", "--stage", "9")
	if code == 0 || !strings.Contains(errOut, "between 1 and 2") {
		t.Errorf("bad stage accepted: %d %s", code, errOut)
	}
}

func TestAgentDiffAndTrace(t *testing.T) {
	isolate(t)
	sh := posixShell(t)
	agent(t, strings.NewReader("apple\nbanana\ncherry\napple\n"), "run", "--shell", sh,
		"sort | uniq | grep -v an | sed s/cherry/CHERRY/")
	out, _, _ := agent(t, nil, "diff", "--stage", "2", "--json")
	var d DiffResult
	json.Unmarshal([]byte(out), &d)
	if d.InputLines != 4 || d.OutputLines != 3 || d.Removed != 1 || d.Added != 0 || d.Kept != 3 {
		t.Errorf("uniq diff: %+v", d)
	}
	out, _, _ = agent(t, nil, "diff", "--stage", "4")
	if !strings.Contains(out, "1 removed, 1 added") || !strings.Contains(out, "CHERRY") {
		t.Errorf("sed diff:\n%s", out)
	}
	out, _, _ = agent(t, nil, "diff", "--stage", "1", "--json")
	json.Unmarshal([]byte(out), &d)
	if !d.Reordered || d.Removed != 0 {
		t.Errorf("sort should only reorder: %+v", d)
	}

	out, _, _ = agent(t, nil, "trace", "--match", "banana", "--json")
	var tr TraceResult
	json.Unmarshal([]byte(out), &tr)
	if tr.LostAt != 3 || len(tr.Pipes) != 5 || tr.Pipes[0].Matches != 1 {
		t.Errorf("trace: %+v", tr)
	}
	out, _, _ = agent(t, nil, "trace", "--match", "durian")
	if !strings.Contains(out, "no line matches anywhere") {
		t.Errorf("trace none:\n%s", out)
	}
}

func TestAgentTimeout(t *testing.T) {
	isolate(t)
	out, _, code := agent(t, nil, "run", "--json", "--timeout", "300ms", "--shell", posixShell(t), "sleep 10 | cat")
	var s Summary
	json.Unmarshal([]byte(out), &s)
	if code != 124 || s.Status != "timeout" || s.Stages[0].Status != "timeout" {
		t.Errorf("code=%d status=%s stages=%+v", code, s.Status, s.Stages)
	}
}

func TestAgentRunsAndRefs(t *testing.T) {
	isolate(t)
	sh := posixShell(t)
	out, _, _ := agent(t, nil, "runs")
	if !strings.Contains(out, "no saved runs") {
		t.Errorf("empty runs: %s", out)
	}
	_, errOut, code := agent(t, nil, "inspect")
	if code != 1 || !strings.Contains(errOut, "no saved runs") {
		t.Errorf("inspect without runs: %d %s", code, errOut)
	}
	o1, _, _ := agent(t, nil, "run", "--json", "--shell", sh, "echo first")
	agent(t, nil, "run", "--shell", sh, "echo second")
	var s Summary
	json.Unmarshal([]byte(o1), &s)
	out, _, _ = agent(t, nil, "inspect", s.ID[len(s.ID)-4:]) // short suffix id
	if !strings.Contains(out, "\tfirst") {
		t.Errorf("suffix ref:\n%s", out)
	}
	out, _, _ = agent(t, nil, "inspect")
	if !strings.Contains(out, "\tsecond") {
		t.Errorf("default should be the last run:\n%s", out)
	}
	out, _, _ = agent(t, nil, "runs", "--json")
	var list []RunMeta
	json.Unmarshal([]byte(out), &list)
	if len(list) != 2 || list[0].Pipeline != "echo second" {
		t.Errorf("runs: %+v", list)
	}
	out, _, code = agent(t, nil, "inspect", "nope", "--json")
	if code != 1 || !strings.Contains(out, `"error"`) {
		t.Errorf("json error: %d %s", code, out)
	}
}

func TestAgentSanitizesAndHexes(t *testing.T) {
	isolate(t)
	sh := posixShell(t)
	agent(t, nil, "run", "--shell", sh, `printf '\033[31mred\033[0m\tok\001\n'`)
	out, _, _ := agent(t, nil, "inspect")
	if !strings.Contains(out, "red\tok\\x01") || strings.Contains(out, "\x1b") {
		t.Errorf("sanitize: %q", out)
	}
	agent(t, nil, "run", "--shell", sh, `printf 'A\000B'`)
	out, _, _ = agent(t, nil, "inspect")
	if !strings.Contains(out, "41 00 42") || !strings.Contains(out, "|A.B|") {
		t.Errorf("hex:\n%s", out)
	}
}

func TestGuideIsEmbedded(t *testing.T) {
	out, _, _ := agent(t, nil, "guide")
	if !strings.Contains(out, "# pipespy for AI agents") || !strings.Contains(out, "pipespy trace") {
		t.Error("guide not embedded")
	}
}

func TestPruneKeepsNewestEvenWithinOneSecond(t *testing.T) {
	isolate(t)
	sh := posixShell(t)
	for i := 0; i < keepRuns+5; i++ {
		agent(t, nil, "run", "--shell", sh, "echo run-"+strconv.Itoa(i))
	}
	list, _ := listRuns()
	if len(list) != keepRuns || list[0].Pipeline != "echo run-54" || list[keepRuns-1].Pipeline != "echo run-5" {
		t.Errorf("kept %d runs, newest %q, oldest %q", len(list), list[0].Pipeline, list[len(list)-1].Pipeline)
	}
}

// CRLF data is the classic invisible pipeline bug: agents must be able to
// see the \r and be told about it.
func TestAgentMakesCRLFVisible(t *testing.T) {
	isolate(t)
	sh := posixShell(t)
	out, _, _ := agent(t, strings.NewReader("US,paid\r\nDE,pending\r\nFR,paid\r\n"), "run", "--shell", sh,
		`grep ',paid$' | cut -d, -f1`)
	wants := []string{"(CRLF)", "Windows (CRLF) line endings"}
	if runtime.GOOS != "windows" {
		// MSYS grep (Git for Windows) treats \r\n as the line end, so there
		// the pattern matches and stage 1 succeeds.
		wants = append(wants, "data disappears at stage 1", "note:     exit 0 is the last stage's status")
	}
	for _, want := range wants {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q:\n%s", want, out)
		}
	}
	out, _, _ = agent(t, nil, "inspect", "--stdin", "--head", "1")
	if !strings.Contains(out, "\tUS,paid\\r\n") {
		t.Errorf("inspect should show \\r:\n%s", out)
	}
	out, _, _ = agent(t, nil, "trace", "--match", "paid$", "--json")
	var tr TraceResult
	json.Unmarshal([]byte(out), &tr)
	if tr.Pipes[0].Matches != 0 || tr.Pipes[0].NearMiss != 2 || !strings.Contains(tr.Verdict, "CRLF") {
		t.Errorf("trace near miss: %+v", tr)
	}
}

func TestAgentHintsUnsortedUniq(t *testing.T) {
	isolate(t)
	sh := posixShell(t)
	out, _, _ := agent(t, strings.NewReader("a\nb\na\nb\nc\nc\n"), "run", "--shell", sh, "uniq -c | sort -rn")
	if !strings.Contains(out, "stage 1 (uniq -c) got unsorted input: 2 values") {
		t.Errorf("missing uniq hint:\n%s", out)
	}
	out, _, _ = agent(t, strings.NewReader("a\na\nb\nc\nc\n"), "run", "--shell", sh, "uniq -c")
	if strings.Contains(out, "unsorted input") {
		t.Errorf("false positive on grouped input:\n%s", out)
	}
	agent(t, strings.NewReader("a\nb\na\n"), "run", "--shell", sh, "uniq -c")
	out, _, _ = agent(t, nil, "diff", "--stage", "1")
	if !strings.Contains(out, "rewrites lines rather than filtering") {
		t.Errorf("missing transform note:\n%s", out)
	}
}
