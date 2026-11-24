package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type StageState int

const (
	Pending StageState = iota
	Running
	Exited     // exit 0
	Failed     // non-zero exit, start error, or fatal signal
	PipeClosed // SIGPIPE: the next stage stopped reading
	Killed     // terminated by pipespy
)

type Stage struct {
	Cmd    string
	Stderr *Capture

	mu     sync.Mutex
	state  StageState
	code   int
	errMsg string
	start  time.Time
	end    time.Time
	pid    int
}

type StageInfo struct {
	State      StageState
	Code       int
	Err        string
	Start, End time.Time
}

func (s *Stage) Info() StageInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return StageInfo{s.state, s.code, s.errMsg, s.start, s.end}
}

func (i StageInfo) Duration() time.Duration {
	if i.Start.IsZero() {
		return 0
	}
	if i.End.IsZero() {
		return time.Since(i.Start)
	}
	return i.End.Sub(i.Start)
}

func (i StageInfo) Finished() bool { return i.State != Pending && i.State != Running }

type Pipeline struct {
	Cmdline  string
	Shell    string
	Stages   []*Stage
	Links    []*Capture // Links[i] is the input of stage i; Links[i+1] is its output
	HasStdin bool
	Start    time.Time
	killed   atomic.Bool
}

func NewPipeline(cmdline, shell string) (*Pipeline, error) {
	parts, err := SplitPipeline(cmdline)
	if err != nil {
		return nil, err
	}
	p := &Pipeline{Cmdline: cmdline, Shell: shell}
	for _, c := range parts {
		p.Stages = append(p.Stages, &Stage{Cmd: c, Stderr: NewCapture()})
	}
	for i := 0; i <= len(parts); i++ {
		p.Links = append(p.Links, NewCapture())
	}
	return p, nil
}

// Run starts every stage. stdin may be nil, in which case the first stage
// reads from /dev/null.
func (p *Pipeline) Run(stdin io.Reader) {
	n := len(p.Stages)
	p.Start = time.Now()
	p.HasStdin = stdin != nil

	inR := make([]*os.File, n)
	inW := make([]*os.File, n)
	for i := 0; i < n; i++ {
		if i == 0 && stdin == nil {
			continue
		}
		r, w, err := os.Pipe()
		if err != nil {
			continue
		}
		inR[i], inW[i] = r, w
	}

	for i, st := range p.Stages {
		var next *os.File
		if i+1 < n {
			next = inW[i+1]
		}
		cmd := exec.Command(p.Shell, "-c", st.Cmd)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if inR[i] != nil {
			cmd.Stdin = inR[i]
		}
		cmd.Stderr = st.Stderr
		out, err := cmd.StdoutPipe()
		if err == nil {
			err = cmd.Start()
		}
		if inR[i] != nil {
			inR[i].Close() // the child holds its own copy
		}
		if err != nil {
			st.mu.Lock()
			st.state, st.code, st.errMsg = Failed, 127, err.Error()
			st.start, st.end = time.Now(), time.Now()
			st.mu.Unlock()
			st.Stderr.Write([]byte(err.Error() + "\n"))
			st.Stderr.Close()
			p.Links[i+1].Close()
			if next != nil {
				next.Close()
			}
			continue
		}
		st.mu.Lock()
		st.state, st.start, st.pid = Running, time.Now(), cmd.Process.Pid
		st.mu.Unlock()

		go func(st *Stage, cmd *exec.Cmd, out io.ReadCloser, link *Capture, next *os.File) {
			var w io.WriteCloser
			if next != nil {
				w = next
			}
			pump(out, link, w)
			out.Close() // if we stopped early, the stage now gets SIGPIPE
			err := cmd.Wait()
			st.finish(err, p.killed.Load())
			st.Stderr.Close()
		}(st, cmd, out, p.Links[i+1], next)
	}

	if stdin != nil {
		go pump(stdin, p.Links[0], inW[0])
	} else {
		p.Links[0].Close()
	}
}

// pump copies r into the capture and forwards it to next. If next stops
// accepting data (the downstream stage exited), pumping stops so the
// upstream writer receives SIGPIPE, exactly as in a real shell pipeline.
func pump(r io.Reader, c *Capture, next io.WriteCloser) {
	buf := make([]byte, 64*1024)
	for {
		k, err := r.Read(buf)
		if k > 0 {
			c.Write(buf[:k])
			if next != nil {
				if _, werr := next.Write(buf[:k]); werr != nil {
					next.Close()
					next = nil
					c.markCutShort()
					break
				}
			}
		}
		if err != nil {
			break
		}
	}
	if next != nil {
		next.Close()
	}
	c.Close()
}

func (s *Stage) finish(err error, killed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.end = time.Now()
	s.state, s.code = Exited, 0
	if err == nil {
		return
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		s.state, s.code, s.errMsg = Failed, 1, err.Error()
		return
	}
	ws, _ := ee.Sys().(syscall.WaitStatus)
	switch {
	case ws.Signaled() && ws.Signal() == syscall.SIGPIPE:
		s.state, s.code = PipeClosed, 141
	case ws.Signaled() && killed:
		s.state, s.code = Killed, 128+int(ws.Signal())
	case ws.Signaled():
		s.state, s.code, s.errMsg = Failed, 128+int(ws.Signal()), ws.Signal().String()
	case ws.ExitStatus() == 141:
		s.state, s.code = PipeClosed, 141
	default:
		s.state, s.code = Failed, ws.ExitStatus()
	}
}

func (p *Pipeline) Done() bool {
	for _, s := range p.Stages {
		if !s.Info().Finished() {
			return false
		}
	}
	return true
}

func (p *Pipeline) End() time.Time {
	var end time.Time
	for _, s := range p.Stages {
		if e := s.Info().End; e.After(end) {
			end = e
		}
	}
	return end
}

func (p *Pipeline) Kill() {
	p.killed.Store(true)
	for _, s := range p.Stages {
		s.mu.Lock()
		if s.state == Running && s.pid > 0 {
			syscall.Kill(-s.pid, syscall.SIGKILL)
		}
		s.mu.Unlock()
	}
}

// ExitCode mirrors shell semantics: the status of the last stage.
func (p *Pipeline) ExitCode() int {
	return p.Stages[len(p.Stages)-1].Info().Code
}

// SplitPipeline splits a shell command line on top-level `|`, respecting
// quotes, escapes, subshells, $(...) and backticks. `||` is left intact.
func SplitPipeline(s string) ([]string, error) {
	var parts []string
	var cur strings.Builder
	var sq, dq, bt bool
	depth := 0
	rs := []rune(s)
	push := func() error {
		part := strings.TrimSpace(cur.String())
		if part == "" {
			return errors.New("empty command in pipeline")
		}
		parts = append(parts, part)
		cur.Reset()
		return nil
	}
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == '\\' && !sq && i+1 < len(rs):
			cur.WriteRune(c)
			i++
			c = rs[i]
		case c == '\'' && !dq && !bt:
			sq = !sq
		case c == '"' && !sq:
			dq = !dq
		case c == '`' && !sq:
			bt = !bt
		case sq || dq || bt:
		case c == '(':
			depth++
		case c == ')':
			if depth > 0 {
				depth--
			}
		case c == '|' && depth == 0:
			if i+1 < len(rs) && rs[i+1] == '|' {
				cur.WriteString("||")
				i++
				continue
			}
			if i > 0 && rs[i-1] == '>' { // >| clobber redirect
				break
			}
			if err := push(); err != nil {
				return nil, err
			}
			if i+1 < len(rs) && rs[i+1] == '&' {
				return nil, errors.New("`|&` is not supported; use `2>&1 |`")
			}
			continue
		}
		cur.WriteRune(c)
	}
	if sq || dq || bt {
		return nil, fmt.Errorf("unterminated quote")
	}
	if err := push(); err != nil {
		return nil, err
	}
	return parts, nil
}
