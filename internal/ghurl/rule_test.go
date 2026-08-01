package ghurl

import "testing"

func TestRuleMatch(t *testing.T) {
	tests := []struct {
		rule        string
		owner, repo string
		want        bool
	}{
		{"octocat", "octocat", "hello", true},
		{"octocat", "octocat", "other", true},
		{"octocat", "someone", "hello", false},
		{"octocat/hello", "octocat", "hello", true},
		{"octocat/hello", "octocat", "other", false},
		{"*/hello", "anyone", "hello", true},
		{"*/hello", "anyone", "other", false},
		{"OctoCat", "octocat", "hello", true}, // GitHub names are case-insensitive
	}
	for _, tc := range tests {
		rs, err := ParseRules(tc.rule)
		if err != nil {
			t.Fatalf("ParseRules(%q): %v", tc.rule, err)
		}
		if got := rs.Match(tc.owner, tc.repo); got != tc.want {
			t.Errorf("rule %q vs %s/%s = %v, want %v", tc.rule, tc.owner, tc.repo, got, tc.want)
		}
	}
}

func TestParseRulesSeparatorsAndComments(t *testing.T) {
	rs, err := ParseRules("a, b/c\n # comment\n*/d  e # trailing\n\n")
	if err != nil {
		t.Fatalf("ParseRules: %v", err)
	}
	if got, want := rs.String(), "a,b/c,*/d,e"; got != want {
		t.Errorf("parsed = %q, want %q", got, want)
	}
}

func TestParseRulesRejectsMalformed(t *testing.T) {
	for _, in := range []string{"/repo", "a/b/c"} {
		if _, err := ParseRules(in); err == nil {
			t.Errorf("ParseRules(%q) succeeded, want an error", in)
		}
	}
}

func TestEmptyRuleSetMatchesNothing(t *testing.T) {
	var rs RuleSet
	if rs.Match("octocat", "hello") {
		t.Error("empty RuleSet matched; a deny list must not block by default")
	}
}
