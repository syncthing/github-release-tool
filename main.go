package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"sort"
	"strconv"

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

	kingpin.Flag("owner", "Owner name (or set GRT_OWNER)").Envar("GRT_OWNER").Required().StringVar(&owner)
	kingpin.Flag("repo", "Repository name (or set GRT_REPO)").Envar("GRT_REPO").Required().StringVar(&repo)

	cmdMilestone := kingpin.Command("milestone", "Collect resolved issues into milestone")
	cmdMilestone.Flag("dry-run", "Don't do it, just report what would be done").BoolVar(&dryRun)
	cmdMilestone.Flag("from", "Start tag/commit").PlaceHolder("TAG/COMMIT").Required().StringVar(&since)
	cmdMilestone.Flag("to", "End tag/commit").Default("HEAD").StringVar(&to)
	cmdMilestone.Flag("skip-label", "Issue labels to skip").PlaceHolder("LABEL").Envar("GRT_SKIPLABELS").StringsVar(&skipLabels)
	argMilestoneMilestone := cmdMilestone.Arg("milestone", "The milestone name").Required().String()

	cmdChangelog := kingpin.Command("changelog", "Show changelog for milestone")
	argChangelogMilestone := cmdChangelog.Arg("milestone", "The milestone name").String()

	cmdRelease := kingpin.Command("release", "Create release from milestone")
	cmdRelease.Flag("dry-run", "Don't do it, just report what would be done").BoolVar(&dryRun)
	cmdRelease.Flag("to", "Release name/version (default is milestone name)").PlaceHolder("NAME").String()
	argReleaseMilestone := cmdRelease.Arg("milestone", "The milestone name").Required().String()

	cmd := kingpin.Parse()

	// Initialize a client.
	ctx := context.Background()
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: os.Getenv("GITHUB_TOKEN")},
	)
	tc := oauth2.NewClient(ctx, ts)
	client := github.NewClient(tc)

	// Engage!
	switch cmd {
	case cmdMilestone.FullCommand():
		milestone(client, owner, repo, since, to, *argMilestoneMilestone, skipLabels)

	case cmdChangelog.FullCommand():
		changelog(os.Stdout, client, owner, repo, *argChangelogMilestone)

	case cmdRelease.FullCommand():
		_ = argReleaseMilestone
	}
}

func milestone(client *github.Client, owner, repo, since, to, milestone string, skipLabels []string) {
	ctx := context.Background()
	stones, _, err := client.Issues.ListMilestones(ctx, owner, repo, &github.MilestoneListOptions{State: "all"})
	if err != nil {
		log.Println("Listing milestones:", err)
		os.Exit(1)
	}
	var stone *github.Milestone
	for _, stone = range stones {
		if stone.GetTitle() == milestone {
			log.Println("Found existing milestone")
			goto done
		}
	}

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

done:
	commits, err := listCommits(client, owner, repo, since, to)
	if err != nil {
		log.Println("Listing commits:", err)
		os.Exit(1)
	}

nextIssue:
	for _, fix := range getFixes(commits) {
		issue, _, err := client.Issues.Get(ctx, owner, repo, fix)
		if err != nil {
			log.Println("Getting issue:", err)
			continue
		}

		if issue.IsPullRequest() {
			continue nextIssue
		}

		labels := labels(issue)
		for _, skip := range skipLabels {
			if contains(skip, labels) {
				continue nextIssue
			}
		}

		if issue.Milestone != nil && issue.Milestone.GetNumber() == stone.GetNumber() {
			// It's already correctly set
			log.Println("Issue", fix, "is already correctly marked")
			continue
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

func changelog(w io.Writer, client *github.Client, owner, repo, milestone string) {
	ctx := context.Background()

	stones, _, err := client.Issues.ListMilestones(ctx, owner, repo, &github.MilestoneListOptions{State: "all"})
	if err != nil {
		log.Println("Listing milestones:", err)
		os.Exit(1)
	}

	var stone *github.Milestone
	for _, stone = range stones {
		if stone.GetTitle() == milestone {
			goto done
		}
	}

	log.Println("Milestone not found")
	os.Exit(1)

done:
	issues, _, err := client.Issues.ListByRepo(ctx, owner, repo, &github.IssueListByRepoOptions{
		Milestone: strconv.Itoa(stone.GetNumber()),
		State:     "all",
	})
	if err != nil {
		log.Println("Listing issues:", err)
		os.Exit(1)
	}

	sort.Slice(issues, func(a, b int) bool {
		return issues[a].GetNumber() < issues[b].GetNumber()
	})

	var bugs, features, other []*github.Issue
	for _, issue := range issues {
		labels := labels(issue)
		switch {
		case contains("bug", labels):
			bugs = append(bugs, issue)
		case contains("enhancement", labels):
			features = append(features, issue)
		default:
			other = append(other, issue)
		}
	}

	if len(bugs) > 0 {
		fmt.Fprintf(w, "Bugs:\n\n")
		printIssues(w, bugs)
		fmt.Fprintf(w, "\n")
	}
	if len(features) > 0 {
		fmt.Fprintf(w, "Enhancements:\n\n")
		printIssues(w, features)
		fmt.Fprintf(w, "\n")
	}
	if len(other) > 0 {
		fmt.Fprintf(w, "Other:\n\n")
		printIssues(w, other)
		fmt.Fprintf(w, "\n")
	}
}

func printIssues(w io.Writer, issues []*github.Issue) {
	for _, issue := range issues {
		fmt.Fprintf(w, " - #%d: %s\n", issue.GetNumber(), issue.GetTitle())
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

func nextMilestone(client *github.Client, owner, repo string) (string, error) {
	ctx := context.Background()
	stones, _, err := client.Issues.ListMilestones(ctx, owner, repo, nil)
	if err != nil {
		return "", err
	}
	for _, stone := range stones {
		if stone.GetState() != "open" {
			continue
		}
		fmt.Println(stone.GetTitle())
	}

	return "", nil
}

func lastRelease(client *github.Client, owner, repo string) (string, error) {
	ctx := context.Background()
	rel, _, err := client.Repositories.GetLatestRelease(ctx, owner, repo)
	if err != nil {
		return "", err
	}
	return rel.GetTagName(), nil
}

func listCommits(client *github.Client, owner, repo, since, to string) ([]github.RepositoryCommit, error) {
	ctx := context.Background()
	commits, _, err := client.Repositories.CompareCommits(ctx, owner, repo, since, to)
	if err != nil {
		return nil, err
	}
	return commits.Commits, nil
}

func getFixes(commits []github.RepositoryCommit) []int {
	fixesRe := regexp.MustCompile(`fixes #(\d+)`)
	var fixes []int
	seen := make(map[int]struct{})
	for _, commit := range commits {
		matches := fixesRe.FindAllStringSubmatch(commit.Commit.GetMessage(), -1)
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
	}
	sort.Ints(fixes)
	return fixes
}
