// Package proxy streams a validated GitHub request upstream and the response
// back, without buffering either direction.
package proxy

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// Options configures a Proxy.
type Options struct {
	// RedirectHosts limits which hosts a GET/HEAD redirect may be followed to.
	RedirectHosts map[string]bool
	// MaxRedirects caps the redirect chain; 0 disables following entirely.
	MaxRedirects int
	// SizeLimit, when non-zero, is the largest response body proxied inline.
	SizeLimit int64
	// UpstreamToken is a GitHub PAT presented to GitHub, if any.
	UpstreamToken string
	// UpstreamTokenFunc, when set, supplies that credential per request and
	// takes precedence over UpstreamToken. It exists so a credential that
	// rotates underneath the process — the gh CLI source — is picked up without
	// a restart. It runs on the request path and must not block.
	UpstreamTokenFunc func() string
	// CORS enables permissive cross-origin response headers.
	CORS bool
	// LogTargets includes upstream URLs in logs. Off by default, because with
	// the token carried in the path a logged URL is a logged credential.
	LogTargets bool

	DialTimeout           time.Duration
	ResponseHeaderTimeout time.Duration

	// BaseTransport overrides the outbound transport. Tests use it to point
	// upstream at a local server; production leaves it nil.
	BaseTransport http.RoundTripper

	Logger *slog.Logger
}

// Proxy forwards requests to GitHub.
type Proxy struct {
	rp   *httputil.ReverseProxy
	opts Options
	log  *slog.Logger
}

type requestInfo struct {
	target *url.URL
	// selfBase is this proxy's own public prefix including the token segment,
	// e.g. "/ivanghproxy/SECRET/". Redirects handed back to the client are
	// rewritten through it so the follow-up request authenticates too.
	selfBase string
}

type ctxKey struct{}

// WithTarget attaches the resolved upstream URL and the caller-facing base path
// to a request, for ServeHTTP to pick up.
func WithTarget(r *http.Request, target *url.URL, selfBase string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ctxKey{}, &requestInfo{
		target:   target,
		selfBase: selfBase,
	}))
}

func infoFrom(ctx context.Context) *requestInfo {
	info, _ := ctx.Value(ctxKey{}).(*requestInfo)
	return info
}

// New builds a Proxy.
func New(opts Options) *Proxy {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.DialTimeout == 0 {
		opts.DialTimeout = 10 * time.Second
	}
	if opts.ResponseHeaderTimeout == 0 {
		opts.ResponseHeaderTimeout = 30 * time.Second
	}

	base := opts.BaseTransport
	if base == nil {
		base = defaultTransport(opts)
	}

	p := &Proxy{opts: opts, log: opts.Logger}
	p.rp = &httputil.ReverseProxy{
		Rewrite:        p.rewrite,
		ModifyResponse: p.modifyResponse,
		ErrorHandler:   p.errorHandler,
		Transport: &followingTransport{
			base:  base,
			hosts: opts.RedirectHosts,
			max:   opts.MaxRedirects,
		},
		// Flush after every write. Without this git's smart-HTTP negotiation
		// deadlocks: both ends wait for bytes that are sitting in a buffer.
		FlushInterval: -1,
		ErrorLog:      slog.NewLogLogger(opts.Logger.Handler(), slog.LevelDebug),
	}
	return p
}

func defaultTransport(opts Options) http.RoundTripper {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   opts.DialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: opts.ResponseHeaderTimeout,
		// Do not negotiate gzip on the client's behalf. Go would otherwise add
		// its own Accept-Encoding and transparently decompress, re-encoding a
		// stream the client asked for verbatim. This is the same reason the
		// original Python implementation reimplemented requests' iter_content
		// with decode_content=False.
		DisableCompression: true,
	}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.rp.ServeHTTP(w, r)
}

func (p *Proxy) rewrite(r *httputil.ProxyRequest) {
	info := infoFrom(r.In.Context())
	if info == nil {
		return
	}
	r.Out.URL = info.target
	r.Out.Host = info.target.Host

	// Strip everything that identifies either this proxy's secret or the client.
	// Deliberately not calling r.SetXForwarded(): GitHub has no business
	// learning the caller's address.
	r.Out.Header.Del("Authorization")
	r.Out.Header.Del("Cookie")
	r.Out.Header.Del(authHeader)
	r.Out.Header.Del("X-Real-Ip")
	r.Out.Header.Del("Forwarded")
	for name := range r.Out.Header {
		if strings.HasPrefix(strings.ToLower(name), "x-forwarded-") {
			r.Out.Header.Del(name)
		}
	}
	// The client's Referer would carry the tokenized URL straight to GitHub.
	r.Out.Header.Del("Referer")

	if token := p.upstreamToken(); token != "" {
		r.Out.Header.Set("Authorization", "Bearer "+token)
	}
}

func (p *Proxy) upstreamToken() string {
	if p.opts.UpstreamTokenFunc != nil {
		return p.opts.UpstreamTokenFunc()
	}
	return p.opts.UpstreamToken
}

func (p *Proxy) modifyResponse(resp *http.Response) error {
	var info *requestInfo
	if resp.Request != nil {
		info = infoFrom(resp.Request.Context())
	}

	// GitHub's cookies would be scoped to the proxy's own domain, and its CSP
	// applies to a page we are not serving.
	resp.Header.Del("Set-Cookie")
	resp.Header.Del("Content-Security-Policy")
	resp.Header.Del("Content-Security-Policy-Report-Only")
	resp.Header.Del("Clear-Site-Data")
	resp.Header.Set("X-Robots-Tag", "noindex, nofollow, noarchive")

	if p.opts.CORS {
		resp.Header.Set("Access-Control-Allow-Origin", "*")
		resp.Header.Set("Access-Control-Expose-Headers", "*")
	}

	// A redirect that reached this point was not followed upstream (a POST, or
	// a chain that ran out). Send it back through the proxy so the client's next
	// request still carries the token.
	if loc := resp.Header.Get("Location"); loc != "" && isRedirect(resp.StatusCode) && info != nil && resp.Request != nil {
		if abs, err := resp.Request.URL.Parse(loc); err == nil {
			resp.Header.Set("Location", info.selfBase+abs.String())
		}
	}

	if p.opts.SizeLimit > 0 && resp.ContentLength > p.opts.SizeLimit && resp.Request != nil {
		return p.redirectOversized(resp)
	}
	return nil
}

// redirectOversized declines to stream a body over the configured limit and
// points the client at the real URL instead, matching the original project's
// behaviour. The upstream URL is public, so this leaks nothing the caller did
// not already supply.
func (p *Proxy) redirectOversized(resp *http.Response) error {
	target := resp.Request.URL.String()
	_ = resp.Body.Close()

	p.log.Info("response over size limit, redirecting client",
		"limit", p.opts.SizeLimit,
		"content_length", resp.ContentLength,
		slog.String("target", p.targetForLog(target)))

	resp.StatusCode = http.StatusFound
	resp.Status = http.StatusText(http.StatusFound)
	resp.Header = http.Header{
		"Location":     {target},
		"Content-Type": {"text/plain; charset=utf-8"},
		"X-Robots-Tag": {"noindex, nofollow, noarchive"},
	}
	resp.Body = io.NopCloser(strings.NewReader(""))
	resp.ContentLength = 0
	return nil
}

func (p *Proxy) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	if r.Context().Err() != nil {
		// Client hung up mid-download; not worth a log line.
		return
	}
	target := ""
	if info := infoFrom(r.Context()); info != nil {
		target = info.target.String()
	}
	p.log.Warn("upstream request failed",
		"error", err,
		slog.String("target", p.targetForLog(target)))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusBadGateway)
	_, _ = io.WriteString(w, "upstream request failed\n")
}

// targetForLog keeps upstream URLs out of logs unless explicitly enabled.
func (p *Proxy) targetForLog(target string) string {
	if !p.opts.LogTargets {
		return "(redacted)"
	}
	return target
}

func isRedirect(code int) bool {
	switch code {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

// authHeader mirrors auth.HeaderName without importing the package, keeping the
// dependency arrow pointing one way.
const authHeader = "X-Proxy-Token"
