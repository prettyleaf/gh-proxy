package auth

import (
	"encoding/base64"
	"net/http"
	"testing"
)

const token = "s3cret-token-value-01"

func basic(user, pass string) http.Header {
	h := http.Header{}
	h.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+pass)))
	return h
}

func header(k, v string) http.Header {
	h := http.Header{}
	h.Set(k, v)
	return h
}

func TestCheckAcceptsEveryCredentialForm(t *testing.T) {
	const rest = "https://github.com/cli/cli/info/refs"
	a := New(token, false)

	tests := []struct {
		name          string
		headers       http.Header
		rest          string
		wantRemainder string
	}{
		{
			name:          "token as leading path segment",
			headers:       http.Header{},
			rest:          token + "/" + rest,
			wantRemainder: rest,
		},
		{
			name:          "percent-encoded path segment",
			headers:       http.Header{},
			rest:          "s3cret%2Dtoken%2Dvalue%2D01/" + rest,
			wantRemainder: rest,
		},
		{
			name:          "basic auth password half",
			headers:       basic("x", token),
			rest:          rest,
			wantRemainder: rest,
		},
		{
			name:          "basic auth username half",
			headers:       basic(token, ""),
			rest:          rest,
			wantRemainder: rest,
		},
		{
			name:          "bearer",
			headers:       header("Authorization", "Bearer "+token),
			rest:          rest,
			wantRemainder: rest,
		},
		{
			name:          "token scheme",
			headers:       header("Authorization", "token "+token),
			rest:          rest,
			wantRemainder: rest,
		},
		{
			name:          "proxy header",
			headers:       header(HeaderName, token),
			rest:          rest,
			wantRemainder: rest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			remainder, ok := a.Check(tc.headers, tc.rest)
			if !ok {
				t.Fatalf("Check rejected a valid credential")
			}
			if remainder != tc.wantRemainder {
				t.Errorf("remainder = %q, want %q", remainder, tc.wantRemainder)
			}
		})
	}
}

func TestCheckRejects(t *testing.T) {
	const rest = "https://github.com/cli/cli/info/refs"
	a := New(token, false)

	tests := []struct {
		name    string
		headers http.Header
		rest    string
	}{
		{"no credential at all", http.Header{}, rest},
		{"wrong path segment", http.Header{}, "wrong-token/" + rest},
		{"token prefix only", http.Header{}, token[:10] + "/" + rest},
		{"token with extra suffix", http.Header{}, token + "x/" + rest},
		{"wrong bearer", header("Authorization", "Bearer nope"), rest},
		{"wrong basic", basic("x", "nope"), rest},
		{"malformed basic", header("Authorization", "Basic !!!not-base64"), rest},
		{"unknown scheme", header("Authorization", "Digest "+token), rest},
		{"wrong proxy header", header(HeaderName, "nope"), rest},
		{"empty proxy header", header(HeaderName, ""), rest},
		{"token as the whole path with no target", http.Header{}, token},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := a.Check(tc.headers, tc.rest); ok {
				t.Fatal("Check accepted an invalid credential")
			}
		})
	}
}

func TestCheckLeavesRestAloneOnHeaderAuth(t *testing.T) {
	// A header credential must not eat the first segment of the target URL.
	a := New(token, false)
	const rest = "https://github.com/cli/cli/info/refs"
	got, ok := a.Check(header("Authorization", "Bearer "+token), rest)
	if !ok {
		t.Fatal("Check rejected a valid bearer token")
	}
	if got != rest {
		t.Errorf("remainder = %q, want the untouched target %q", got, rest)
	}
}

func TestAnonymousPassesEverythingThrough(t *testing.T) {
	a := New("", true)
	const rest = "https://github.com/cli/cli/info/refs"
	got, ok := a.Check(http.Header{}, rest)
	if !ok {
		t.Fatal("anonymous mode rejected a request")
	}
	if got != rest {
		t.Errorf("remainder = %q, want %q (no segment should be consumed)", got, rest)
	}
	if !a.Anonymous() {
		t.Error("Anonymous() = false, want true")
	}
}

func TestEmptyTokenNeverMatches(t *testing.T) {
	// Guards against a misconfiguration where an empty token would authenticate
	// an empty credential.
	a := New("", false)
	if _, ok := a.Check(header("Authorization", "Bearer "), "x/y"); ok {
		t.Error("empty token accepted an empty bearer credential")
	}
	if _, ok := a.Check(http.Header{}, "/https://github.com/a/b"); ok {
		t.Error("empty token accepted an empty path segment")
	}
}
