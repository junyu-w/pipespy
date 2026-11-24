package main

import (
	"bytes"
	"sync"
	"time"
)

// maxCapture bounds how many bytes of each stream are kept for display.
// Bytes past the limit are still counted (and forwarded) but not stored.
const maxCapture = 64 << 20

type event struct {
	t time.Time
	n int64
}

// Capture is a thread-safe recording of one stream in the pipeline:
// the bytes flowing through one pipe, or one stage's stderr.
type Capture struct {
	mu        sync.RWMutex
	data      []byte
	starts    []int // byte offset of the start of each stored line
	total     int64
	newlines  int64
	crlf      int64 // lines ending in \r\n
	lastByte  byte
	truncated bool
	cutShort  bool // reader downstream went away before EOF
	done      bool
	binary    bool
	sniffed   int
	events    []event
	last      time.Time
}

func NewCapture() *Capture { return &Capture{starts: []int{0}} }

func (c *Capture) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	n := int64(len(p))
	c.total += n
	c.newlines += int64(bytes.Count(p, []byte{'\n'}))
	c.crlf += int64(bytes.Count(p, []byte("\r\n")))
	c.lastByte = p[len(p)-1]
	c.last = now

	if k := len(c.events); k > 0 && now.Sub(c.events[k-1].t) < 2*time.Millisecond {
		c.events[k-1].n += n
	} else {
		c.events = append(c.events, event{now, n})
		if len(c.events) > 20000 { // compact: merge neighbours
			half := c.events[:0]
			for i := 0; i+1 < len(c.events); i += 2 {
				half = append(half, event{c.events[i].t, c.events[i].n + c.events[i+1].n})
			}
			c.events = half
		}
	}

	if c.sniffed < 8192 {
		s := p
		if len(s) > 8192-c.sniffed {
			s = s[:8192-c.sniffed]
		}
		if bytes.IndexByte(s, 0) >= 0 {
			c.binary = true
		}
		c.sniffed += len(s)
	}

	room := maxCapture - len(c.data)
	if room <= 0 {
		c.truncated = true
		return len(p), nil
	}
	q := p
	if len(q) > room {
		q = q[:room]
		c.truncated = true
	}
	base := len(c.data)
	c.data = append(c.data, q...)
	for off := 0; ; {
		i := bytes.IndexByte(q[off:], '\n')
		if i < 0 {
			break
		}
		off += i + 1
		c.starts = append(c.starts, base+off)
	}
	return len(p), nil
}

func (c *Capture) Close() {
	c.mu.Lock()
	c.done = true
	c.mu.Unlock()
}

func (c *Capture) markCutShort() {
	c.mu.Lock()
	c.cutShort = true
	c.mu.Unlock()
}

type CapStats struct {
	Bytes       int64
	Lines       int64
	StoredLines int
	StoredBytes int
	Done        bool
	Truncated   bool
	CutShort    bool
	Binary      bool
	CRLF        bool // most lines end in \r\n (Windows line endings)
	Last        time.Time
}

func (c *Capture) Stats() CapStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	lines := c.newlines
	if c.total > 0 && c.lastByte != '\n' {
		lines++
	}
	return CapStats{
		Bytes: c.total, Lines: lines, StoredLines: c.storedLines(), StoredBytes: len(c.data),
		Done: c.done, Truncated: c.truncated, CutShort: c.cutShort, Binary: c.binary, Last: c.last,
		CRLF: c.crlf > 0 && c.crlf*2 >= c.newlines,
	}
}

func (c *Capture) storedLines() int {
	n := len(c.starts)
	if c.starts[n-1] == len(c.data) {
		n--
	}
	return n
}

// Lines returns up to count stored lines starting at line index from.
// The returned slices alias the capture's buffer, which is append-only.
func (c *Capture) Lines(from, count int) [][]byte {
	c.mu.RLock()
	defer c.mu.RUnlock()
	n := c.storedLines()
	if from < 0 {
		from = 0
	}
	var out [][]byte
	for i := from; i < n && len(out) < count; i++ {
		out = append(out, c.line(i))
	}
	return out
}

func (c *Capture) line(i int) []byte {
	return bytes.TrimSuffix(c.rawLine(i), []byte{'\r'})
}

// rawLine keeps a trailing \r so CRLF data stays visible to agents.
func (c *Capture) rawLine(i int) []byte {
	end := len(c.data)
	if i+1 < len(c.starts) {
		end = c.starts[i+1] - 1
	}
	return c.data[c.starts[i]:end]
}

// RawLines is Lines without stripping a trailing \r.
func (c *Capture) RawLines(from, count int) [][]byte {
	c.mu.RLock()
	defer c.mu.RUnlock()
	n := c.storedLines()
	var out [][]byte
	for i := max(from, 0); i < n && len(out) < count; i++ {
		out = append(out, c.rawLine(i))
	}
	return out
}

func (c *Capture) Bytes(off, n int) []byte {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if off >= len(c.data) {
		return nil
	}
	end := off + n
	if end > len(c.data) {
		end = len(c.data)
	}
	return c.data[off:end]
}

func (c *Capture) All() []byte {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data
}

// LineSet returns the set of distinct stored lines (nil for huge captures).
func (c *Capture) LineSet() map[string]struct{} {
	c.mu.RLock()
	defer c.mu.RUnlock()
	n := c.storedLines()
	if n > 500000 {
		return nil
	}
	set := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		set[string(c.line(i))] = struct{}{}
	}
	return set
}

// Histogram buckets the bytes written between start and end.
func (c *Capture) Histogram(start, end time.Time, buckets int) []int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]int64, buckets)
	span := end.Sub(start)
	if span <= 0 {
		span = time.Millisecond
	}
	for _, e := range c.events {
		b := int(int64(e.t.Sub(start)) * int64(buckets) / int64(span))
		if b < 0 {
			b = 0
		}
		if b >= buckets {
			b = buckets - 1
		}
		out[b] += e.n
	}
	return out
}
