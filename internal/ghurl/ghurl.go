// Package ghurl decides which GitHub URLs the proxy is willing to forward.
//
// Everything it rejects becomes a 404 for the client, so the matcher doubles as
// the anti-SSRF boundary: this service is not a general-purpose relay, it only
// speaks to a fixed set of GitHub hosts on a fixed set of path shapes.
package ghurl

import (
	"errors"
	"net/url"
	"strings"
)

// ErrNoMatch means the URL is not a GitHub URL this proxy forwards.
var ErrNoMatch = errors.New("ghurl: no match")

// Kind identifies which of the supported GitHub URL shapes matched.
type Kind int

const (
	KindInvalid Kind = iota
	KindRelease      // github.com/{o}/{r}/releases/... and /archive/...
	KindBlob         // github.com/{o}/{r}/blob/... and /raw/...
	KindGit          // github.com/{o}/{r}/info/refs, /git-upload-pack, /git-receive-pack
	KindTags         // github.com/{o}/{r}/tags...
	KindRaw          // raw.githubusercontent.com/{o}/{r}/{ref}/...
	KindGist         // gist.githubusercontent.com/{o}/{id}/...
)

func (k Kind) String() string {
	switch k {
	case KindRelease:
		return "release"
	case KindBlob:
		return "blob"
	case KindGit:
		return "git"
	case KindTags:
		return "tags"
	case KindRaw:
		return "raw"
	case KindGist:
		return "gist"
	default:
		return "invalid"
	}
}

// Target is a validated, normalized upstream request.
type Target struct {
	Kind  Kind
	Owner string
	Repo  string // empty for gists, which have no repo name
	URL   *url.URL
}

// Parse repairs, validates and normalizes an embedded GitHub URL.
//
// raw is the remainder of the request target after the mount prefix and the
// auth token have been stripped, e.g.
// "https://github.com/cli/cli/info/refs?service=git-upload-pack".
func Parse(raw string) (*Target, error) {
	return ParseWithDefaults(raw, nil)
}

// ParseWithDefaults is Parse with a fallback for targets that name no host,
// e.g. "cli/cli/blob/trunk/README.md". Each host in defaults is tried in turn
// and the first one whose path shape matches wins, which is what makes the
// proxy usable as a mirror: the client's URL is the GitHub URL with the host
// chopped off.
//
// defaults must contain only hosts SupportedHost accepts; an empty list keeps
// the strict behaviour of Parse, where the host is mandatory.
func ParseWithDefaults(raw string, defaults []string) (*Target, error) {
	u, err := prepare(raw)
	if err != nil {
		return nil, err
	}
	if t, err := match(u); err == nil {
		return t, nil
	}
	// A target that named a host is taken at face value. Retrying a recognized
	// one against the defaults would quietly move a request the client aimed at
	// github.com over to raw.githubusercontent.com, which is a different
	// resource; retrying an unrecognized one would turn "gitlab.com" into an
	// owner name and answer with somebody else's file.
	if namesAHost(u) {
		return nil, ErrNoMatch
	}
	for _, h := range defaults {
		u2, err := prepare(h + "/" + hostless(u))
		if err != nil {
			continue
		}
		if t, err := match(u2); err == nil {
			return t, nil
		}
	}
	return nil, ErrNoMatch
}

// prepare turns a request remainder into a normalized absolute URL. It does no
// matching, so the result is not yet known to be a GitHub URL this proxy will
// forward.
func prepare(raw string) (*url.URL, error) {
	u, err := url.Parse(RepairScheme(raw))
	if err != nil {
		return nil, ErrNoMatch
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, ErrNoMatch
	}
	// Credentials embedded in the *target* are never forwarded; the only
	// credential this proxy honours is its own token.
	u.User = nil
	// Always talk to GitHub over TLS regardless of what the client asked for.
	u.Scheme = "https"
	return u, nil
}

// match validates the host and path shape, canonicalizing u in place.
func match(u *url.URL) (*Target, error) {
	host, candidates, ok := hostCandidates(strings.ToLower(u.Hostname()))
	if !ok {
		return nil, ErrNoMatch
	}
	u.Host = host

	var t *Target
	for _, c := range candidates {
		if m := c.match(u.Path); m != nil {
			t = newTarget(c.kind, m, u)
			break
		}
	}
	if t == nil {
		return nil, ErrNoMatch
	}

	// A /blob/ URL renders HTML; /raw/ serves the file, which is what anyone
	// putting a URL through an accelerator actually wants.
	if t.Kind == KindBlob {
		rewriteBlobToRaw(t.URL)
	}
	return t, nil
}

// hostCandidates is the entire host allow list: the set of hosts this proxy
// will talk to, and the path shapes each one accepts. Nothing else reaches the
// network, which is what keeps the service from being a general-purpose relay.
func hostCandidates(host string) (canonical string, candidates []candidate, ok bool) {
	switch host {
	case "github.com", "www.github.com":
		return "github.com", githubCandidates, true
	case "raw.githubusercontent.com", "raw.github.com":
		return host, rawCandidates, true
	case "gist.github.com", "gist.githubusercontent.com":
		return host, gistCandidates, true
	default:
		return "", nil, false
	}
}

// SupportedHost reports whether host may be proxied, and so whether it is a
// usable default host. Configuration validates against it at startup.
func SupportedHost(host string) bool {
	_, _, ok := hostCandidates(strings.ToLower(strings.TrimSpace(host)))
	return ok
}

// namesAHost reports whether the client actually spelled out a host, as opposed
// to handing over a bare repository path whose first segment url.Parse had to
// take for the authority.
//
// A dot or a colon is the tell: GitHub owner names are alphanumerics and
// hyphens, so "prettyleaf" is a path segment while "gitlab.com", "127.0.0.1",
// "[::1]" and "example:8080" are all attempts to name a host. Only the former
// may have a default host spliced in front of it.
func namesAHost(u *url.URL) bool {
	if _, _, known := hostCandidates(strings.ToLower(u.Hostname())); known {
		return true
	}
	return strings.ContainsAny(u.Host, ".:")
}

// hostless re-renders a URL as the path it would have been without a host, so
// that a default host can be pasted in front of it. u.Host holds whatever
// url.Parse took for the authority, which for a hostless target is really the
// first path segment ("cli" in "cli/cli/blob/trunk/README.md").
//
// The result is a string spliced before url.Parse, exactly like the original
// request target, so percent-encoding and the query survive untouched.
func hostless(u *url.URL) string {
	s := u.Host + u.EscapedPath()
	if u.RawQuery != "" {
		s += "?" + u.RawQuery
	}
	return strings.TrimPrefix(s, "/")
}

type candidate struct {
	kind  Kind
	match func(path string) []string
}

var (
	githubCandidates = []candidate{
		{KindRelease, matchRelease},
		{KindBlob, matchBlob},
		{KindGit, matchGit},
		{KindTags, matchTags},
	}
	rawCandidates  = []candidate{{KindRaw, matchRaw}}
	gistCandidates = []candidate{{KindGist, matchGist}}
)

func newTarget(k Kind, m []string, u *url.URL) *Target {
	t := &Target{Kind: k, Owner: m[0], URL: u}
	if len(m) > 1 {
		t.Repo = m[1]
	}
	return t
}

// The path matchers below take a decoded path (u.Path) and return the captured
// segments, or nil. They are deliberately written against [^/]+ segments rather
// than the original project's non-greedy .+? so that an owner or repo can never
// swallow a slash and shift the meaning of the rest of the path.

func matchRelease(p string) []string {
	o, r, rest, ok := ownerRepo(p)
	if !ok {
		return nil
	}
	if seg, tail, hasMore := cut(rest); (seg == "releases" || seg == "archive") && hasMore && tail != "" {
		return []string{o, r}
	}
	return nil
}

func matchBlob(p string) []string {
	o, r, rest, ok := ownerRepo(p)
	if !ok {
		return nil
	}
	if seg, tail, hasMore := cut(rest); (seg == "blob" || seg == "raw") && hasMore && tail != "" {
		return []string{o, r}
	}
	return nil
}

func matchGit(p string) []string {
	o, r, rest, ok := ownerRepo(p)
	if !ok {
		return nil
	}
	switch {
	case rest == "info/refs",
		rest == "git-upload-pack",
		rest == "git-receive-pack":
		return []string{o, r}
	}
	return nil
}

func matchTags(p string) []string {
	o, r, rest, ok := ownerRepo(p)
	if !ok {
		return nil
	}
	if seg, _, _ := cut(rest); seg == "tags" {
		return []string{o, r}
	}
	return nil
}

// matchRaw covers raw.githubusercontent.com/{owner}/{repo}/{ref}/{path...}.
func matchRaw(p string) []string {
	o, r, rest, ok := ownerRepo(p)
	if !ok {
		return nil
	}
	if _, tail, hasMore := cut(rest); hasMore && tail != "" {
		return []string{o, r}
	}
	return nil
}

// matchGist covers gist.githubusercontent.com/{owner}/{gistID}/{path...}.
func matchGist(p string) []string {
	o, rest, ok := firstSegment(p)
	if !ok {
		return nil
	}
	if _, tail, hasMore := cut(rest); hasMore && tail != "" {
		return []string{o}
	}
	return nil
}

// ownerRepo splits "/owner/repo/rest..." and requires a non-empty rest.
func ownerRepo(p string) (owner, repo, rest string, ok bool) {
	owner, after, ok := firstSegment(p)
	if !ok {
		return "", "", "", false
	}
	repo, rest, hasMore := cut(after)
	if repo == "" || !hasMore || rest == "" {
		return "", "", "", false
	}
	return owner, repo, rest, true
}

// firstSegment strips the leading slash and splits off the first path segment.
func firstSegment(p string) (seg, rest string, ok bool) {
	if !strings.HasPrefix(p, "/") {
		return "", "", false
	}
	seg, rest, hasMore := cut(p[1:])
	if seg == "" || !hasMore {
		return "", "", false
	}
	return seg, rest, true
}

func cut(s string) (before, after string, found bool) {
	return strings.Cut(s, "/")
}

// rewriteBlobToRaw turns the first /blob/ segment into /raw/, preserving any
// percent-encoding in the rest of the path.
func rewriteBlobToRaw(u *url.URL) {
	esc := u.EscapedPath()
	i := strings.Index(esc, "/blob/")
	if i < 0 {
		return
	}
	esc = esc[:i] + "/raw/" + esc[i+len("/blob/"):]
	if pu, err := url.Parse(esc); err == nil {
		u.Path = pu.Path
		u.RawPath = pu.RawPath
	}
}

// RepairScheme puts a usable scheme back on an embedded URL.
//
// Reverse proxies that normalize the request target collapse the "//" in
// "https://github.com/..." down to "https:/github.com/...". The recommended
// nginx configuration avoids this, but Caddy, Traefik and an nginx block with a
// rewrite in it all do collapse, so repair it rather than depend on the front
// end. Some clients also percent-encode the whole embedded URL.
func RepairScheme(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	if low := strings.ToLower(s); strings.HasPrefix(low, "https%3a") || strings.HasPrefix(low, "http%3a") {
		if dec, err := url.PathUnescape(s); err == nil {
			s = dec
		}
	}
	switch low := strings.ToLower(s); {
	case strings.HasPrefix(low, "https://"), strings.HasPrefix(low, "http://"):
		return s
	case strings.HasPrefix(low, "https:/"):
		return "https://" + s[len("https:/"):]
	case strings.HasPrefix(low, "http:/"):
		return "http://" + s[len("http:/"):]
	case strings.HasPrefix(low, "https:"):
		return "https://" + s[len("https:"):]
	case strings.HasPrefix(low, "http:"):
		return "http://" + s[len("http:"):]
	default:
		return "https://" + s
	}
}
