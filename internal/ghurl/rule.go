package ghurl

import (
	"fmt"
	"strings"
)

// Rule is one entry of an allow or deny list, using the same semantics as the
// original project's white_list/black_list:
//
//	octocat        every repo owned by octocat
//	octocat/hello  exactly that repo
//	*/hello        any repo named hello, whoever owns it
type Rule struct {
	Owner string
	Repo  string // empty means "any repo of this owner"
}

func (r Rule) String() string {
	if r.Repo == "" {
		return r.Owner
	}
	return r.Owner + "/" + r.Repo
}

// Match reports whether the rule covers the given owner/repo pair.
func (r Rule) Match(owner, repo string) bool {
	if r.Owner != "*" && !strings.EqualFold(r.Owner, owner) {
		return false
	}
	if r.Repo == "" {
		return true
	}
	return strings.EqualFold(r.Repo, repo)
}

// RuleSet is an ordered list of rules; the zero value matches nothing and, as an
// allow list, is treated by the caller as "no restriction".
type RuleSet []Rule

// Match reports whether any rule in the set covers owner/repo.
func (rs RuleSet) Match(owner, repo string) bool {
	for _, r := range rs {
		if r.Match(owner, repo) {
			return true
		}
	}
	return false
}

func (rs RuleSet) String() string {
	parts := make([]string, len(rs))
	for i, r := range rs {
		parts[i] = r.String()
	}
	return strings.Join(parts, ",")
}

// ParseRules reads a rule list separated by commas, newlines or whitespace.
// Blank entries and everything after a '#' on a line are ignored.
func ParseRules(s string) (RuleSet, error) {
	var out RuleSet
	for _, line := range strings.Split(s, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		for _, field := range strings.FieldsFunc(line, func(r rune) bool {
			return r == ',' || r == ' ' || r == '\t' || r == '\r'
		}) {
			owner, repo, _ := strings.Cut(field, "/")
			if owner == "" {
				return nil, fmt.Errorf("rule %q: empty owner", field)
			}
			if strings.Contains(repo, "/") {
				return nil, fmt.Errorf("rule %q: expected owner or owner/repo", field)
			}
			out = append(out, Rule{Owner: owner, Repo: repo})
		}
	}
	return out, nil
}
