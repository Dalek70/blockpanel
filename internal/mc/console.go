// Package mc manages Minecraft server processes directly (no containers):
// process lifecycle, console ring buffer with live subscribers, resource
// stats, sandboxed file access, and zip backups.
package mc

import (
	"strings"
	"sync"
	"time"

	"blockpanel/internal/util"
)

const (
	consoleCapacity = 10_000 // lines kept in memory per server
	maxLineLen      = 4_000
)

type ConsoleLine struct {
	T    time.Time `json:"t"`
	Text string    `json:"text"`
}

// Console is a fixed-capacity ring buffer of log lines with pub/sub for live
// streaming (SSE). Subscribers with full channels drop lines rather than
// blocking the reader goroutine.
type Console struct {
	mu    sync.RWMutex
	buf   []ConsoleLine
	start int // index of oldest line
	count int
	subs  map[chan ConsoleLine]struct{}
}

func NewConsole() *Console {
	return &Console{
		buf:  make([]ConsoleLine, consoleCapacity),
		subs: map[chan ConsoleLine]struct{}{},
	}
}

func (c *Console) Append(text string) {
	text = util.StripANSI(text)
	text = strings.TrimRight(text, "\r\n")
	if len(text) > maxLineLen {
		text = text[:maxLineLen] + " …[truncated]"
	}
	line := ConsoleLine{T: time.Now(), Text: text}

	c.mu.Lock()
	idx := (c.start + c.count) % len(c.buf)
	if c.count == len(c.buf) {
		c.start = (c.start + 1) % len(c.buf)
	} else {
		c.count++
	}
	c.buf[idx] = line
	for ch := range c.subs {
		select {
		case ch <- line:
		default: // slow subscriber: drop
		}
	}
	c.mu.Unlock()
}

// Last returns up to n newest lines, oldest first.
func (c *Console) Last(n int) []ConsoleLine {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if n <= 0 || n > c.count {
		n = c.count
	}
	out := make([]ConsoleLine, n)
	for i := 0; i < n; i++ {
		out[i] = c.buf[(c.start+c.count-n+i)%len(c.buf)]
	}
	return out
}

// LastText returns up to n newest lines joined with newlines (for AI context).
func (c *Console) LastText(n int) string {
	lines := c.Last(n)
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.T.Format("15:04:05"))
		b.WriteByte(' ')
		b.WriteString(l.Text)
		b.WriteByte('\n')
	}
	return b.String()
}

// Search returns up to max newest lines containing needle (case-insensitive).
func (c *Console) Search(needle string, max int) []ConsoleLine {
	if max <= 0 || max > 200 {
		max = 50
	}
	needle = strings.ToLower(needle)
	all := c.Last(0)
	var out []ConsoleLine
	for i := len(all) - 1; i >= 0 && len(out) < max; i-- {
		if strings.Contains(strings.ToLower(all[i].Text), needle) {
			out = append(out, all[i])
		}
	}
	// oldest first
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func (c *Console) Subscribe() chan ConsoleLine {
	ch := make(chan ConsoleLine, 256)
	c.mu.Lock()
	c.subs[ch] = struct{}{}
	c.mu.Unlock()
	return ch
}

func (c *Console) Unsubscribe(ch chan ConsoleLine) {
	c.mu.Lock()
	delete(c.subs, ch)
	c.mu.Unlock()
}
