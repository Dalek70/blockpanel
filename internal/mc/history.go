package mc

import (
	"sync"
	"time"
)

// Sample is one point of resource history, used to draw the graphs.
type Sample struct {
	T       int64   `json:"t"` // unix seconds
	CPU     float64 `json:"cpu"`
	RSSMB   float64 `json:"rss"`
	Players int     `json:"players"`
}

const historyCap = 720 // at one sample per 5s this is the last hour

// History is a fixed-size ring of resource samples per server.
type History struct {
	mu    sync.RWMutex
	buf   []Sample
	start int
	count int
}

func NewHistory() *History {
	return &History{buf: make([]Sample, historyCap)}
}

func (h *History) Add(s Sample) {
	h.mu.Lock()
	defer h.mu.Unlock()
	idx := (h.start + h.count) % len(h.buf)
	if h.count == len(h.buf) {
		h.start = (h.start + 1) % len(h.buf)
	} else {
		h.count++
	}
	h.buf[idx] = s
}

// Recent returns up to n newest samples, oldest first.
func (h *History) Recent(n int) []Sample {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if n <= 0 || n > h.count {
		n = h.count
	}
	out := make([]Sample, n)
	for i := 0; i < n; i++ {
		out[i] = h.buf[(h.start+h.count-n+i)%len(h.buf)]
	}
	return out
}

func (h *History) Reset() {
	h.mu.Lock()
	h.start, h.count = 0, 0
	h.mu.Unlock()
}

// PlayerTracker keeps the current online-player list, updated both by server
// pings and by parsing join/leave lines out of the console.
type PlayerTracker struct {
	mu      sync.RWMutex
	online  map[string]time.Time // name -> joined at
	maxSeen int
}

func NewPlayerTracker() *PlayerTracker {
	return &PlayerTracker{online: map[string]time.Time{}}
}

func (p *PlayerTracker) Join(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.online[name]; !ok {
		p.online[name] = time.Now()
	}
	if len(p.online) > p.maxSeen {
		p.maxSeen = len(p.online)
	}
}

func (p *PlayerTracker) Leave(name string) {
	p.mu.Lock()
	delete(p.online, name)
	p.mu.Unlock()
}

func (p *PlayerTracker) Clear() {
	p.mu.Lock()
	p.online = map[string]time.Time{}
	p.mu.Unlock()
}

type OnlinePlayer struct {
	Name     string `json:"name"`
	SinceSec int64  `json:"since_sec"`
}

func (p *PlayerTracker) List() []OnlinePlayer {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]OnlinePlayer, 0, len(p.online))
	for name, t := range p.online {
		out = append(out, OnlinePlayer{Name: name, SinceSec: int64(time.Since(t).Seconds())})
	}
	return out
}

func (p *PlayerTracker) Count() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.online)
}
