package metrics_test

import (
	"strings"
	"testing"

	"github.com/prettyleaf/gh-proxy/internal/metrics"
)

func TestRecordsOutcomesSeparately(t *testing.T) {
	m := metrics.New("test")

	r := m.Start("GET")
	r.Target("release", "cli/cli")
	r.Finish(200, 4096)

	r = m.Start("GET")
	r.Deny("authentication failed")
	r.Finish(404, 18)

	r = m.Start("OPTIONS")
	r.Target("blob", "cli/cli")
	r.Preflight()
	r.Finish(204, 0)

	s := m.Snapshot()
	if s.Total != 3 || s.Proxied != 1 || s.Denied != 1 || s.Preflight != 1 {
		t.Errorf("total=%d proxied=%d denied=%d preflight=%d, want 3/1/1/1",
			s.Total, s.Proxied, s.Denied, s.Preflight)
	}
	if s.Bytes != 4114 {
		t.Errorf("bytes = %d, want 4114", s.Bytes)
	}
	if s.InFlight != 0 {
		t.Errorf("in flight = %d after every request finished, want 0", s.InFlight)
	}
	if got := count(s.Reasons, "authentication failed"); got != 1 {
		t.Errorf("deny reason count = %d, want 1", got)
	}
	if got := count(s.TopRepos, "cli/cli"); got != 2 {
		t.Errorf("cli/cli = %d, want 2: a preflight still names a repository", got)
	}
	if got := count(s.Status, "2xx"); got != 2 {
		t.Errorf("2xx = %d, want 2", got)
	}
}

func TestInFlightTracksOpenRequests(t *testing.T) {
	m := metrics.New("test")
	a, b := m.Start("GET"), m.Start("GET")
	if got := m.Snapshot().InFlight; got != 2 {
		t.Fatalf("in flight = %d, want 2", got)
	}
	a.Finish(200, 1)
	b.Finish(200, 1)
	if got := m.Snapshot().InFlight; got != 0 {
		t.Fatalf("in flight = %d, want 0", got)
	}
}

func TestRecentIsNewestFirstAndBounded(t *testing.T) {
	m := metrics.New("test")
	for i := 0; i < 40; i++ {
		r := m.Start("GET")
		r.Target("raw", "o/r")
		r.Finish(200+i%2, int64(i))
	}
	s := m.Snapshot()
	if len(s.Recent) != 25 {
		t.Fatalf("recent = %d entries, want the ring size 25", len(s.Recent))
	}
	if s.Recent[0].Bytes != 39 {
		t.Errorf("newest entry has %d bytes, want 39 (newest first)", s.Recent[0].Bytes)
	}
	if s.Recent[24].Bytes != 15 {
		t.Errorf("oldest kept entry has %d bytes, want 15", s.Recent[24].Bytes)
	}
}

func TestTopReposIsCappedToTen(t *testing.T) {
	m := metrics.New("test")
	for i := 0; i < 30; i++ {
		for j := 0; j <= i; j++ { // repo i is requested i+1 times
			r := m.Start("GET")
			r.Target("raw", string(rune('a'+i))+"/repo")
			r.Finish(200, 0)
		}
	}
	s := m.Snapshot()
	if len(s.TopRepos) != 10 {
		t.Fatalf("top repos = %d, want 10", len(s.TopRepos))
	}
	if s.TopRepos[0].Count < s.TopRepos[9].Count {
		t.Errorf("top repos are not sorted by count: %v", s.TopRepos)
	}
}

func TestHistoryIsAnHourEndingNow(t *testing.T) {
	m := metrics.New("test")
	r := m.Start("GET")
	r.Finish(200, 512)

	s := m.Snapshot()
	if len(s.History) != 60 {
		t.Fatalf("history = %d buckets, want 60", len(s.History))
	}
	last := s.History[59]
	if last.Requests != 1 || last.Bytes != 512 {
		t.Errorf("current minute = %d requests / %d bytes, want 1/512", last.Requests, last.Bytes)
	}
	for i, b := range s.History {
		if want := last.Minute - int64(59-i); b.Minute != want {
			t.Fatalf("bucket %d is minute %d, want %d (contiguous, oldest first)", i, b.Minute, want)
		}
	}
	if s.History[0].Requests != 0 {
		t.Errorf("an hour ago = %d requests, want 0", s.History[0].Requests)
	}
}

func TestNilRecorderIsInert(t *testing.T) {
	var m *metrics.Metrics
	r := m.Start("GET") // a Server built without metrics takes this path
	r.Deny("nothing")
	r.Target("raw", "o/r")
	r.Preflight()
	r.Finish(404, 0)
	if s := m.Snapshot(); s.Total != 0 {
		t.Errorf("nil collector counted %d requests", s.Total)
	}
}

func TestSnapshotIsIndependentOfLaterRecording(t *testing.T) {
	m := metrics.New("v1")
	r := m.Start("GET")
	r.Target("git", "o/r")
	r.Finish(200, 10)

	s := m.Snapshot()
	r = m.Start("GET")
	r.Target("git", "o/r")
	r.Finish(500, 20)

	if s.Total != 1 || s.Bytes != 10 {
		t.Errorf("snapshot changed under a later request: total=%d bytes=%d", s.Total, s.Bytes)
	}
	if !strings.EqualFold(s.Version, "v1") {
		t.Errorf("version = %q, want v1", s.Version)
	}
}

func count(pairs []metrics.Pair, name string) uint64 {
	for _, p := range pairs {
		if p.Name == name {
			return p.Count
		}
	}
	return 0
}
