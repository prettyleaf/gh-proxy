package proxy

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"
)

func rewriteOnce(t *testing.T, p *Proxy) *http.Request {
	t.Helper()
	target, err := url.Parse("https://github.com/octocat/hello/releases/download/v1/f.zip")
	if err != nil {
		t.Fatal(err)
	}
	in := WithTarget(httptest.NewRequest(http.MethodGet, "http://proxy/x", nil), target, "/")
	out := in.Clone(in.Context())
	p.rewrite(&httputil.ProxyRequest{In: in, Out: out})
	return out
}

func TestUpstreamTokenFuncIsConsultedPerRequest(t *testing.T) {
	token := "gho_first"
	p := New(Options{
		UpstreamToken:     "ghp_static",
		UpstreamTokenFunc: func() string { return token },
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	if got := rewriteOnce(t, p).Header.Get("Authorization"); got != "Bearer gho_first" {
		t.Fatalf("Authorization = %q, want the func's credential to win over the static one", got)
	}

	// The point of the func: gh rotates the token underneath a running process,
	// and the next request has to carry the new one without a restart.
	token = "gho_second"
	if got := rewriteOnce(t, p).Header.Get("Authorization"); got != "Bearer gho_second" {
		t.Errorf("Authorization = %q, want the rotated credential", got)
	}
}

func TestNoUpstreamCredentialLeavesNoAuthorization(t *testing.T) {
	p := New(Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if got := rewriteOnce(t, p).Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want none", got)
	}
}
