---
title: "Maintainer's Guide to Tide"
weight: 10
description: >
  
---

## Best practices

1. Don't let humans (or other bots) merge especially if tests have a long duration. Every merge invalidates currently running tests for that pool.
1. Try to limit the total number of queries that you configure. Individual queries can cover many repos and include many criteria without using additional API tokens, but separate queries each require additional API tokens.
1. Ensure that merge requirements configured in GitHub match the merge requirements configured for Tide. If the requirements differ, Tide may try to merge a PR that GitHub considers unmergeable.
1. If you are using the `lgtm` plugin and requiring the `lgtm` label for merge, don't make queries exclude the `needs-ok-to-test` label. The `lgtm` plugin triggers one round of testing when applied to an untrusted PR and removes the `lgtm` label if the PR changes so it indicates to Tide that the current version of the PR is considered trusted and can be retested safely.
1. Do not enable the "Require branches to be up to date before merging" GitHub setting for repos managed by Tide. This requires all PRs to be rebased before merge so that PRs are always simple fast-forwards. This is a simplistic way to ensure that PRs are tested against the most recent base branch commit, but Tide already provides this guarantee through a more sophisticated mechanism that does not force PR authors to rebase their PR whenever another PR merges first. Enabling this GH setting may cause unexpected Tide behavior, provides absolutely no benefit over Tide's natural behavior, and forces PR author's to needlessly rebase their PRs. Don't use it on Tide managed repos.

## Expected behavior that might seem strange

1. Any merge to a pool kicks all other PRs in the pool back into `Queued for retest`. This is because Tide requires PRs to be tested against the most recent base branch commit in order to be merged. When a merge occurs, the base branch updates so any existing or in-progress tests can no longer be used to qualify PRs for merge. All remaining PRs in the pool must be retested.
1. Waiting to merge a successful PR because a batch is pending. This is because Tide prioritizes batches over individual PRs and the previous point tells us that merging the individual PR would invalidate the pending batch. In this case Tide will wait for the batch to complete and will merge the individual PR only if the batch fails. If the batch succeeds, the batch is merged.
1. If the merge requirements for a pool change it may be necessary to "poke" or "bump" PRs to trigger an update on the PRs so that Tide will resync the status context. Alternatively, Tide can be restarted to resync all statuses.
1. Tide may merge a PR without retesting if the existing test results are already against the latest base branch commit.
1. It is possible for `tide` status contexts on PRs to temporarily differ from the Tide dashboard or Tide's behavior. This is because status contexts are updated asynchronously from the main Tide sync loop and have a separate rate limit and loop period.

## Troubleshooting
1. If Prow's PR dashboard indicates that a PR is ready to merge and it appears to meet all merge requirements, but the PR is being ignored by Tide, you may have encountered a rare bug with GitHub's search indexing. __TLDR: If this is the probelm, then any update to the PR (e.g. adding a comment) will make the PR visible to Tide again after a short delay.__
The longer explanation is that when GitHub's background jobs for search indexing PRs fail, the search index becomes corrupted and the search API will have some incorrect belief about the affected PR, e.g. that it is missing a required label or still has a forbidden one. This causes the search query Tide uses to identify the mergeable PRs to incorrectly omit the PR. Since the same search engine is used by both the API and GitHub's front end, you can confirm that the affected PR is not included in the query for mergeable PRs by using the appropriate "GitHub search link" from the expandable "Merge Requirements" section on the Tide status page. You can actually determine which particular index is corrupted by incrementally tweaking the query to remove requirements until the PR is included.
Any update to the PR causes GitHub to kick off a new search indexing job in the background. Once it completes, the corrupted index should be fixed and Tide will be able to see the PR again in query results, allowing Tide to resume processing the PR. It appears any update to the PR is sufficient to trigger reindexing so we typically just leave a comment. [Slack thread](https://kubernetes.slack.com/archives/C7J9RP96G/p1671494352250439) about an example of this.

## Other resources

- [Configuring Tide](/docs/components/core/tide/config/)
