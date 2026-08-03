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
	"github.com/prettyleaf/gh-proxy/internal/proxy"
)

// Server is the public HTTP handler.
//
// It is deliberately a bare http.Handler rather than an http.ServeMux: ServeMux
// runs path.Clean over the request path and would answer a 301 redirect for
// "/prefix/https://github.com/..." pointing at "/prefix/https:/github.com/...",
// mangling the embedded URL before this code ever sees it.
type Server struct {
	cfg   *config.Config
	auth  *auth.Authenticator
	proxy *proxy.Proxy
	log   *slog.Logger
}

// New builds the public handler.
func New(cfg *config.Config, p *proxy.Proxy, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		cfg:   cfg,
		auth:  auth.New(cfg.Token, cfg.AllowAnonymous),
		proxy: p,
		log:   log,
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodOptions:
	default:
		s.deny(w, r, "method not allowed")
		return
	}

	// Work from the raw request target, not r.URL.Path. r.URL.Path is already
	// percent-decoded, and release asset names routinely contain characters
	// that must reach GitHub encoded exactly as the client sent them.
	raw := r.RequestURI
	if raw == "" || raw == "*" {
		s.deny(w, r, "no request target")
		return
	}

	rest, ok := strings.CutPrefix(raw, s.cfg.Prefix)
	if !ok {
		s.deny(w, r, "outside mount prefix")
		return
	}

	rest, ok = s.auth.Check(r.Header, rest)
	if !ok {
		s.deny(w, r, "authentication failed")
		return
	}

	// Everything from here on is the embedded URL, query string included.
	target, err := ghurl.ParseWithDefaults(rest, s.cfg.DefaultHosts)
	if err != nil {
		s.deny(w, r, "not a proxyable GitHub URL")
		return
	}

	if !s.listsAllow(target) {
		s.deny(w, r, "blocked by access list")
		return
	}

	if r.Method == http.MethodOptions {
		s.preflight(w)
		return
	}

	s.proxy.ServeHTTP(w, proxy.WithTarget(r, target.URL, s.selfBase(raw, rest)))
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
func (s *Server) deny(w http.ResponseWriter, r *http.Request, reason string) {
	s.log.Debug("request denied", "reason", reason, "method", r.Method)
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte("404 page not found\n"))
}
