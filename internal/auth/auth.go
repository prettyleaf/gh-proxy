// Package auth checks the single shared token that gates the proxy.
//
// The token is accepted in four places. The path segment is the primary one,
// because it is the only form that survives a `git clone` against a server that
// answers 404 instead of 401: git sends its first request unauthenticated and
// relies on a 401 challenge to know it should retry with credentials, which a
// stealth 404 never issues. A token already in the URL sidesteps that handshake
// entirely.
package auth

import (
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
)

// HeaderName is the proxy-specific header carrying the token.
const HeaderName = "X-Proxy-Token"

// Authenticator validates requests against a fixed token.
type Authenticator struct {
	token     []byte
	anonymous bool
}

// New returns an Authenticator. When anonymous is true every request passes and
// no token segment is consumed from the path.
func New(token string, anonymous bool) *Authenticator {
	return &Authenticator{token: []byte(token), anonymous: anonymous}
}

// Anonymous reports whether authentication is disabled entirely.
func (a *Authenticator) Anonymous() bool { return a.anonymous }

// Check authenticates a request.
//
// rest is the part of the request target following the mount prefix, with no
// leading slash. If the token was supplied as the leading path segment, the
// returned remainder has that segment removed; otherwise rest is returned
// unchanged. ok is false when no accepted credential matched.
func (a *Authenticator) Check(h http.Header, rest string) (remainder string, ok bool) {
	if a.anonymous {
		return rest, true
	}
	if r, matched := a.checkPath(rest); matched {
		return r, true
	}
	if a.checkHeaders(h) {
		return rest, true
	}
	return rest, false
}

// checkPath consumes a leading "<token>/" segment.
func (a *Authenticator) checkPath(rest string) (string, bool) {
	seg, remainder, found := strings.Cut(rest, "/")
	if !found || seg == "" {
		return rest, false
	}
	if a.equal(seg) {
		return remainder, true
	}
	// Tolerate a percent-encoded segment; some clients escape path components
	// wholesale even when the token needs no escaping.
	if dec, err := url.PathUnescape(seg); err == nil && dec != seg && a.equal(dec) {
		return remainder, true
	}
	return rest, false
}

func (a *Authenticator) checkHeaders(h http.Header) bool {
	if a.equal(h.Get(HeaderName)) {
		return true
	}
	authz := h.Get("Authorization")
	scheme, value, found := strings.Cut(authz, " ")
	if !found {
		return false
	}
	value = strings.TrimSpace(value)
	switch {
	case strings.EqualFold(scheme, "bearer"), strings.EqualFold(scheme, "token"):
		return a.equal(value)
	case strings.EqualFold(scheme, "basic"):
		raw, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			return false
		}
		user, pass, _ := strings.Cut(string(raw), ":")
		// Accept the token as either half so that both
		// https://x:TOKEN@host/... and https://TOKEN@host/... work.
		return a.equal(pass) || a.equal(user)
	}
	return false
}

func (a *Authenticator) equal(candidate string) bool {
	if len(a.token) == 0 || candidate == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(candidate), a.token) == 1
}
