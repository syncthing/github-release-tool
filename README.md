# GitHub Release Tool

## Workflow, General Idea

Commit code, close issues. At release time, decide on a new version. Gather
issues closed since that release and collect them into a milestone with the
new release version.

From this milestone, generate change log and releases (multiple, if there
are candidates). When done, close the milestone.

## Pre-requisites

- You use the issue labels `bug` and `enhancement`.
- You close issues by `fixes #1234` style annotations in the commit subject.
- You use semver-style versions.
- You have your GitHub token in the `GITHUB_TOKEN` env var when executing this command.

## Usage

```
usage: grt --owner=OWNER --repo=REPO [<flags>] <command> [<args> ...]

Flags:
  --help         Show context-sensitive help (also try --help-long and --help-man).
  --owner=OWNER  Owner name (or set GRT_OWNER)
  --repo=REPO    Repository name (or set GRT_REPO)

Commands:
  help [<command>...]
    Show help.


  milestone --from=TAG/COMMIT [<flags>] <milestone>
    Collect resolved issues into milestone

    --dry-run               Don't do it, just report what would be done
    --from=TAG/COMMIT       Start tag/commit
    --to="HEAD"             End tag/commit
    --skip-label=LABEL ...  Issue labels to skip

  changelog [<milestone>]
    Show changelog for milestone


  release [<flags>] <milestone>
    Create release from milestone

    --dry-run  Don't do it, just report what would be done
    --to=NAME  Release name/version (default is milestone name)
```

## Examples

### Create a new milestone

Let's say we released v0.14.40 a while back, it's now time to release a
candidate for v0.14.41. We start by gathering closed issues into the
milestone.

```
$ grt milestone --from=v0.14.40 v0.14.41
```

This lists all the commits since `v0.14.40` (the tag), looks for GitHub "...
fixes #1234" annotations in the commit subject, and grabs those issues. It
creates the milestone `v0.14.41` and adds them to it.

This means the milestone now contains the issues that should go into the
changelog, and the issues themselves show the release they will be in as
their milestone. This is useful for any user looking at the issue.

### View the changelog for a milestone

```
$ grt changelog v0.14.41
```

This gathers the issues in the mentioned milestone and prints a changelog,
grouped for bugs, enhancements and other issues. Use this for your tag
message, actual changelog file, or whatever.

### Release a version based on a milestone

It's time to put out a release candidate, `v0.14.41-rc.1`.

```
$ grt release v0.14.41 --to=v0.14.41-rc.1
```

This creates the `v0.14.41-rc.1` release, sets the description to the
changelog from above. Since the version is different from the milestone
name, the release is marked as a pre-release and the milestone is not closed.

At some later date, we release the tested version.

```
$ grt release v0.14.41
```

The same thing as above happens, except the milestone is closed and the
release is not marked pre-release.

