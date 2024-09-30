package main

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/google/go-github/github"
	"golang.org/x/mod/semver"
)

type semverCmd struct{}

func (cmd *semverCmd) Run(o *commonOptions) error {
	ctx := context.Background()
	tags, _, err := o.client.Repositories.ListTags(ctx, o.Owner, o.Repo, &github.ListOptions{})
	if err != nil {
		return err
	}

	// Get the latest tag
	var latestTag *github.RepositoryTag
	for _, tag := range tags {
		if !semver.IsValid(tag.GetName()) {
			continue
		}
		if semver.Prerelease(tag.GetName()) != "" {
			continue
		}
		if latestTag == nil {
			latestTag = tag
			continue
		}
		if semver.Compare(tag.GetName(), latestTag.GetName()) > 0 {
			latestTag = tag
		}
	}

	fmt.Println("Latest tag:", latestTag.GetName())

	// Get the commits since that tag
	comp, _, err := o.client.Repositories.CompareCommits(ctx, o.Owner, o.Repo, latestTag.GetCommit().GetSHA(), "main")
	if err != nil {
		return err
	}

	// Print the commits
	bumpMinor := false
	var commits []conventionalCommit
	for _, commit := range comp.Commits {
		msg := commit.GetCommit().GetMessage()
		msg, _, _ = strings.Cut(msg, "\n")
		cc, ok := parseConventionalCommit(msg)
		if !ok {
			fmt.Println("-", commit.GetSHA(), msg)
			continue
		}
		commits = append(commits, cc)
		fmt.Println("+", commit.GetSHA(), cc.kind, cc.scopes, cc.description)
		bumpMinor = bumpMinor || cc.feature()
	}

	ver := parseVer(latestTag.GetName())
	if bumpMinor {
		ver[1]++
		ver[2] = 0
	} else {
		ver[2]++
	}

	fmt.Println(formatVer(ver))
	fmt.Println(formatCommitList(commits))

	return nil
}

func formatCommitList(commits []conventionalCommit) string {
	slices.SortFunc(commits, func(a, b conventionalCommit) int {
		if len(a.scopes) > 0 && len(b.scopes) > 0 {
			if d := cmp.Compare(a.scopes[0], b.scopes[0]); d != 0 {
				return d
			}
		}
		return cmp.Compare(a.description, b.description)
	})

	var fixes, features, others []conventionalCommit
	for _, cc := range commits {
		if cc.fix() {
			fixes = append(fixes, cc)
		} else if cc.feature() {
			features = append(features, cc)
		} else {
			others = append(others, cc)
		}
	}

	var b strings.Builder
	if len(fixes) > 0 {
		b.WriteString("## Bugfixes:\n")
		for _, cc := range fixes {
			b.WriteString("- ")
			b.WriteString(cc.messageString())
			b.WriteRune('\n')
		}
		b.WriteRune('\n')
	}
	if len(features) > 0 {
		b.WriteString("## Features:\n")
		for _, cc := range features {
			b.WriteString("- ")
			b.WriteString(cc.messageString())
			b.WriteRune('\n')
		}
		b.WriteRune('\n')
	}
	if len(others) > 0 {
		b.WriteString("## Other things:\n")
		for _, cc := range others {
			b.WriteString("- ")
			b.WriteString(cc.messageString())
			b.WriteRune('\n')
		}
		b.WriteRune('\n')
	}
	return b.String()
}

func parseVer(v string) []int {
	var ver []int
	v = strings.TrimPrefix(v, "v")
	for _, s := range strings.Split(v, ".") {
		d, _ := strconv.Atoi(s)
		ver = append(ver, d)
	}
	for len(ver) < 3 {
		ver = append(ver, 0)
	}
	return ver
}

func formatVer(ver []int) string {
	return fmt.Sprintf("v%d.%d.%d", ver[0], ver[1], ver[2])
}
