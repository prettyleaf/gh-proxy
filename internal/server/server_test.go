package server_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/prettyleaf/gh-proxy/internal/config"
	"github.com/prettyleaf/gh-proxy/internal/proxy"
	"github.com/prettyleaf/gh-proxy/internal/server"
)

const (
	testToken = "test-token-0123456789"
	prefix    = "/ivanghproxy/"
	base      = prefix + testToken + "/"
)

// recorded is what the fake GitHub saw.
type recorded struct {
	Method  string
	Host    string
	URI     string
	Headers http.Header
	Body    string
}

// fakeGitHub stands in for every GitHub host. Its transport dials this server
// regardless of the hostname requested, so the code under test resolves and
// connects exactly as it would in production while the traffic stays local.
type fakeGitHub struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []recorded
}

func newFakeGitHub(t *testing.T, h http.HandlerFunc) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body)) // put it back for the handler
		f.mu.Lock()
		f.reqs = append(f.reqs, recorded{
			Method:  r.Method,
			Host:    r.Host,
			URI:     r.RequestURI,
			Headers: r.Header.Clone(),
			Body:    string(body),
		})
		f.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) transport() http.RoundTripper {
	addr := strings.TrimPrefix(f.srv.URL, "https://")
	return &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
		TLSClientConfig:    &tls.Config{InsecureSkipVerify: true},
		DisableCompression: true,
	}
}

func (f *fakeGitHub) last(t *testing.T) recorded {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) == 0 {
		t.Fatal("upstream received no requests")
	}
	return f.reqs[len(f.reqs)-1]
}

func (f *fakeGitHub) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func defaultRedirectHosts() map[string]bool {
	m := map[string]bool{}
	for _, h := range config.DefaultRedirectHosts {
		m[h] = true
	}
	return m
}

func newHandler(t *testing.T, f *fakeGitHub, mutate func(*config.Config)) http.Handler {
	t.Helper()
	cfg := &config.Config{
		Prefix:        prefix,
		Token:         testToken,
		RedirectHosts: defaultRedirectHosts(),
		MaxRedirects:  5,
	}
	if mutate != nil {
		mutate(cfg)
	}
	p := proxy.New(proxy.Options{
		RedirectHosts: cfg.RedirectHosts,
		MaxRedirects:  cfg.MaxRedirects,
		SizeLimit:     cfg.SizeLimit,
		UpstreamToken: cfg.UpstreamToken,
		CORS:          cfg.CORS,
		BaseTransport: f.transport(),
		Logger:        discardLogger(),
	})
	return server.New(cfg, p, discardLogger())
}

// do runs one request through the handler.
func do(h http.Handler, method, target string, body io.Reader, headers http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, body)
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func okHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.WriteString(w, body)
	}
}

// --- stealth -----------------------------------------------------------------

func TestUnauthorizedRequestsAreIndistinguishable404s(t *testing.T) {
	f := newFakeGitHub(t, okHandler("payload"))
	h := newHandler(t, f, nil)

	targets := []struct {
		name   string
		target string
	}{
		{"site root", "/"},
		{"mount point itself", prefix},
		{"mount point without trailing slash", "/ivanghproxy"},
		{"wrong token", prefix + "wrong-token/https://github.com/cli/cli/releases/download/v1/f.zip"},
		{"no token", prefix + "https://github.com/cli/cli/releases/download/v1/f.zip"},
		{"different prefix", "/other/" + testToken + "/https://github.com/cli/cli/releases/download/v1/f.zip"},
		{"token but unsupported url", base + "https://gitlab.com/a/b/releases/download/v1/f.zip"},
		{"token but internal address", base + "http://127.0.0.1:8900/healthz"},
		{"token but github html page", base + "https://github.com/cli/cli"},
	}

	var bodies []string
	for _, tc := range targets {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(h, http.MethodGet, tc.target, nil, nil)
			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", rec.Code)
			}
			if v := rec.Header().Get("WWW-Authenticate"); v != "" {
				t.Errorf("WWW-Authenticate = %q; a stealth 404 must not challenge", v)
			}
			bodies = append(bodies, rec.Body.String())
		})
	}

	// Every rejection must look the same, or the differences become an oracle.
	for i := 1; i < len(bodies); i++ {
		if bodies[i] != bodies[0] {
			t.Errorf("rejection bodies differ: %q vs %q", bodies[0], bodies[i])
		}
	}
	if f.count() != 0 {
		t.Errorf("upstream saw %d requests; a rejected request must never reach GitHub", f.count())
	}
}

func TestUnsupportedMethodIs404(t *testing.T) {
	f := newFakeGitHub(t, okHandler("payload"))
	h := newHandler(t, f, nil)
	rec := do(h, http.MethodPut, base+"https://github.com/cli/cli/releases/download/v1/f.zip", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// --- happy path --------------------------------------------------------------

func TestProxiesReleaseAsset(t *testing.T) {
	f := newFakeGitHub(t, okHandler("release-bytes"))
	h := newHandler(t, f, nil)

	rec := do(h, http.MethodGet, base+"https://github.com/cli/cli/releases/download/v1/f.zip", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "release-bytes" {
		t.Errorf("body = %q, want %q", got, "release-bytes")
	}
	got := f.last(t)
	if got.Host != "github.com" {
		t.Errorf("upstream Host = %q, want github.com", got.Host)
	}
	if got.URI != "/cli/cli/releases/download/v1/f.zip" {
		t.Errorf("upstream URI = %q", got.URI)
	}
}

func TestBlobIsFetchedAsRaw(t *testing.T) {
	f := newFakeGitHub(t, okHandler("file"))
	h := newHandler(t, f, nil)

	do(h, http.MethodGet, base+"https://github.com/cli/cli/blob/trunk/README.md", nil, nil)
	if got := f.last(t).URI; got != "/cli/cli/raw/trunk/README.md" {
		t.Errorf("upstream URI = %q, want the /raw/ form", got)
	}
}

func TestPercentEncodingReachesUpstreamIntact(t *testing.T) {
	f := newFakeGitHub(t, okHandler("x"))
	h := newHandler(t, f, nil)

	const encoded = "/cli/cli/releases/download/v1/my%20file%2Bx%2Fy.zip"
	do(h, http.MethodGet, base+"https://github.com"+encoded, nil, nil)
	if got := f.last(t).URI; got != encoded {
		t.Errorf("upstream URI = %q, want %q", got, encoded)
	}
}

func TestCollapsedSchemeIsRepaired(t *testing.T) {
	// What a front end that normalizes "//" sends us.
	f := newFakeGitHub(t, okHandler("x"))
	h := newHandler(t, f, nil)

	rec := do(h, http.MethodGet, base+"https:/github.com/cli/cli/releases/download/v1/f.zip", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := f.last(t).Host; got != "github.com" {
		t.Errorf("upstream Host = %q, want github.com", got)
	}
}

func TestQueryStringIsForwarded(t *testing.T) {
	f := newFakeGitHub(t, okHandler("x"))
	h := newHandler(t, f, nil)

	do(h, http.MethodGet, base+"https://github.com/cli/cli/info/refs?service=git-upload-pack", nil, nil)
	if got := f.last(t).URI; got != "/cli/cli/info/refs?service=git-upload-pack" {
		t.Errorf("upstream URI = %q, want the query preserved", got)
	}
}

// --- credential hygiene ------------------------------------------------------

func TestClientCredentialsNeverReachGitHub(t *testing.T) {
	f := newFakeGitHub(t, okHandler("x"))
	h := newHandler(t, f, nil)

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+testToken)
	headers.Set("Cookie", "session=secret")
	headers.Set("X-Proxy-Token", testToken)
	headers.Set("X-Forwarded-For", "203.0.113.7")
	headers.Set("X-Real-IP", "203.0.113.7")
	headers.Set("Referer", "https://sub.example.com"+base+"something")

	do(h, http.MethodGet, base+"https://github.com/cli/cli/releases/download/v1/f.zip", nil, headers)

	for _, name := range []string{"Authorization", "Cookie", "X-Proxy-Token", "X-Forwarded-For", "X-Real-Ip", "Referer"} {
		if v := f.last(t).Headers.Get(name); v != "" {
			t.Errorf("upstream saw %s: %q; it must be stripped", name, v)
		}
	}
}

func TestUpstreamTokenIsPresentedToGitHub(t *testing.T) {
	f := newFakeGitHub(t, okHandler("x"))
	h := newHandler(t, f, func(c *config.Config) { c.UpstreamToken = "ghp_example" })

	do(h, http.MethodGet, base+"https://github.com/cli/cli/releases/download/v1/f.zip", nil, nil)
	if got := f.last(t).Headers.Get("Authorization"); got != "Bearer ghp_example" {
		t.Errorf("upstream Authorization = %q, want the configured PAT", got)
	}
}

func TestGitHubCookiesAndPolicyHeadersAreStripped(t *testing.T) {
	f := newFakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", "logged_in=no; Path=/")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		w.Header().Set("Clear-Site-Data", `"cache"`)
		_, _ = io.WriteString(w, "x")
	})
	h := newHandler(t, f, nil)

	rec := do(h, http.MethodGet, base+"https://github.com/cli/cli/releases/download/v1/f.zip", nil, nil)
	// A Set-Cookie from GitHub would be scoped to the proxy's own domain.
	for _, name := range []string{"Set-Cookie", "Content-Security-Policy", "Clear-Site-Data"} {
		if v := rec.Header().Get(name); v != "" {
			t.Errorf("response carried %s: %q; it must be stripped", name, v)
		}
	}
	if got := rec.Header().Get("X-Robots-Tag"); !strings.Contains(got, "noindex") {
		t.Errorf("X-Robots-Tag = %q, want a noindex directive", got)
	}
}

// --- redirects ---------------------------------------------------------------

func TestFollowsRedirectToAssetBackend(t *testing.T) {
	f := newFakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "github.com" {
			http.Redirect(w, r, "https://objects.githubusercontent.com/stored/f.zip?token=sig", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, "asset-bytes")
	})
	h := newHandler(t, f, nil)

	rec := do(h, http.MethodGet, base+"https://github.com/cli/cli/releases/download/v1/f.zip", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want the redirect to be followed to 200", rec.Code)
	}
	if got := rec.Body.String(); got != "asset-bytes" {
		t.Errorf("body = %q, want the asset content", got)
	}
	if f.count() != 2 {
		t.Errorf("upstream request count = %d, want 2 (original + redirect)", f.count())
	}
}

func TestRedirectDropsAuthorizationOnHostChange(t *testing.T) {
	// GitHub's storage backends reject a signed URL that also carries an
	// Authorization header.
	f := newFakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "github.com" {
			http.Redirect(w, r, "https://objects.githubusercontent.com/stored/f.zip?token=sig", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, "asset")
	})
	h := newHandler(t, f, func(c *config.Config) { c.UpstreamToken = "ghp_example" })

	do(h, http.MethodGet, base+"https://github.com/cli/cli/releases/download/v1/f.zip", nil, nil)
	if got := f.last(t).Headers.Get("Authorization"); got != "" {
		t.Errorf("asset backend saw Authorization %q, want it dropped on the cross-host hop", got)
	}
}

func TestRejectsRedirectToDisallowedHost(t *testing.T) {
	f := newFakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.example/exfiltrate", http.StatusFound)
	})
	h := newHandler(t, f, nil)

	rec := do(h, http.MethodGet, base+"https://github.com/cli/cli/releases/download/v1/f.zip", nil, nil)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 for an off-allowlist redirect", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "evil.example") {
		t.Error("error body leaked the redirect target")
	}
}

func TestPostRedirectIsReturnedThroughTheProxy(t *testing.T) {
	// A POST body cannot be replayed, so the redirect goes back to the client,
	// rewritten so the follow-up request is still authenticated.
	f := newFakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://github.com/cli/cli/git-upload-pack", http.StatusTemporaryRedirect)
	})
	h := newHandler(t, f, nil)

	rec := do(h, http.MethodPost, base+"https://github.com/cli/cli/git-upload-pack", strings.NewReader("0000"), nil)
	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want 307", rec.Code)
	}
	want := base + "https://github.com/cli/cli/git-upload-pack"
	if got := rec.Header().Get("Location"); got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

func TestRedirectRewriteOmitsTokenWhenAuthCameFromAHeader(t *testing.T) {
	f := newFakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://github.com/cli/cli/git-upload-pack", http.StatusTemporaryRedirect)
	})
	h := newHandler(t, f, nil)

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+testToken)
	rec := do(h, http.MethodPost, prefix+"https://github.com/cli/cli/git-upload-pack", strings.NewReader("0000"), headers)

	want := prefix + "https://github.com/cli/cli/git-upload-pack"
	if got := rec.Header().Get("Location"); got != want {
		t.Errorf("Location = %q, want %q (no token, since the client uses a header)", got, want)
	}
}

// --- streaming semantics -----------------------------------------------------

func TestRangeRequestIsPassedThrough(t *testing.T) {
	f := newFakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "" {
			t.Error("upstream did not receive the Range header")
		}
		w.Header().Set("Content-Range", "bytes 0-9/100")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "0123456789")
	})
	h := newHandler(t, f, nil)

	headers := http.Header{}
	headers.Set("Range", "bytes=0-9")
	rec := do(h, http.MethodGet, base+"https://github.com/cli/cli/releases/download/v1/f.zip", nil, headers)

	if rec.Code != http.StatusPartialContent {
		t.Errorf("status = %d, want 206", rec.Code)
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes 0-9/100" {
		t.Errorf("Content-Range = %q", got)
	}
}

func TestCompressedBodyIsNotReEncoded(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write([]byte("compressible content"))
	_ = zw.Close()
	compressed := buf.Bytes()

	f := newFakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(compressed)
	})
	h := newHandler(t, f, nil)

	rec := do(h, http.MethodGet, base+"https://github.com/cli/cli/releases/download/v1/f.zip", nil, nil)
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Errorf("Content-Encoding = %q, want gzip", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), compressed) {
		t.Error("body was re-encoded in transit; it must pass through byte for byte")
	}
}

func TestGitSmartHTTPRoundTrip(t *testing.T) {
	const pack = "0032want 0000000000000000000000000000000000000000\n0000"
	f := newFakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/info/refs"):
			w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
			_, _ = io.WriteString(w, "001e# service=git-upload-pack\n0000")
		case strings.HasSuffix(r.URL.Path, "/git-upload-pack"):
			w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
			body, _ := io.ReadAll(r.Body)
			_, _ = w.Write(body)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	h := newHandler(t, f, nil)

	rec := do(h, http.MethodGet, base+"https://github.com/cli/cli/info/refs?service=git-upload-pack", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("info/refs status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "service=git-upload-pack") {
		t.Errorf("info/refs body = %q", rec.Body.String())
	}

	headers := http.Header{}
	headers.Set("Content-Type", "application/x-git-upload-pack-request")
	rec = do(h, http.MethodPost, base+"https://github.com/cli/cli/git-upload-pack", strings.NewReader(pack), headers)
	if rec.Code != http.StatusOK {
		t.Fatalf("git-upload-pack status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != pack {
		t.Errorf("git-upload-pack body = %q, want the request echoed back", rec.Body.String())
	}
	if got := f.last(t).Headers.Get("Content-Type"); got != "application/x-git-upload-pack-request" {
		t.Errorf("upstream Content-Type = %q, want the git request type preserved", got)
	}
	if got := f.last(t).Body; got != pack {
		t.Errorf("upstream body = %q, want %q", got, pack)
	}
}

// --- policy ------------------------------------------------------------------

func TestSizeLimitRedirectsInsteadOfStreaming(t *testing.T) {
	f := newFakeGitHub(t, okHandler(strings.Repeat("x", 100)))
	h := newHandler(t, f, func(c *config.Config) { c.SizeLimit = 10 })

	const target = "https://github.com/cli/cli/releases/download/v1/f.zip"
	rec := do(h, http.MethodGet, base+target, nil, nil)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 for an oversized body", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != target {
		t.Errorf("Location = %q, want the real upstream URL %q", got, target)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want it dropped", rec.Body.String())
	}
}

func TestAccessLists(t *testing.T) {
	const allowed = "https://github.com/octocat/hello/releases/download/v1/f.zip"
	const other = "https://github.com/someone/thing/releases/download/v1/f.zip"

	t.Run("allow list restricts to listed owners", func(t *testing.T) {
		f := newFakeGitHub(t, okHandler("x"))
		h := newHandler(t, f, func(c *config.Config) {
			c.AllowList = mustRules(t, "octocat")
		})
		if got := do(h, http.MethodGet, base+allowed, nil, nil).Code; got != http.StatusOK {
			t.Errorf("allowed owner status = %d, want 200", got)
		}
		if got := do(h, http.MethodGet, base+other, nil, nil).Code; got != http.StatusNotFound {
			t.Errorf("unlisted owner status = %d, want 404", got)
		}
	})

	t.Run("deny list wins over allow list", func(t *testing.T) {
		f := newFakeGitHub(t, okHandler("x"))
		h := newHandler(t, f, func(c *config.Config) {
			c.AllowList = mustRules(t, "octocat")
			c.DenyList = mustRules(t, "octocat/hello")
		})
		if got := do(h, http.MethodGet, base+allowed, nil, nil).Code; got != http.StatusNotFound {
			t.Errorf("denied repo status = %d, want 404", got)
		}
	})

	t.Run("no lists means no restriction", func(t *testing.T) {
		f := newFakeGitHub(t, okHandler("x"))
		h := newHandler(t, f, nil)
		if got := do(h, http.MethodGet, base+other, nil, nil).Code; got != http.StatusOK {
			t.Errorf("status = %d, want 200", got)
		}
	})
}

func TestCORS(t *testing.T) {
	const target = "https://github.com/cli/cli/releases/download/v1/f.zip"

	t.Run("off by default", func(t *testing.T) {
		f := newFakeGitHub(t, okHandler("x"))
		h := newHandler(t, f, nil)
		rec := do(h, http.MethodGet, base+target, nil, nil)
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("Access-Control-Allow-Origin = %q, want none", got)
		}
	})

	t.Run("preflight when enabled", func(t *testing.T) {
		f := newFakeGitHub(t, okHandler("x"))
		h := newHandler(t, f, func(c *config.Config) { c.CORS = true })
		rec := do(h, http.MethodOptions, base+target, nil, nil)
		if rec.Code != http.StatusNoContent {
			t.Errorf("status = %d, want 204", rec.Code)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
			t.Errorf("Access-Control-Allow-Origin = %q, want *", got)
		}
		if f.count() != 0 {
			t.Error("a preflight must not be forwarded to GitHub")
		}
	})

	t.Run("preflight still requires the token", func(t *testing.T) {
		f := newFakeGitHub(t, okHandler("x"))
		h := newHandler(t, f, func(c *config.Config) { c.CORS = true })
		rec := do(h, http.MethodOptions, prefix+target, nil, nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})
}

func TestRootMountStillWorks(t *testing.T) {
	f := newFakeGitHub(t, okHandler("x"))
	h := newHandler(t, f, func(c *config.Config) { c.Prefix = "/" })

	rec := do(h, http.MethodGet, "/"+testToken+"/https://github.com/cli/cli/releases/download/v1/f.zip", nil, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

// --- short form (GHP_DEFAULT_HOST) -------------------------------------------

// mirrorHosts is the ordered list a mirror deployment configures.
var mirrorHosts = []string{"github.com", "raw.githubusercontent.com"}

func TestShortFormResolvesToTheDefaultHost(t *testing.T) {
	tests := []struct {
		name     string
		target   string
		wantHost string
		wantURI  string
	}{
		{
			name:     "blob shape goes to github.com and is fetched as raw",
			target:   "prettyleaf/media/blob/main/logo.png",
			wantHost: "github.com",
			wantURI:  "/prettyleaf/media/raw/main/logo.png",
		},
		{
			name:     "bare ref path falls through to raw.githubusercontent.com",
			target:   "prettyleaf/media/main/logo.png",
			wantHost: "raw.githubusercontent.com",
			wantURI:  "/prettyleaf/media/main/logo.png",
		},
		{
			name:     "release asset",
			target:   "cli/cli/releases/download/v1/f.zip",
			wantHost: "github.com",
			wantURI:  "/cli/cli/releases/download/v1/f.zip",
		},
		{
			name:     "percent encoding reaches upstream intact",
			target:   "cli/cli/releases/download/v1/my%20file%2Bx.zip",
			wantHost: "github.com",
			wantURI:  "/cli/cli/releases/download/v1/my%20file%2Bx.zip",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeGitHub(t, okHandler("bytes"))
			h := newHandler(t, f, func(c *config.Config) { c.DefaultHosts = mirrorHosts })

			rec := do(h, http.MethodGet, base+tc.target, nil, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			got := f.last(t)
			if got.Host != tc.wantHost {
				t.Errorf("upstream Host = %q, want %q", got.Host, tc.wantHost)
			}
			if got.URI != tc.wantURI {
				t.Errorf("upstream URI = %q, want %q", got.URI, tc.wantURI)
			}
		})
	}
}

func TestShortFormIsOffUnlessConfigured(t *testing.T) {
	f := newFakeGitHub(t, okHandler("bytes"))
	h := newHandler(t, f, nil)

	rec := do(h, http.MethodGet, base+"prettyleaf/media/blob/main/logo.png", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 while GHP_DEFAULT_HOST is unset", rec.Code)
	}
	if f.count() != 0 {
		t.Error("upstream was contacted for a target the proxy should not have resolved")
	}
}

func TestShortFormStillRequiresTheToken(t *testing.T) {
	f := newFakeGitHub(t, okHandler("bytes"))
	h := newHandler(t, f, func(c *config.Config) { c.DefaultHosts = mirrorHosts })

	rec := do(h, http.MethodGet, prefix+"prettyleaf/media/blob/main/logo.png", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if f.count() != 0 {
		t.Error("an unauthenticated short-form request reached GitHub")
	}
}

func TestShortFormObeysAccessLists(t *testing.T) {
	f := newFakeGitHub(t, okHandler("x"))
	h := newHandler(t, f, func(c *config.Config) {
		c.DefaultHosts = mirrorHosts
		c.DenyList = mustRules(t, "prettyleaf/secret")
	})

	if got := do(h, http.MethodGet, base+"prettyleaf/media/blob/main/logo.png", nil, nil).Code; got != http.StatusOK {
		t.Errorf("allowed repo status = %d, want 200", got)
	}
	if got := do(h, http.MethodGet, base+"prettyleaf/secret/blob/main/logo.png", nil, nil).Code; got != http.StatusNotFound {
		t.Errorf("denied repo status = %d, want 404", got)
	}
}

func TestShortFormDoesNotReinterpretForeignHosts(t *testing.T) {
	f := newFakeGitHub(t, okHandler("x"))
	h := newHandler(t, f, func(c *config.Config) { c.DefaultHosts = mirrorHosts })

	// Each of these names a host. None may be re-read as an owner segment and
	// silently served from the default host.
	for _, target := range []string{
		"https://gitlab.com/a/b/blob/main/f",
		"gitlab.com/a/b/blob/main/f",
		"http://127.0.0.1:8900/healthz",
		"https://github.com/cli/cli/main/README.md", // raw-only shape on github.com
	} {
		if got := do(h, http.MethodGet, base+target, nil, nil).Code; got != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", target, got)
		}
	}
	if f.count() != 0 {
		t.Errorf("upstream saw %d requests, want 0", f.count())
	}
}

func TestShortFormGitSmartHTTP(t *testing.T) {
	f := newFakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		_, _ = io.WriteString(w, "001e# service=git-upload-pack\n")
	})
	h := newHandler(t, f, func(c *config.Config) { c.DefaultHosts = mirrorHosts })

	rec := do(h, http.MethodGet, base+"cli/browser/info/refs?service=git-upload-pack", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := f.last(t)
	if got.Host != "github.com" {
		t.Errorf("upstream Host = %q, want github.com", got.Host)
	}
	if got.URI != "/cli/browser/info/refs?service=git-upload-pack" {
		t.Errorf("upstream URI = %q", got.URI)
	}
}

func TestShortFormRedirectStaysInsideTheProxy(t *testing.T) {
	f := newFakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "github.com" {
			http.Redirect(w, r, "https://objects.githubusercontent.com/blob/f.zip", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, "asset-bytes")
	})
	h := newHandler(t, f, func(c *config.Config) { c.DefaultHosts = mirrorHosts })

	rec := do(h, http.MethodGet, base+"cli/cli/releases/download/v1/f.zip", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "asset-bytes" {
		t.Errorf("body = %q, want the asset from the CDN host", got)
	}
}
