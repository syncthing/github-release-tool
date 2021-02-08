package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/google/go-github/github"
	"golang.org/x/oauth2"
)

type cliOptions struct {
	commonOptions

	Milestone milestoneOptions `cmd:"" help:"Collect resolved issues into milestone"`
	Changelog changelogOptions `cmd:"" help:"Show changelog for milestone"`
	Release   releaseOptions   `cmd:"" help:"Create release from milestone"`
}

type commonOptions struct {
	Owner string `required:"" env:"GRT_OWNER" help:"Owner name"`
	Repo  string `required:"" env:"GRT_REPO" help:"Repository name"`

	ctx    context.Context
	client *github.Client
}

type dryRunFlag struct {
	DryRun bool `help:"Don't do it, just report what would be done"`
}

type skipLabelFlag struct {
	SkipLabels []string `placeholder:"LABEL" env:"GRT_SKIPLABELS" help:"Issue labels to skip"`
}

type releaseArg struct {
	Release string `arg:"" required:"" help:"The release name"`
}

type milestoneOptions struct {
	dryRunFlag
	From      string `placeholder:"TAG/COMMIT" help:"Start tag/commit"`
	To        string `placeholder:"TAG/COMMIT" default:"HEAD" help:"End tag/commit"`
	Force     bool   `help:"Overwrite milestone on already milestoned issues"`
	Milestone string `arg:"" required:"" help:"The milestone name"`
}

type changelogOptions struct {
	skipLabelFlag
	releaseArg
	Md        bool   `help:"Markdown links"`
	SkipLabel string `placeholder:"LABEL" env:"GRT_SKIPLABELS" help:"Issue labels to skip"`
}

type releaseOptions struct {
	dryRunFlag
	skipLabelFlag
	releaseArg
}

func main() {
	// log is used for error messages only. Normal messages go to stdout via
	// package fmt.
	log.SetFlags(log.Lshortfile)

	var cli cliOptions

	// Initialize a client, with or without authentication.
	cli.ctx = context.Background()
	var tc *http.Client
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		tc = oauth2.NewClient(cli.ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token}))
	}
	cli.client = github.NewClient(tc)

	cmd := kong.Parse(&cli)
	cmd.FatalIfErrorf(cmd.Run(&cli.commonOptions))
}

func (o *milestoneOptions) Run(common *commonOptions) error {
	return createMilestone(common.ctx, common.client, common.Owner, common.Repo, o.From, o.To, o.Milestone, o.Force, o.DryRun)
}

func (o changelogOptions) Run(common *commonOptions) error {
	return changelog(common.ctx, os.Stdout, common.client, common.Owner, common.Repo, o.Release, o.Md, o.SkipLabels, true)

}

func (o releaseOptions) Run(common *commonOptions) error {
	buf := new(bytes.Buffer)
	if err := changelog(common.ctx, buf, common.client, common.Owner, common.Repo, o.Release, false, o.SkipLabels, false); err != nil {
		return err
	}
	return createRelease(common.ctx, common.client, common.Owner, common.Repo, o.Release, buf.String())
}

func createMilestone(ctx context.Context, client *github.Client, owner, repo, since, to, milestone string, force, dryRun bool) error {
	stone, err := getMilestone(ctx, client, owner, repo, milestone)
	if err != nil {
		log.Println("Creating milestone", milestone)
		if !dryRun {
			stone = &github.Milestone{
				Title: github.String(milestone),
			}
			stone, _, err = client.Issues.CreateMilestone(ctx, owner, repo, stone)
			if err != nil {
				return err
			}
		}
	}

	commits, err := listCommits(ctx, client, owner, repo, since, to)
	if err != nil {
		return fmt.Errorf("listing commits: %w", err)
	}

	for _, fix := range getFixes(commits) {
		issue, _, err := client.Issues.Get(ctx, owner, repo, fix)
		if err != nil {
			log.Println("Getting issue:", err)
			continue
		}

		if issue.GetState() != "closed" {
			log.Println("Issue", fix, "is not closed; not marking")
			continue
		}
		if issue.Milestone != nil {
			if issue.Milestone.GetNumber() == stone.GetNumber() {
				// It's already correctly set
				log.Println("Issue", fix, "is already correctly marked")
				continue
			} else if !force {
				log.Println("Issue", fix, "is already marked with another milestone")
				continue
			}
		}

		// Set the issue milestone.
		log.Println("Marking issue", fix)
		if !dryRun {
			_, _, err = client.Issues.Edit(ctx, owner, repo, issue.GetNumber(), &github.IssueRequest{
				Milestone: github.Int(stone.GetNumber()),
			})
			if err != nil {
				log.Println("Setting milestone on issue:", err)
				continue
			}
		}
	}
	return nil
}

func changelog(ctx context.Context, w io.Writer, client *github.Client, owner, repo, release string, markdownLinks bool, skipLabels []string, withSubject bool) error {
	milestone := strings.SplitN(release, "-", 2)[0]

	stone, err := getMilestone(ctx, client, owner, repo, milestone)
	if err != nil {
		return fmt.Errorf("getting milestone: %w", err)
	}

	opts := &github.IssueListByRepoOptions{
		Milestone: strconv.Itoa(stone.GetNumber()),
		State:     "all",
	}
	var issues []*github.Issue
	for {
		is, resp, err := client.Issues.ListByRepo(ctx, owner, repo, opts)
		if err != nil {
			return fmt.Errorf("listing issues: %w", err)
		}
		issues = append(issues, is...)
		if resp.NextPage <= opts.Page {
			break
		}
		opts.Page = resp.NextPage
	}

	sort.Slice(issues, func(a, b int) bool {
		return issues[a].GetNumber() < issues[b].GetNumber()
	})

	var bugs, enhancements, other []*github.Issue
nextIssue:
	for _, issue := range issues {
		if issue.IsPullRequest() {
			continue
		}

		labels := labels(issue)
		for _, skip := range skipLabels {
			if contains(skip, labels) {
				continue nextIssue
			}
		}

		switch {
		case contains("bug", labels):
			bugs = append(bugs, issue)
		case contains("enhancement", labels):
			enhancements = append(enhancements, issue)
		default:
			other = append(other, issue)
		}
	}

	if withSubject {
		if markdownLinks {
			fmt.Fprintf(w, "# [%s](https://github.com/%s/%s/releases/%s)\n\n", release, owner, repo, release)
		} else {
			fmt.Fprintf(w, "%s\n\n", release)
		}
	}

	if descr := stone.GetDescription(); descr != "" {
		descr := wrap(strings.TrimSpace(descr), 72)
		fmt.Fprintf(w, "%s\n\n", descr)
	}

	if len(bugs) > 0 {
		if markdownLinks {
			fmt.Fprintf(w, "## Bugfixes\n\n")
		} else {
			fmt.Fprintf(w, "Bugfixes:\n\n")
		}
		printIssues(w, bugs, markdownLinks)
		fmt.Fprintf(w, "\n")
	}
	if len(enhancements) > 0 {
		if markdownLinks {
			fmt.Fprintf(w, "## Enhancements\n\n")
		} else {
			fmt.Fprintf(w, "Enhancements:\n\n")
		}
		printIssues(w, enhancements, markdownLinks)
		fmt.Fprintf(w, "\n")
	}
	if len(other) > 0 {
		if markdownLinks {
			fmt.Fprintf(w, "## Other issues\n\n")
		} else {
			fmt.Fprintf(w, "Other issues:\n\n")
		}
		printIssues(w, other, markdownLinks)
		fmt.Fprintf(w, "\n")
	}
	return nil
}

func createRelease(ctx context.Context, client *github.Client, owner, repo, release string, changelog string) error {
	splits := strings.SplitN(release, "-", 2)
	milestone := splits[0]
	pre := release != milestone

	stone, err := getMilestone(ctx, client, owner, repo, milestone)
	if err != nil {
		return fmt.Errorf("getting milestone: %w", err)
	}

	rel := &github.RepositoryRelease{
		Name:       github.String(release),
		TagName:    github.String(release),
		Body:       github.String(changelog),
		Prerelease: github.Bool(pre),
		Draft:      github.Bool(false),
	}
	if _, _, err := client.Repositories.CreateRelease(ctx, owner, repo, rel); err != nil {
		return err
	}

	if !pre { // Close milestone
		_, _, err := client.Issues.EditMilestone(ctx, owner, repo, stone.GetNumber(), &github.Milestone{
			State: github.String("closed"),
		})
		if err != nil {
			return fmt.Errorf("closing milestone: %w")
		}
	}
	return nil
}

func printIssues(w io.Writer, issues []*github.Issue, markdownLinks bool) {
	for _, issue := range issues {
		if markdownLinks {
			fmt.Fprintf(w, "- [#%d](%s): %s\n", issue.GetNumber(), issue.GetHTMLURL(), issue.GetTitle())
		} else {
			fmt.Fprintf(w, "- #%d: %s\n", issue.GetNumber(), issue.GetTitle())
		}
	}
}

func labels(issue *github.Issue) []string {
	labels := make([]string, len(issue.Labels))
	for i, l := range issue.Labels {
		labels[i] = l.GetName()
	}
	sort.Strings(labels)
	return labels
}

func contains(s string, ss []string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

func listCommits(ctx context.Context, client *github.Client, owner, repo, since, to string) ([]github.RepositoryCommit, error) {
	commits, _, err := client.Repositories.CompareCommits(ctx, owner, repo, since, to)
	if err != nil {
		return nil, err
	}
	return commits.Commits, nil
}

func getFixes(commits []github.RepositoryCommit) []int {
	fixesRe := regexp.MustCompile(`fixes #(\d+)`)
	pullReqRe := regexp.MustCompile(`\(#(\d+)\)$`)
	var fixes []int
	seen := make(map[int]struct{})
	for _, commit := range commits {
		msg := commit.Commit.GetMessage()
		lines := strings.Split(msg, "\n")
		msg = lines[0]

		matches := fixesRe.FindAllStringSubmatch(msg, -1)
		for _, m := range matches {
			num, err := strconv.Atoi(m[1])
			if err != nil {
				continue // can't happen
			}
			if _, ok := seen[num]; ok {
				continue
			}
			fixes = append(fixes, num)
			seen[num] = struct{}{}
		}

		match := pullReqRe.FindStringSubmatch(msg)
		if len(match) == 2 {
			num, err := strconv.Atoi(match[1])
			if err != nil {
				continue // can't happen
			}
			if _, ok := seen[num]; ok {
				continue
			}
			fixes = append(fixes, num)
			seen[num] = struct{}{}
		}
	}
	sort.Ints(fixes)
	return fixes
}

func getMilestone(ctx context.Context, client *github.Client, owner, repo, name string) (*github.Milestone, error) {
	opts := &github.MilestoneListOptions{State: "all"}
	for {
		stones, resp, err := client.Issues.ListMilestones(ctx, owner, repo, opts)
		if err != nil {
			return nil, err
		}

		var stone *github.Milestone
		for _, stone = range stones {
			if stone.GetTitle() == name {
				return stone, nil
			}
		}

		if resp.NextPage <= opts.Page {
			break
		}

		opts.Page = resp.NextPage
	}

	return nil, errors.New("not found")
}

func wrap(s string, w int) string {
	var b strings.Builder
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if i > 0 {
			b.WriteRune('\n')
		}
		b.WriteString(wrapParagraph(line, w))
	}
	return b.String()
}

func wrapParagraph(s string, w int) string {
	var b strings.Builder
	words := strings.Fields(s)
	if len(words) == 0 {
		return ""
	}

	pref1 := ""
	switch words[0] {
	case "-", "*":
		pref1 = "  "
	}

	l := 0
	for _, word := range words {
		if l > 0 {
			b.WriteRune(' ')
			l++
		}
		if l+len(word) > w {
			b.WriteRune('\n')
			l = 0
			b.WriteString(pref1)
			l += len(pref1)
		}
		b.WriteString(word)
		l += len(word)
	}
	return b.String()
}
