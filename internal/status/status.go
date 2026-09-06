// Package status serves the operator-facing status page.
//
// It is off unless GHP_STATUS_PATH names a path, and it is mounted outside the
// proxy's own mount prefix so a reverse proxy can put its own authentication
// (tinyauth, basic auth, an SSO forward-auth) in front of that one location.
// Three representations of the same numbers live under that path: an HTML page,
// "/json" for the page's own polling, and "/metrics" for a Prometheus scraper.
package status

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/prettyleaf/gh-proxy/internal/metrics"
)

//go:embed page.html
var page []byte

// Info is the slice of the running configuration the page displays. It is
// deliberately a flattened copy rather than the *config.Config: the token must
// never be one field-name typo away from the response body.
type Info struct {
	Prefix       string   `json:"prefix"`
	Auth         string   `json:"auth"`
	StatusAuth   string   `json:"status_auth"`
	Upstream     string   `json:"upstream"`
	AllowList    string   `json:"allow_list"`
	DenyList     string   `json:"deny_list"`
	DefaultHosts []string `json:"default_hosts"`
	SizeLimit    int64    `json:"size_limit"`
	MaxRedirects int      `json:"max_redirects"`
	CORS         bool     `json:"cors"`
}

// Build is where this binary came from: what the header's version chip shows,
// and what its popover expands into. Every field but the version is set with
// -ldflags at build time, so a plain `go build` leaves them empty and the page
// omits the rows.
//
// Repo is the GitHub repository the header links to for the star count and the
// latest release, as "owner/name". It is the only field the page sends anywhere:
// the browser asks api.github.com about it directly, so an instance with no
// outbound access from the operator's browser simply shows no counts.
type Build struct {
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
	Branch  string `json:"branch,omitempty"`
	Time    string `json:"time,omitempty"`
	Number  string `json:"number,omitempty"`
	Repo    string `json:"repo,omitempty"`
}

// Handler answers the status path and everything under it.
type Handler struct {
	m     *metrics.Metrics
	info  Info
	build Build
}

// New builds the handler. It expects to be mounted with the status path already
// stripped, so r.URL.Path is "", "/json" or "/metrics".
func New(m *metrics.Metrics, info Info, build Build) *Handler {
	return &Handler{m: m, info: info, build: build}
}

type payload struct {
	metrics.Snapshot
	Config Info  `json:"config"`
	Build  Build `json:"build"`
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		notFound(w)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")

	switch strings.TrimSuffix(r.URL.Path, "/") {
	case "":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Length", strconv.Itoa(len(page)))
		_, _ = w.Write(page)
	case "/json":
		h.writeJSON(w)
	case "/metrics":
		h.writeProm(w)
	default:
		notFound(w)
	}
}

func (h *Handler) writeJSON(w http.ResponseWriter) {
	// Rendered into a buffer first so a marshalling error cannot leave a
	// half-written 200 on the wire.
	body, err := json.Marshal(payload{Snapshot: h.m.Snapshot(), Config: h.info, Build: h.build})
	if err != nil {
		http.Error(w, "snapshot failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	_, _ = w.Write(body)
}

// writeProm renders the Prometheus text exposition format, so an existing
// scraper can take these numbers without the page in the middle.
func (h *Handler) writeProm(w http.ResponseWriter) {
	s := h.m.Snapshot()
	var b bytes.Buffer

	metric(&b, "ghproxy_build_info", "gauge", "Provenance of the running binary.")
	fmt.Fprintf(&b, "ghproxy_build_info{version=%s,commit=%s,branch=%s} 1\n",
		quote(s.Version), quote(h.build.Commit), quote(h.build.Branch))

	simple(&b, "ghproxy_uptime_seconds", "gauge", "Seconds since start.", s.Uptime)
	simple(&b, "ghproxy_requests_in_flight", "gauge", "Requests currently being served.", float64(s.InFlight))
	simple(&b, "ghproxy_requests_total", "counter", "Requests received.", float64(s.Total))
	simple(&b, "ghproxy_proxied_total", "counter", "Requests forwarded to GitHub.", float64(s.Proxied))
	simple(&b, "ghproxy_denied_total", "counter", "Requests rejected before GitHub.", float64(s.Denied))
	simple(&b, "ghproxy_preflight_total", "counter", "CORS preflight requests answered.", float64(s.Preflight))
	simple(&b, "ghproxy_bytes_sent_total", "counter", "Response body bytes written to clients.", float64(s.Bytes))

	metric(&b, "ghproxy_responses_total", "counter", "Responses by status class.")
	for _, p := range s.Status {
		fmt.Fprintf(&b, "ghproxy_responses_total{class=%s} %d\n", quote(p.Name), p.Count)
	}
	metric(&b, "ghproxy_denials_total", "counter", "Denials by reason.")
	for _, p := range s.Reasons {
		fmt.Fprintf(&b, "ghproxy_denials_total{reason=%s} %d\n", quote(p.Name), p.Count)
	}
	metric(&b, "ghproxy_targets_total", "counter", "Proxied requests by GitHub URL kind.")
	for _, p := range s.Kinds {
		fmt.Fprintf(&b, "ghproxy_targets_total{kind=%s} %d\n", quote(p.Name), p.Count)
	}

	// Not a histogram: the collector keeps a sum and a count, which is all an
	// average needs and all this page ever claimed to offer.
	metric(&b, "ghproxy_request_duration_seconds", "summary", "Time to finish a proxied request.")
	fmt.Fprintf(&b, "ghproxy_request_duration_seconds_sum %g\n", s.AvgMillis/1000*float64(s.Proxied))
	fmt.Fprintf(&b, "ghproxy_request_duration_seconds_count %d\n", s.Proxied)

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(b.Len()))
	_, _ = w.Write(b.Bytes())
}

func metric(b *bytes.Buffer, name, typ, help string) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
}

func simple(b *bytes.Buffer, name, typ, help string, v float64) {
	metric(b, name, typ, help)
	fmt.Fprintf(b, "%s %g\n", name, v)
}

// quote escapes a Prometheus label value.
func quote(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(v) + `"`
}

// notFound answers exactly as the proxy's own rejection does, so probing the
// status path for sub-resources tells an unauthenticated caller nothing that
// probing any other path would not.
func notFound(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, "404 page not found\n")
}
