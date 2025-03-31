package main

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

var ccExp = regexp.MustCompile(`^(?P<type>\w+)(?:\((?P<scope>.+)\))?!?: (?P<description>.+)$`)

type conventionalCommit struct {
	kind        string
	scopes      []string
	description string
}

func (c conventionalCommit) feature() bool {
	return c.kind == "feat"
}

func (c conventionalCommit) fix() bool {
	return c.kind == "fix"
}

func (c conventionalCommit) messageString() string {
	sentenceDescr := strings.ToUpper(c.description[:1]) + c.description[1:]
	if len(c.scopes) == 0 {
		return sentenceDescr
	}
	return fmt.Sprintf("_%s:_ %s", strings.Join(c.scopes, ", "), sentenceDescr)
}

func parseConventionalCommit(msg string) (conventionalCommit, bool) {
	matches := ccExp.FindStringSubmatch(msg)
	if matches == nil {
		return conventionalCommit{}, false
	}

	cc := conventionalCommit{
		kind:        matches[1],
		description: matches[3],
	}
	if matches[2] != "" {
		cc.scopes = strings.Split(matches[2], ",")
		for i := range cc.scopes {
			cc.scopes[i] = strings.TrimSpace(cc.scopes[i])
		}
		slices.Sort(cc.scopes)
	}
	return cc, true
}
