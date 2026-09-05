// Package metrics keeps the small set of counters the status page renders.
//
// Everything lives in memory and resets with the process: this is a status
// page for one operator, not a time-series database. Nothing recorded here
// identifies a client — no addresses, no headers, and never the proxy token,
// which would otherwise arrive as part of the request path.
package metrics

import (
	"sort"
	"sync"
	"time"
)

const (
	// recentCap is how many finished requests the page lists.
	recentCap = 25
	// reposCap bounds the "top repositories" table. Once it is full, only
	// repositories already in the table keep counting, so a caller walking
	// random names cannot grow this map without limit.
	reposCap = 1000
	// historyMinutes is the width of the per-minute chart.
	historyMinutes = 60
)

// Metrics is the collector. The zero value is not usable; call New.
//
// A single mutex guards everything. The lock is held for a few map operations
// per request, which is nothing next to the network round trip it accompanies,
// and it keeps every counter consistent with every other one in a snapshot.
type Metrics struct {
	version string
	start   time.Time

	mu        sync.Mutex
	inFlight  int64
	total     uint64
	proxied   uint64
	denied    uint64
	preflight uint64
	bytes     uint64
	durSum    time.Duration
	durMax    time.Duration
	status    map[string]uint64
	reasons   map[string]uint64
	kinds     map[string]uint64
	repos     map[string]uint64
	recent    [recentCap]Event
	recentN   int // total events ever recorded, for ring ordering
	history   [historyMinutes]bucket
}

// bucket is one minute of the rolling chart. minute is the wall-clock minute it
// belongs to, so a slot left untouched for an hour is recognized as stale
// rather than replayed as current traffic.
type bucket struct {
	minute   int64
	requests uint64
	bytes    uint64
}

// New returns a collector. version is reported verbatim on the status page.
func New(version string) *Metrics {
	return &Metrics{
		version: version,
		start:   time.Now(),
		status:  map[string]uint64{},
		reasons: map[string]uint64{},
		kinds:   map[string]uint64{},
		repos:   map[string]uint64{},
	}
}

// Recorder accumulates one request's outcome. Every method tolerates a nil
// receiver, so a Server built without metrics needs no branches on the request
// path.
type Recorder struct {
	m       *Metrics
	start   time.Time
	method  string
	kind    string
	repo    string
	reason  string
	options bool
}

// Start opens a recording. The matching Finish must always run, including on a
// panic: the in-flight gauge is incremented here.
func (m *Metrics) Start(method string) *Recorder {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	m.inFlight++
	m.mu.Unlock()
	return &Recorder{m: m, start: time.Now(), method: method}
}

// Deny marks the request as rejected before it reached GitHub, with the same
// reason string the debug log carries.
func (r *Recorder) Deny(reason string) {
	if r == nil {
		return
	}
	r.reason = reason
}

// Target records the upstream this request resolved to. repo is "owner/repo",
// or just the owner for a gist, and is never the caller-supplied path.
func (r *Recorder) Target(kind, repo string) {
	if r == nil {
		return
	}
	r.kind, r.repo = kind, repo
}

// Preflight marks the request as a CORS preflight, which is neither proxied
// nor denied and would otherwise inflate the proxied count.
func (r *Recorder) Preflight() {
	if r == nil {
		return
	}
	r.options = true
}

// Finish closes the recording with what the client actually received.
func (r *Recorder) Finish(status int, bytes int64) {
	if r == nil {
		return
	}
	if bytes < 0 {
		bytes = 0
	}
	d := time.Since(r.start)
	now := time.Now()

	m := r.m
	m.mu.Lock()
	defer m.mu.Unlock()

	m.inFlight--
	m.total++
	m.bytes += uint64(bytes)
	m.status[statusClass(status)]++

	switch {
	case r.reason != "":
		m.denied++
		m.reasons[r.reason]++
	case r.options:
		m.preflight++
	default:
		m.proxied++
		m.durSum += d
		if d > m.durMax {
			m.durMax = d
		}
	}

	if r.kind != "" {
		m.kinds[r.kind]++
	}
	if r.repo != "" {
		if _, seen := m.repos[r.repo]; seen || len(m.repos) < reposCap {
			m.repos[r.repo]++
		}
	}

	m.recent[m.recentN%recentCap] = Event{
		At:     now,
		Method: r.method,
		Kind:   r.kind,
		Repo:   r.repo,
		Status: status,
		Bytes:  uint64(bytes),
		Millis: float64(d.Microseconds()) / 1000,
		Reason: r.reason,
	}
	m.recentN++

	minute := now.Unix() / 60
	b := &m.history[minute%historyMinutes]
	if b.minute != minute {
		*b = bucket{minute: minute}
	}
	b.requests++
	b.bytes += uint64(bytes)
}

// Event is one finished request as shown in the "recent" list.
type Event struct {
	At     time.Time `json:"at"`
	Method string    `json:"method"`
	Kind   string    `json:"kind,omitempty"`
	Repo   string    `json:"repo,omitempty"`
	Status int       `json:"status"`
	Bytes  uint64    `json:"bytes"`
	Millis float64   `json:"millis"`
	Reason string    `json:"reason,omitempty"`
}

// Pair is a named counter, sorted before it leaves the collector so that both
// the page and the JSON have a stable order.
type Pair struct {
	Name  string `json:"name"`
	Count uint64 `json:"count"`
}

// Bucket is one minute of the chart, oldest first, gaps filled with zeros.
type Bucket struct {
	Minute   int64  `json:"minute"` // minutes since the epoch
	Requests uint64 `json:"requests"`
	Bytes    uint64 `json:"bytes"`
}

// Snapshot is a consistent copy of every counter, safe to marshal.
type Snapshot struct {
	Version   string    `json:"version"`
	StartedAt time.Time `json:"started_at"`
	Now       time.Time `json:"now"`
	Uptime    float64   `json:"uptime_seconds"`

	InFlight  int64  `json:"in_flight"`
	Total     uint64 `json:"requests_total"`
	Proxied   uint64 `json:"proxied"`
	Denied    uint64 `json:"denied"`
	Preflight uint64 `json:"preflight"`
	Bytes     uint64 `json:"bytes_sent"`

	AvgMillis float64 `json:"avg_millis"`
	MaxMillis float64 `json:"max_millis"`

	Status   []Pair   `json:"status"`
	Reasons  []Pair   `json:"deny_reasons"`
	Kinds    []Pair   `json:"kinds"`
	TopRepos []Pair   `json:"top_repos"`
	Recent   []Event  `json:"recent"`
	History  []Bucket `json:"history"`
}

// Snapshot copies the current state. Sorting happens after the lock is
// released; only the copying needs to be atomic with respect to recording.
func (m *Metrics) Snapshot() Snapshot {
	if m == nil {
		return Snapshot{}
	}
	now := time.Now()

	m.mu.Lock()
	s := Snapshot{
		Version:   m.version,
		StartedAt: m.start,
		Now:       now,
		Uptime:    now.Sub(m.start).Seconds(),
		InFlight:  m.inFlight,
		Total:     m.total,
		Proxied:   m.proxied,
		Denied:    m.denied,
		Preflight: m.preflight,
		Bytes:     m.bytes,
		MaxMillis: float64(m.durMax.Microseconds()) / 1000,
		Status:    pairs(m.status),
		Reasons:   pairs(m.reasons),
		Kinds:     pairs(m.kinds),
		TopRepos:  pairs(m.repos),
		Recent:    m.recentEvents(),
		History:   m.historyFrom(now),
	}
	if m.proxied > 0 {
		s.AvgMillis = float64((m.durSum / time.Duration(m.proxied)).Microseconds()) / 1000
	}
	m.mu.Unlock()

	sortByName(s.Status)
	sortByCount(s.Reasons)
	sortByCount(s.Kinds)
	sortByCount(s.TopRepos)
	if len(s.TopRepos) > 10 {
		s.TopRepos = s.TopRepos[:10]
	}
	return s
}

// recentEvents unrolls the ring newest first. Caller holds the lock.
func (m *Metrics) recentEvents() []Event {
	n := m.recentN
	if n > recentCap {
		n = recentCap
	}
	out := make([]Event, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, m.recent[(m.recentN-i)%recentCap])
	}
	return out
}

// historyFrom returns the last hour ending with the current minute. Slots whose
// minute does not match are reported as zero rather than as their stale
// contents. Caller holds the lock.
func (m *Metrics) historyFrom(now time.Time) []Bucket {
	cur := now.Unix() / 60
	out := make([]Bucket, 0, historyMinutes)
	for i := historyMinutes - 1; i >= 0; i-- {
		minute := cur - int64(i)
		b := m.history[((minute%historyMinutes)+historyMinutes)%historyMinutes]
		if b.minute != minute {
			b = bucket{minute: minute}
		}
		out = append(out, Bucket{Minute: minute, Requests: b.requests, Bytes: b.bytes})
	}
	return out
}

func pairs(m map[string]uint64) []Pair {
	out := make([]Pair, 0, len(m))
	for k, v := range m {
		out = append(out, Pair{Name: k, Count: v})
	}
	return out
}

func sortByName(p []Pair) {
	sort.Slice(p, func(i, j int) bool { return p[i].Name < p[j].Name })
}

// sortByCount orders by count, breaking ties by name so the page does not
// reshuffle equal rows between refreshes.
func sortByCount(p []Pair) {
	sort.Slice(p, func(i, j int) bool {
		if p[i].Count != p[j].Count {
			return p[i].Count > p[j].Count
		}
		return p[i].Name < p[j].Name
	})
}

func statusClass(status int) string {
	switch status / 100 {
	case 1:
		return "1xx"
	case 2:
		return "2xx"
	case 3:
		return "3xx"
	case 4:
		return "4xx"
	case 5:
		return "5xx"
	default:
		return "other"
	}
}
