---
name: Role nomination (Reviewer or Maintainer)
about: Propose promoting a contributor to Reviewer or Maintainer
title: 'Proposal: Promote @<github-handle> to <Reviewer|Maintainer>'
labels: 'community'
assignees: ''
---

<!--
Read CONTRIBUTOR_LADDER.md before filing: it defines both roles, their requirements and the privileges that come with them.

Anyone may open a nomination, including a self-nomination. Only maintainers vote.

This issue collects the discussion and the votes. The formal step is a separate pull request — CODEOWNERS for a Reviewer, MAINTAINERS.md for a Maintainer — opened after the vote passes.
-->

## Nominee

- **Name:**
- **GitHub:** @
- **Affiliation:**
- **Proposed role:** Reviewer / Maintainer
- **Area** (Reviewer only — the directories or component this covers):

## Why

<!-- What has this person actually done, and why does the project benefit from giving them this role? Cover both the work and the judgement: reviews given, issues triaged, questions answered, releases tested, discussions moved forward. Contributions that are not code count. -->

## Representative contributions

<!-- Link pull requests, reviews, issues or discussions. A handful of representative examples is more useful than an exhaustive list. Say which areas of the project they touch. -->

## Requirements

<!-- Tick what the nominee meets per CONTRIBUTOR_LADDER.md. If something is not met, say so and explain why the nomination still stands — an honest gap is fine, an unticked box that nobody mentions is not. -->

- [ ] Contributing for the period the ladder requires
- [ ] Sustained contribution record (PRs, reviews, issues, or an equivalent)
- [ ] Two sponsors, at least one from a different employer
- [ ] Demonstrated depth in the area they would own
- [ ] Supportive of new and occasional contributors

## Voting

Voting is open to current maintainers, listed in [MAINTAINERS.md](https://github.com/cozystack/cozystack/blob/main/MAINTAINERS.md). Vote in a comment with `+1` (approve), `0` (abstain) or `-1` (do not approve). A `-1` should come with a short reason, so the concern can be addressed.

The vote closes after **5 calendar days**, or once a majority of current maintainers have voted — whichever comes first. Passing requires a majority of current maintainers, per GOVERNANCE.md.

## Next steps if it passes

1. The nominee confirms in a comment that they accept the responsibilities of the role.
2. Open the pull request — add them to `.github/CODEOWNERS` for a Reviewer, or to `MAINTAINERS.md` for a Maintainer. Nominations are recorded through a PR, not a direct push to `main`.
3. Grant the GitHub permissions the role requires, and link the merged PR here before closing this issue.
