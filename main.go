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

	"github.com/alecthomas/kingpin"
	"github.com/google/go-github/github"
	"golang.org/x/oauth2"
)

var dryRun = false

func main() {
	// log is used for error messages only. Normal messages go to stdout via
	// package fmt.
	log.SetFlags(log.Lshortfile)

	// Set up commands.
	var repo, owner string
	var since, to string
	var skipLabels []string
	var markdownLinks bool
	var milestone string
	var forceMilestone bool

	kingpin.Flag("owner", "Owner name (or set GRT_OWNER)").Envar("GRT_OWNER").Required().StringVar(&owner)
	kingpin.Flag("repo", "Repository name (or set GRT_REPO)").Envar("GRT_REPO").Required().StringVar(&repo)

	cmdMilestone := kingpin.Command("milestone", "Collect resolved issues into milestone")
	cmdMilestone.Flag("dry-run", "Don't do it, just report what would be done").BoolVar(&dryRun)
	cmdMilestone.Flag("from", "Start tag/commit").PlaceHolder("TAG/COMMIT").Required().StringVar(&since)
	cmdMilestone.Flag("to", "End tag/commit").Default("HEAD").StringVar(&to)
	cmdMilestone.Flag("force", "Overwrite milestone on already milestoned issues").BoolVar(&forceMilestone)
	cmdMilestone.Arg("milestone", "The milestone name").Required().StringVar(&milestone)

	cmdChangelog := kingpin.Command("changelog", "Show changelog for milestone")
	cmdChangelog.Flag("md", "Markdown links").BoolVar(&markdownLinks)
	cmdChangelog.Flag("skip-label", "Issue labels to skip").PlaceHolder("LABEL").Envar("GRT_SKIPLABELS").StringsVar(&skipLabels)
	cmdChangelog.Arg("milestone", "The milestone name").Required().StringVar(&milestone)

	cmdRelease := kingpin.Command("release", "Create release from milestone")
	cmdRelease.Flag("dry-run", "Don't do it, just report what would be done").BoolVar(&dryRun)
	cmdRelease.Flag("to", "Release name/version (default is milestone name)").PlaceHolder("NAME").StringVar(&to)
	cmdRelease.Flag("skip-label", "Issue labels to skip").PlaceHolder("LABEL").Envar("GRT_SKIPLABELS").StringsVar(&skipLabels)
	cmdRelease.Arg("milestone", "The milestone name").Required().StringVar(&milestone)

	cmd := kingpin.Parse()

	// Initialize a client, with or without authentication.
	ctx := context.Background()
	var tc *http.Client
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		tc = oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token}))
	}
	client := github.NewClient(tc)

	// Engage!
	switch cmd {
	case cmdMilestone.FullCommand():
		createMilestone(ctx, client, owner, repo, since, to, milestone, forceMilestone)

	case cmdChangelog.FullCommand():
		changelog(ctx, os.Stdout, client, owner, repo, milestone, markdownLinks, skipLabels, true)

	case cmdRelease.FullCommand():
		buf := new(bytes.Buffer)
		changelog(ctx, buf, client, owner, repo, milestone, false, skipLabels, false)

		releaseName := milestone
		close := true
		pre := false
		if to != "" {
			releaseName = to
			close = false
			pre = true
		}
		createRelease(ctx, client, owner, repo, milestone, releaseName, close, pre, buf.String())
	}
}

func createMilestone(ctx context.Context, client *github.Client, owner, repo, since, to, milestone string, force bool) {
	stone, err := getMilestone(ctx, client, owner, repo, milestone)
	if err != nil {
		log.Println("Creating milestone", milestone)
		if !dryRun {
			stone = &github.Milestone{
				Title: github.String(milestone),
			}
			stone, _, err = client.Issues.CreateMilestone(ctx, owner, repo, stone)
			if err != nil {
				log.Println("Creating milestone:", err)
				os.Exit(1)
			}
		}
	}

	commits, err := listCommits(ctx, client, owner, repo, since, to)
	if err != nil {
		log.Println("Listing commits:", err)
		os.Exit(1)
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
}

func changelog(ctx context.Context, w io.Writer, client *github.Client, owner, repo, milestone string, markdownLinks bool, skipLabels []string, withSubject bool) {
	stone, err := getMilestone(ctx, client, owner, repo, milestone)
	if err != nil {
		log.Println("Getting milestone:", err)
		os.Exit(1)
	}

	opts := &github.IssueListByRepoOptions{
		Milestone: strconv.Itoa(stone.GetNumber()),
		State:     "all",
	}
	var issues []*github.Issue
	for {
		is, resp, err := client.Issues.ListByRepo(ctx, owner, repo, opts)
		if err != nil {
			log.Println("Listing issues:", err)
			os.Exit(1)
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
			fmt.Fprintf(w, "# [%s](https://github.com/%s/%s/releases/%s)\n\n", milestone, owner, repo, milestone)
		} else {
			fmt.Fprintf(w, "%s\n\n", milestone)
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
}

func createRelease(ctx context.Context, client *github.Client, owner, repo, milestone, name string, close, pre bool, changelog string) {
	stone, err := getMilestone(ctx, client, owner, repo, milestone)
	if err != nil {
		log.Println("Getting milestone:", err)
		os.Exit(1)
	}

	rel := &github.RepositoryRelease{
		Name:       github.String(name),
		TagName:    github.String(name),
		Body:       github.String(changelog),
		Prerelease: github.Bool(pre),
		Draft:      github.Bool(false),
	}
	if _, _, err := client.Repositories.CreateRelease(ctx, owner, repo, rel); err != nil {
		log.Println("Creating release:", err)
		os.Exit(1)
	}

	if close {
		_, _, err := client.Issues.EditMilestone(ctx, owner, repo, stone.GetNumber(), &github.Milestone{
			State: github.String("closed"),
		})
		if err != nil {
			fmt.Println("Closing milestone:", err)
			os.Exit(1)
		}
	}
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
