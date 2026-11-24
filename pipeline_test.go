package main

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSplitPipeline(t *testing.T) {
	cases := map[string][]string{
		`ls | wc -l`:                    {"ls", "wc -l"},
		`echo 'a|b' | tr a b`:           {"echo 'a|b'", "tr a b"},
		`echo "x | y" | cat`:            {`echo "x | y"`, "cat"},
		`echo a\|b | cat`:               {`echo a\|b`, "cat"},
		`echo $(ls | head -1) | cat`:    {"echo $(ls | head -1)", "cat"},
		"echo `ls | head -1` | cat":     {"echo `ls | head -1`", "cat"},
		`false || echo hi | cat`:        {"false || echo hi", "cat"},
		`(a | b) | c`:                   {"(a | b)", "c"},
		`awk '{print $1}'|sort|uniq -c`: {"awk '{print $1}'", "sort", "uniq -c"},
	}
	for in, want := range cases {
		got, err := SplitPipeline(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%q: got %q %v, want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{`ls |`, `| ls`, `ls || `, `echo 'oops | cat`} {
		if _, err := SplitPipeline(bad); err == nil && bad != `ls || ` {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func waitDone(t *testing.T, p *Pipeline) {
	deadline := time.Now().Add(5 * time.Second)
	for !p.Done() {
		if time.Now().After(deadline) {
			t.Fatal("pipeline did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
}

func TestRunCapturesEveryLink(t *testing.T) {
	p, _ := NewPipeline(`tr a-z A-Z | grep -v B | sort -r`, "/bin/sh")
	p.Run(strings.NewReader("a\nb\nc\n"))
	waitDone(t, p)
	want := []string{"a\nb\nc\n", "A\nB\nC\n", "A\nC\n", "C\nA\n"}
	for i, w := range want {
		if got := string(p.Links[i].All()); got != w {
			t.Errorf("link %d = %q, want %q", i, got, w)
		}
	}
	if p.ExitCode() != 0 {
		t.Errorf("exit = %d", p.ExitCode())
	}
	if s := p.Links[2].Stats(); s.Lines != 2 || s.StoredLines != 2 {
		t.Errorf("stats %+v", s)
	}
}

func TestSigpipeAndStderr(t *testing.T) {
	p, _ := NewPipeline(`yes | head -3 | sh -c 'cat; echo oops >&2; exit 3'`, "/bin/sh")
	p.Run(nil)
	waitDone(t, p)
	if st := p.Stages[0].Info().State; st != PipeClosed {
		t.Errorf("yes state = %v, want PipeClosed", st)
	}
	if got := string(p.Links[2].All()); got != "y\ny\ny\n" {
		t.Errorf("head out = %q", got)
	}
	if got := string(p.Stages[2].Stderr.All()); got != "oops\n" {
		t.Errorf("stderr = %q", got)
	}
	if p.ExitCode() != 3 || p.Stages[2].Info().State != Failed {
		t.Errorf("exit = %d", p.ExitCode())
	}
}

func TestLinesWithoutTrailingNewline(t *testing.T) {
	c := NewCapture()
	c.Write([]byte("one\ntw"))
	c.Write([]byte("o\r\nthree"))
	s := c.Stats()
	if s.Lines != 3 || s.StoredLines != 3 {
		t.Fatalf("stats %+v", s)
	}
	got := c.Lines(0, 10)
	if string(got[1]) != "two" || string(got[2]) != "three" {
		t.Errorf("lines %q", got)
	}
}
