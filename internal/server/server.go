// Package server implements the request pipeline: mount prefix, authentication,
// URL validation, access lists, and finally the proxy.
package server

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/prettyleaf/gh-proxy/internal/auth"
	"github.com/prettyleaf/gh-proxy/internal/config"
	"github.com/prettyleaf/gh-proxy/internal/ghurl"
	"github.com/prettyleaf/gh-proxy/internal/metrics"
	"github.com/prettyleaf/gh-proxy/internal/proxy"
	"github.com/prettyleaf/gh-proxy/internal/status"
)

// Server is the public HTTP handler.
//
// It is deliberately a bare http.Handler rather than an http.ServeMux: ServeMux
// runs path.Clean over the request path and would answer a 301 redirect for
// "/prefix/https://github.com/..." pointing at "/prefix/https:/github.com/...",
// mangling the embedded URL before this code ever sees it.
type Server struct {
	cfg     *config.Config
	auth    *auth.Authenticator
	proxy   *proxy.Proxy
	metrics *metrics.Metrics
	status  http.Handler // nil unless GHP_STATUS_PATH is set
	log     *slog.Logger
}

// New builds the public handler. m may be nil, in which case nothing is
// counted and the status page is not served. b is the build metadata the status
// page's header displays.
func New(cfg *config.Config, p *proxy.Proxy, m *metrics.Metrics, b status.Build, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	s := &Server{
		cfg:     cfg,
		auth:    auth.New(cfg.Token, cfg.AllowAnonymous),
		proxy:   p,
		metrics: m,
		log:     log,
	}
	if m != nil && cfg.StatusPath != "" {
		// Mounted with its own path stripped, so the handler only ever sees
		// "", "/json" or "/metrics" and does not need to know where it lives.
		s.status = http.StripPrefix(cfg.StatusPath, status.New(m, StatusInfo(cfg), b))
	}
	return s
}

// StatusInfo copies the settings the status page displays. The token is not
// among them and must never become one.
func StatusInfo(cfg *config.Config) status.Info {
	return status.Info{
		Prefix:       cfg.Prefix,
		Auth:         authMode(cfg),
		StatusAuth:   cfg.StatusAuth,
		Upstream:     upstreamMode(cfg),
		AllowList:    cfg.AllowList.String(),
		DenyList:     cfg.DenyList.String(),
		DefaultHosts: cfg.DefaultHosts,
		SizeLimit:    cfg.SizeLimit,
		MaxRedirects: cfg.MaxRedirects,
		CORS:         cfg.CORS,
	}
}

func authMode(cfg *config.Config) string {
	if cfg.AllowAnonymous {
		return "anonymous"
	}
	return "token"
}

func upstreamMode(cfg *config.Config) string {
	switch {
	case cfg.UpstreamSource == config.UpstreamSourceGH:
		return "gh-cli"
	case cfg.UpstreamToken != "":
		return "token"
	default:
		return "none"
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// The status page is answered before any recording starts: it polls itself
	// every few seconds, and counting that would drown out the traffic it is
	// there to report.
	if s.status != nil && s.onStatusPath(r.URL.Path) {
		s.serveStatus(w, r)
		return
	}

	rec := s.metrics.Start(r.Method)
	cw := &countingWriter{ResponseWriter: w, status: http.StatusOK}
	defer func() { rec.Finish(cw.status, cw.written) }()

	s.serve(cw, r, rec)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request, rec *metrics.Recorder) {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodOptions:
	default:
		s.deny(w, r, rec, "method not allowed")
		return
	}

	// Work from the raw request target, not r.URL.Path. r.URL.Path is already
	// percent-decoded, and release asset names routinely contain characters
	// that must reach GitHub encoded exactly as the client sent them.
	raw := r.RequestURI
	if raw == "" || raw == "*" {
		s.deny(w, r, rec, "no request target")
		return
	}

	rest, ok := strings.CutPrefix(raw, s.cfg.Prefix)
	if !ok {
		s.deny(w, r, rec, "outside mount prefix")
		return
	}

	rest, ok = s.auth.Check(r.Header, rest)
	if !ok {
		s.deny(w, r, rec, "authentication failed")
		return
	}

	// Everything from here on is the embedded URL, query string included.
	target, err := ghurl.ParseWithDefaults(rest, s.cfg.DefaultHosts)
	if err != nil {
		s.deny(w, r, rec, "not a proxyable GitHub URL")
		return
	}

	if !s.listsAllow(target) {
		s.deny(w, r, rec, "blocked by access list")
		return
	}
	rec.Target(target.Kind.String(), repoName(target))

	if r.Method == http.MethodOptions {
		rec.Preflight()
		s.preflight(w)
		return
	}

	s.proxy.ServeHTTP(w, proxy.WithTarget(r, target.URL, s.selfBase(raw, rest)))
}

// repoName is the identity shown on the status page: what was asked for, never
// how it was asked for. A gist has no repository name, so its owner stands
// alone.
func repoName(t *ghurl.Target) string {
	if t.Repo == "" {
		return t.Owner
	}
	return t.Owner + "/" + t.Repo
}

// onStatusPath matches the configured path and everything below it, so that
// "/status", "/status/json" and "/status/metrics" all arrive here while
// "/statuses" does not.
func (s *Server) onStatusPath(p string) bool {
	return p == s.cfg.StatusPath || strings.HasPrefix(p, s.cfg.StatusPath+"/")
}

func (s *Server) serveStatus(w http.ResponseWriter, r *http.Request) {
	if !s.statusAllowed(r) {
		s.deny(w, r, nil, "status page authentication failed")
		return
	}
	s.status.ServeHTTP(w, r)
}

// statusAllowed decides who sees the page. With GHP_STATUS_AUTH=none the answer
// is "whoever the reverse proxy let through", which is the point of putting the
// page on a path of its own: tinyauth and friends guard one `location` and this
// handler stops second-guessing them.
func (s *Server) statusAllowed(r *http.Request) bool {
	if s.cfg.StatusAuth == config.StatusAuthNone {
		return true
	}
	if _, ok := s.auth.Check(r.Header, ""); ok {
		return true
	}
	// A browser navigating to a URL cannot set a header, so the token is also
	// taken from the query string here — and only here, never on the proxy
	// path, where it would end up in every Referer sent to GitHub.
	return s.auth.Match(r.URL.Query().Get("token"))
}

// listsAllow applies the allow list first and then the deny list, matching the
// evaluation order of the project this replaces.
func (s *Server) listsAllow(t *ghurl.Target) bool {
	if len(s.cfg.AllowList) > 0 && !s.cfg.AllowList.Match(t.Owner, t.Repo) {
		return false
	}
	return !s.cfg.DenyList.Match(t.Owner, t.Repo)
}

// selfBase reconstructs this proxy's own public prefix for the current request,
// including the token segment if one was used, so that a redirect handed back to
// the client stays authenticated. It is derived from the request rather than
// from configuration because the token may have arrived in a header instead.
func (s *Server) selfBase(raw, rest string) string {
	return strings.TrimSuffix(raw, rest)
}

func (s *Server) preflight(w http.ResponseWriter) {
	if s.cfg.CORS {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Methods", "GET, HEAD, POST, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "*")
		h.Set("Access-Control-Max-Age", "86400")
	}
	w.WriteHeader(http.StatusNoContent)
}

// deny answers every rejection identically: a bare 404 with no
// WWW-Authenticate, no hint of a mount point, and no distinction between "wrong
// token" and "no such path". To an unauthenticated probe the service is
// indistinguishable from a site that simply has nothing here.
func (s *Server) deny(w http.ResponseWriter, r *http.Request, rec *metrics.Recorder, reason string) {
	s.log.Debug("request denied", "reason", reason, "method", r.Method)
	rec.Deny(reason)
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte("404 page not found\n"))
}

// countingWriter observes the status code and the number of body bytes that
// reached the client, which is the only place those two are known for a
// streamed response.
type countingWriter struct {
	http.ResponseWriter
	status  int
	written int64
	wrote   bool
}

func (c *countingWriter) WriteHeader(code int) {
	if !c.wrote {
		c.status, c.wrote = code, true
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *countingWriter) Write(b []byte) (int, error) {
	c.wrote = true
	n, err := c.ResponseWriter.Write(b)
	c.written += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController reach the real writer. Without it the
// reverse proxy could not flush or hijack through this wrapper, which would
// deadlock git's smart-HTTP negotiation.
func (c *countingWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

func (c *countingWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
