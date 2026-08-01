package proxy

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// followingTransport chases redirects inside the RoundTrip call.
//
// This exists because a release download answers 302 to
// objects.githubusercontent.com, and a caller that cannot reach github.com
// cannot reach that host either — handing the redirect back would defeat the
// point of the proxy. http.Client is not usable here: ReverseProxy needs a
// RoundTripper, and a Transport on its own never follows anything, which is
// exactly the control we want.
type followingTransport struct {
	base  http.RoundTripper
	hosts map[string]bool
	max   int
}

func (t *followingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	// Only GET and HEAD are safe to replay. A POST body — git-upload-pack, in
	// practice — is a one-shot stream that cannot be buffered for a retry
	// without breaking the streaming this proxy exists to provide, so its
	// redirects go back to the client instead.
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return resp, nil
	}

	cur := req
	for i := 0; i < t.max; i++ {
		loc := resp.Header.Get("Location")
		if !isRedirect(resp.StatusCode) || loc == "" {
			return resp, nil
		}
		next, perr := cur.URL.Parse(loc)
		if perr != nil {
			return resp, nil // malformed Location: let the client deal with it
		}
		if next.Scheme != "http" && next.Scheme != "https" {
			drain(resp)
			return nil, fmt.Errorf("redirect to unsupported scheme %q", next.Scheme)
		}
		if !t.hosts[strings.ToLower(next.Hostname())] {
			drain(resp)
			return nil, fmt.Errorf("redirect to disallowed host %q", next.Hostname())
		}
		drain(resp)

		cur = nextRequest(cur, next)
		if resp, err = t.base.RoundTrip(cur); err != nil {
			return nil, err
		}
	}
	// Chain exhausted: return the final redirect, which modifyResponse rewrites
	// back through the proxy so the client can continue it.
	return resp, nil
}

func nextRequest(prev *http.Request, next *url.URL) *http.Request {
	r := prev.Clone(prev.Context())
	r.URL = next
	r.Host = ""
	r.Body = nil
	r.ContentLength = 0
	// Signed asset URLs carry their own credentials in the query string, and
	// GitHub's storage backends reject a request that also presents an
	// Authorization header. Real HTTP clients drop it on a cross-host hop too.
	if !strings.EqualFold(prev.URL.Hostname(), next.Hostname()) {
		r.Header = prev.Header.Clone()
		r.Header.Del("Authorization")
	}
	return r
}

// drain releases the connection back to the pool.
func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	_ = resp.Body.Close()
}
