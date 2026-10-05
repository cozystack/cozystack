---
name: Role nomination (Reviewer or Maintainer)
about: Propose promoting a contributor to Reviewer or Maintainer
title: 'Proposal: Promote @<github-handle> to <Reviewer|Maintainer>'
labels: 'community'
assignees: ''
---

<!--
Read CONTRIBUTOR_LADDER.md before filing: it defines both roles, their requirements, the privileges that come with them, and how each promotion is approved.

Who may open this issue:
- Reviewer: anyone who can make the case; the ladder does not restrict who nominates.
- Maintainer: a current Maintainer, and the nominee must be a current Reviewer.

This issue collects the discussion. The formal step is a separate pull request, opened once there is support for it. Delete the sections that do not apply to the proposed role.
-->

## Nominee

- **Name:**
- **GitHub:** @
- **Affiliation:**
- **Proposed role:** Reviewer / Maintainer
- **Area** (Reviewer only — the directories or component this covers):
- **Sponsors** (two current Reviewers or Maintainers; for a Maintainer nomination, the sponsors of their Reviewer promotion): @, @

## Why

<!-- What has this person actually done, and why does the project benefit from giving them this role? Cover both the work and the judgement: reviews given, issues triaged, questions answered, releases tested, discussions moved forward. Contributions that are not code count. -->

## Representative contributions

<!-- Link pull requests, reviews, issues or discussions. A handful of representative examples is more useful than an exhaustive list. Say which areas of the project they touch. -->

## Requirements

<!-- Tick what the nominee meets per CONTRIBUTOR_LADDER.md. If something is not met, say so and explain why the nomination still stands — an honest gap is fine, an unticked box that nobody mentions is not. -->

Reviewer:

- [ ] Contributing for at least 6 months, and actively contributing to at least one project area
- [ ] Successful contributions: 10 accepted PRs, 20 reviewed PRs, 20 resolved issues, ownership of a key project management area, or an equivalent combination
- [ ] Has reviewed, or helped review, at least 20 pull requests
- [ ] Two sponsors who are themselves Reviewers or Maintainers, at least one of whom does not work for the same employer
- [ ] In-depth knowledge of the area, including having analysed and resolved test failures there
- [ ] Commits to being responsible for that area
- [ ] Supportive of new and occasional contributors, and helps get useful PRs in shape to commit

Maintainer (in addition to the Reviewer requirements):

- [ ] Reviewer for at least 6 months
- [ ] Broad knowledge of the project across multiple areas
- [ ] Exercises judgement for the good of the project, independent of employer, friends or team
- [ ] Mentors other contributors
- [ ] Can commit at least 10 hours per month to the project

## Approval

**Reviewer.** The promotion is approved on the pull request: at least two members of the team that owns the repository or directory, who are already Approvers, approve it (`CONTRIBUTOR_LADDER.md`).

**Maintainer.** Current maintainers, listed in [MAINTAINERS.md](https://github.com/cozystack/cozystack/blob/main/MAINTAINERS.md), vote in a comment on this issue with `+1` (approve), `0` (abstain) or `-1` (do not approve). A `-1` should come with a short reason, so the concern can be addressed. The vote is the discussion; the decision is the promotion pull request, which a majority of the current maintainers must approve (`CONTRIBUTOR_LADDER.md`). As in #2343 and #2344, the vote stays open for **5 calendar days**, or until a majority of current maintainers have voted, whichever comes first.

## Next steps once there is support

Reviewer:

1. Open the pull request adding the nominee to `.github/CODEOWNERS` for their area and to the Reviewers section of `MAINTAINERS.md`, and link it here.
2. Grant the nominee write access before the pull request is merged: a `CODEOWNERS` entry for a handle without write access is silently ignored. Once it is merged, close this issue.

Maintainer:

1. Open the pull request adding the nominee to the Active Maintainers section of `MAINTAINERS.md`, and link it here.
2. The nominee comments on that pull request that they agree to all requirements of becoming a Maintainer.
3. Merge once a majority of current maintainers have approved, grant the GitHub permissions the role requires, and close this issue.

Roles are recorded through a pull request, not a direct push to `main`.
