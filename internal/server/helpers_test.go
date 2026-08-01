package server_test

import (
	"testing"

	"github.com/prettyleaf/gh-proxy/internal/ghurl"
)

func mustRules(t *testing.T, s string) ghurl.RuleSet {
	t.Helper()
	rs, err := ghurl.ParseRules(s)
	if err != nil {
		t.Fatalf("ParseRules(%q): %v", s, err)
	}
	return rs
}
