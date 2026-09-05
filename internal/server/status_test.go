package server_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/prettyleaf/gh-proxy/internal/config"
)

const statusPath = "/ghp-status"

func statusEnabled(auth string) func(*config.Config) {
	return func(c *config.Config) {
		c.StatusPath = statusPath
		c.StatusAuth = auth
	}
}

func TestStatusPageIs404WhenNotConfigured(t *testing.T) {
	f := newFakeGitHub(t, okHandler("payload"))
	h := newHandler(t, f, nil)

	for _, target := range []string{statusPath, statusPath + "/json", statusPath + "/metrics"} {
		rec := do(h, http.MethodGet, target, nil, nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404 while the page is disabled", target, rec.Code)
		}
	}
}

func TestStatusPageRequiresTheTokenByDefault(t *testing.T) {
	f := newFakeGitHub(t, okHandler("payload"))
	h := newHandler(t, f, statusEnabled(config.StatusAuthToken))

	rec := do(h, http.MethodGet, statusPath, nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unauthenticated status page = %d, want 404", rec.Code)
	}
	if got := rec.Body.String(); got != "404 page not found\n" {
		t.Errorf("body = %q; a refused status page must look like every other refusal", got)
	}

	headers := http.Header{"X-Proxy-Token": {testToken}}
	rec = do(h, http.MethodGet, statusPath, nil, headers)
	if rec.Code != http.StatusOK {
		t.Fatalf("status page with a token header = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want HTML", ct)
	}
	if !strings.Contains(rec.Body.String(), "<title>gh-proxy status</title>") {
		t.Error("the page body is not the status page")
	}
}

// A browser cannot set a header on a plain navigation, so the query form is the
// only way to open the page by hand on a token-gated instance.
func TestStatusPageAcceptsTheTokenAsAQueryParameter(t *testing.T) {
	f := newFakeGitHub(t, okHandler("payload"))
	h := newHandler(t, f, statusEnabled(config.StatusAuthToken))

	if rec := do(h, http.MethodGet, statusPath+"?token="+testToken, nil, nil); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if rec := do(h, http.MethodGet, statusPath+"?token=wrong", nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d with a wrong query token, want 404", rec.Code)
	}
}

// GHP_STATUS_AUTH=none is the tinyauth arrangement: the reverse proxy decides.
func TestStatusPageWithoutProxyAuth(t *testing.T) {
	f := newFakeGitHub(t, okHandler("payload"))
	h := newHandler(t, f, statusEnabled(config.StatusAuthNone))

	if rec := do(h, http.MethodGet, statusPath, nil, nil); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 with external authentication", rec.Code)
	}
	// The proxy itself stays token-gated regardless.
	target := prefix + "https://github.com/cli/cli/releases/download/v1/f.zip"
	if rec := do(h, http.MethodGet, target, nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("proxy request without a token = %d, want 404", rec.Code)
	}
}

func TestStatusJSONReportsProxiedTrafficAndNotItself(t *testing.T) {
	f := newFakeGitHub(t, okHandler("release-bytes"))
	h := newHandler(t, f, statusEnabled(config.StatusAuthNone))

	if rec := do(h, http.MethodGet, base+"https://github.com/cli/cli/releases/download/v1/f.zip", nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("proxied request = %d, want 200", rec.Code)
	}
	if rec := do(h, http.MethodGet, base+"https://github.com/cli/cli/nope", nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unproxyable request = %d, want 404", rec.Code)
	}
	// Several page loads, which must not appear in the numbers they report.
	for i := 0; i < 3; i++ {
		do(h, http.MethodGet, statusPath, nil, nil)
	}

	rec := do(h, http.MethodGet, statusPath+"/json", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want JSON", ct)
	}

	var got struct {
		Total   uint64 `json:"requests_total"`
		Proxied uint64 `json:"proxied"`
		Denied  uint64 `json:"denied"`
		Bytes   uint64 `json:"bytes_sent"`
		Recent  []struct {
			Repo   string `json:"repo"`
			Status int    `json:"status"`
			Reason string `json:"reason"`
		} `json:"recent"`
		TopRepos []struct {
			Name  string `json:"name"`
			Count uint64 `json:"count"`
		} `json:"top_repos"`
		Config struct {
			Prefix string `json:"prefix"`
			Auth   string `json:"auth"`
		} `json:"config"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding the snapshot: %v", err)
	}
	if got.Total != 2 || got.Proxied != 1 || got.Denied != 1 {
		t.Errorf("total=%d proxied=%d denied=%d, want 2/1/1: status page requests are not traffic",
			got.Total, got.Proxied, got.Denied)
	}
	if got.Bytes < uint64(len("release-bytes")) {
		t.Errorf("bytes_sent = %d, want at least the proxied body", got.Bytes)
	}
	if len(got.Recent) != 2 {
		t.Fatalf("recent = %d entries, want 2", len(got.Recent))
	}
	if got.Recent[1].Repo != "cli/cli" || got.Recent[1].Status != 200 {
		t.Errorf("oldest recent entry = %+v, want the proxied cli/cli 200", got.Recent[1])
	}
	if len(got.TopRepos) == 0 || got.TopRepos[0].Name != "cli/cli" {
		t.Errorf("top repos = %+v, want cli/cli", got.TopRepos)
	}
	if got.Config.Prefix != prefix || got.Config.Auth != "token" {
		t.Errorf("config = %+v, want the mount prefix and token auth", got.Config)
	}
	if strings.Contains(rec.Body.String(), testToken) {
		t.Error("the snapshot contains the proxy token")
	}
}

func TestStatusMetricsAreScrapable(t *testing.T) {
	f := newFakeGitHub(t, okHandler("payload"))
	h := newHandler(t, f, statusEnabled(config.StatusAuthNone))
	do(h, http.MethodGet, base+"https://github.com/cli/cli/releases/download/v1/f.zip", nil, nil)

	rec := do(h, http.MethodGet, statusPath+"/metrics", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"# TYPE ghproxy_requests_total counter",
		"ghproxy_requests_total 1",
		`ghproxy_responses_total{class="2xx"} 1`,
		`ghproxy_targets_total{kind="release"} 1`,
		"ghproxy_request_duration_seconds_count 1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output is missing %q\n%s", want, body)
		}
	}
}

func TestStatusPathRejectsUnknownSubPathsAndMethods(t *testing.T) {
	f := newFakeGitHub(t, okHandler("payload"))
	h := newHandler(t, f, statusEnabled(config.StatusAuthNone))

	if rec := do(h, http.MethodGet, statusPath+"/secrets", nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown sub-path = %d, want 404", rec.Code)
	}
	if rec := do(h, http.MethodPost, statusPath, nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("POST = %d, want 404", rec.Code)
	}
	// A neighbouring path must not be swallowed by the prefix match.
	if rec := do(h, http.MethodGet, statusPath+"-other", nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if rec := do(h, http.MethodGet, statusPath+"/", nil, nil); rec.Code != http.StatusOK {
		t.Errorf("trailing slash = %d, want 200", rec.Code)
	}
}
