package main

import (
	"slices"
	"testing"
)

func TestParseConventionalCommits(t *testing.T) {
	cases := []struct {
		input       string
		kind        string
		scope       []string
		description string
		ok          bool
	}{
		{"feat: add new feature", "feat", nil, "add new feature", true},
		{"feat(scope): add new feature", "feat", []string{"scope"}, "add new feature", true},
		{"feat(scope1, scope2): add new feature", "feat", []string{"scope1", "scope2"}, "add new feature", true},
		{"lib/foo: whatever", "", nil, "", false},
	}

	for _, c := range cases {
		cc, ok := parseConventionalCommit(c.input)
		if ok != c.ok || cc.kind != c.kind || !slices.Equal(cc.scopes, c.scope) || cc.description != c.description {
			t.Errorf("parseConventionalCommit(%q) == %q, %v, %q, %v, want %q, %v, %q, %v", c.input, cc.kind, cc.scopes, cc.description, ok, c.kind, c.scope, c.description, c.ok)
		}
	}
}
