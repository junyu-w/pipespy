package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// keepRuns is how many saved runs are kept before the oldest are pruned.
const keepRuns = 50

type StreamMeta struct {
	Bytes     int64 `json:"bytes"`
	Lines     int64 `json:"lines"`
	Truncated bool  `json:"truncated,omitempty"`
	Binary    bool  `json:"binary,omitempty"`
	CutShort  bool  `json:"cut_short,omitempty"`
}

func streamMeta(c *Capture) StreamMeta {
	s := c.Stats()
	return StreamMeta{s.Bytes, s.Lines, s.Truncated, s.Binary, s.CutShort}
}

type StageMeta struct {
	Stage      int        `json:"stage"`
	Command    string     `json:"command"`
	Status     string     `json:"status"` // ok | failed | sigpipe | killed | timeout
	ExitCode   int        `json:"exit_code"`
	Error      string     `json:"error,omitempty"`
	DurationMS float64    `json:"duration_ms"`
	Stderr     StreamMeta `json:"stderr"`
}

// RunMeta describes one saved run. Pipes[0] is the pipeline's stdin,
// Pipes[i] is the output of stage i, and Pipes[len-1] the final output.
type RunMeta struct {
	Schema     int          `json:"schema"`
	ID         string       `json:"id"`
	Pipeline   string       `json:"pipeline"`
	Shell      string       `json:"shell"`
	Dir        string       `json:"dir"`
	StartedAt  time.Time    `json:"started_at"`
	DurationMS float64      `json:"duration_ms"`
	Status     string       `json:"status"` // ok | failed | timeout
	ExitCode   int          `json:"exit_code"`
	HasStdin   bool         `json:"has_stdin"`
	Pipes      []StreamMeta `json:"pipes"`
	Stages     []StageMeta  `json:"stages"`
}

type Run struct {
	Meta RunMeta
	path string
}

func storeRoot() (string, error) {
	if d := os.Getenv("PIPESPY_HOME"); d != "" {
		return d, nil
	}
	c, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(c, "pipespy"), nil
}

func runsDir() (string, error) {
	root, err := storeRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "runs"), nil
}

func newRunID(t time.Time) string {
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	return t.Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

func stageStatus(i StageInfo, timedOut bool) string {
	switch i.State {
	case Exited:
		return "ok"
	case PipeClosed:
		return "sigpipe"
	case Killed:
		if timedOut {
			return "timeout"
		}
		return "killed"
	case Failed:
		return "failed"
	}
	return "running"
}

func buildMeta(pl *Pipeline, id string, timedOut bool) RunMeta {
	wd, _ := os.Getwd()
	m := RunMeta{
		Schema: 1, ID: id, Pipeline: pl.Cmdline, Shell: pl.Shell, Dir: wd,
		StartedAt: pl.Start, HasStdin: pl.HasStdin, Status: "ok",
	}
	end := pl.End()
	if end.IsZero() || end.Before(pl.Start) {
		end = time.Now()
	}
	m.DurationMS = ms(end.Sub(pl.Start))
	for _, l := range pl.Links {
		m.Pipes = append(m.Pipes, streamMeta(l))
	}
	for i, s := range pl.Stages {
		info := s.Info()
		sm := StageMeta{
			Stage: i + 1, Command: s.Cmd, Status: stageStatus(info, timedOut),
			ExitCode: info.Code, Error: info.Err, DurationMS: ms(info.Duration()),
			Stderr: streamMeta(s.Stderr),
		}
		if sm.Status == "failed" && m.Status == "ok" {
			m.Status = "failed"
		}
		m.Stages = append(m.Stages, sm)
	}
	m.ExitCode = pl.ExitCode()
	if timedOut {
		m.Status, m.ExitCode = "timeout", 124
	}
	return m
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func saveRun(pl *Pipeline, meta RunMeta) (*Run, error) {
	dir, err := runsDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, meta.ID)
	if err := os.MkdirAll(path, 0o755); err != nil {
		return nil, err
	}
	for k, l := range pl.Links {
		if err := os.WriteFile(filepath.Join(path, fmt.Sprintf("pipe-%d.data", k)), l.All(), 0o644); err != nil {
			return nil, err
		}
	}
	for i, s := range pl.Stages {
		if err := os.WriteFile(filepath.Join(path, fmt.Sprintf("stage-%d.stderr", i+1)), s.Stderr.All(), 0o644); err != nil {
			return nil, err
		}
	}
	b, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(filepath.Join(path, "meta.json"), b, 0o644); err != nil {
		return nil, err
	}
	_ = os.WriteFile(filepath.Join(dir, "last"), []byte(meta.ID), 0o644)
	pruneRuns(dir, keepRuns)
	return &Run{Meta: meta, path: path}, nil
}

func runIDs(dir string) []string {
	entries, _ := os.ReadDir(dir)
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids)
	return ids
}

// readMetas loads every saved run's metadata, oldest first. Ids only have
// one-second resolution, so order comes from the recorded start time.
func readMetas(dir string) []RunMeta {
	var out []RunMeta
	for _, id := range runIDs(dir) {
		b, err := os.ReadFile(filepath.Join(dir, id, "meta.json"))
		if err != nil {
			continue
		}
		var m RunMeta
		if json.Unmarshal(b, &m) == nil {
			m.ID = id
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

func pruneRuns(dir string, keep int) {
	metas := readMetas(dir)
	for len(metas) > keep {
		_ = os.RemoveAll(filepath.Join(dir, metas[0].ID))
		metas = metas[1:]
	}
}

// loadRun resolves "", "last", a full run id, or a unique id prefix/suffix.
func loadRun(ref string) (*Run, error) {
	dir, err := runsDir()
	if err != nil {
		return nil, err
	}
	if ref == "" || ref == "last" {
		b, err := os.ReadFile(filepath.Join(dir, "last"))
		if err != nil {
			return nil, errors.New("no saved runs yet; start one with: pipespy run '<pipeline>'")
		}
		ref = strings.TrimSpace(string(b))
	}
	var matches []string
	for _, id := range runIDs(dir) {
		if id == ref {
			matches = []string{id}
			break
		}
		if strings.HasPrefix(id, ref) || strings.HasSuffix(id, ref) {
			matches = append(matches, id)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no run matches %q; list runs with: pipespy runs", ref)
	case 1:
	default:
		return nil, fmt.Errorf("%q matches %d runs (%s); use a longer id", ref, len(matches), strings.Join(matches, ", "))
	}
	path := filepath.Join(dir, matches[0])
	b, err := os.ReadFile(filepath.Join(path, "meta.json"))
	if err != nil {
		return nil, fmt.Errorf("run %s is incomplete: %v", matches[0], err)
	}
	r := &Run{path: path}
	if err := json.Unmarshal(b, &r.Meta); err != nil {
		return nil, fmt.Errorf("run %s: bad meta.json: %v", matches[0], err)
	}
	return r, nil
}

func listRuns() ([]RunMeta, error) {
	dir, err := runsDir()
	if err != nil {
		return nil, err
	}
	metas := readMetas(dir)
	for i, j := 0, len(metas)-1; i < j; i, j = i+1, j-1 {
		metas[i], metas[j] = metas[j], metas[i]
	}
	return metas, nil
}

// Stream identifies one recorded byte stream of a run.
type Stream struct {
	Stage int    // 1-based stage
	Kind  string // "in", "out" or "err"
}

func (s Stream) pipe() int {
	if s.Kind == "in" {
		return s.Stage - 1
	}
	return s.Stage
}

func (r *Run) streamMeta(s Stream) StreamMeta {
	if s.Kind == "err" {
		return r.Meta.Stages[s.Stage-1].Stderr
	}
	return r.Meta.Pipes[s.pipe()]
}

func (r *Run) read(s Stream) ([]byte, error) {
	name := fmt.Sprintf("pipe-%d.data", s.pipe())
	if s.Kind == "err" {
		name = fmt.Sprintf("stage-%d.stderr", s.Stage)
	}
	return os.ReadFile(filepath.Join(r.path, name))
}

func (r *Run) pipeData(k int) ([]byte, error) {
	return os.ReadFile(filepath.Join(r.path, fmt.Sprintf("pipe-%d.data", k)))
}

// splitLines splits data into lines without their terminators.
func splitLines(data []byte) [][]byte {
	if len(data) == 0 {
		return nil
	}
	lines := bytes.Split(data, []byte{'\n'})
	if len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	for i, l := range lines {
		lines[i] = bytes.TrimSuffix(l, []byte{'\r'})
	}
	return lines
}
