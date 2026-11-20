# Contributing to Cozystack

Welcome! We are glad that you want to contribute to our Cozystack project! 💖

As you get started, you are in the best position to give us feedbacks on areas of our project that we need help with, including:

* Problems found while setting up the development environment
* Gaps in our documentation
* Bugs in our GitHub actions

First, though, it is important that you read the [CNCF Code of Conduct](https://github.com/cncf/foundation/blob/master/code-of-conduct.md).

The guidelines below are a starting point. We don't want to limit your
creativity, passion, and initiative. If you think there's a better way, please
feel free to bring it up in a GitHub discussion, or open a pull request. We're
certain there are always better ways to do things, we just need to start some
constructive dialogue!

## Ways to contribute

We welcome many types of contributions including:

* New features
* Builds, CI/CD
* Bug fixes
* [Documentation](https://GitHub.com/cozystack/cozystack-website/tree/main)
* Issue Triage
* Answering questions on Slack or GitHub Discussions
* Web design
* Communications / Social Media / Blog Posts
* Events participation
* Release management

## Ask for Help

The best way to reach us with a question when contributing is to drop a line in
our [Telegram channel](https://t.me/cozystack), or start a new GitHub discussion.

## Where do I file?

Cozystack work is split across two repositories. Use this table to land in the right place:

| You have… | Where it goes |
|---|---|
| A **bug** | [New issue → Bug report](https://github.com/cozystack/cozystack/issues/new/choose) in this repo (`kind/bug`) |
| A **concrete, scoped feature** | [New issue → Feature request](https://github.com/cozystack/cozystack/issues/new/choose) in this repo (`kind/feature`) |
| A **usage question** or an early idea | [GitHub Discussions](https://github.com/cozystack/cozystack/discussions) |
| A **cross-cutting / architectural change** — affects multiple components or APIs, or needs a decision before code | A [design proposal](https://github.com/cozystack/community/tree/main/design-proposals) in the [**community**](https://github.com/cozystack/community) repo |
| **Governance, process, or community** matters | The [community](https://github.com/cozystack/community) repo |

Rule of thumb: **bugs and concrete features live here; "what or whether we should build it" lives in [community](https://github.com/cozystack/community)** as a design proposal or discussion. A large feature often starts as a Discussion, graduates to a design-proposal PR, then spawns implementation issues here.

## Raising Issues

When raising issues, please specify the following:

- A scenario where the issue occurred (with details on how to reproduce it)
- Errors and log messages that are displayed by the involved software
- Any other detail that might be useful

## Sign Off Your Commits (DCO)

Cozystack uses the [Developer Certificate of Origin](https://developercertificate.org/) (DCO) instead of a contributor licence agreement. By signing off a commit, you certify that you wrote the change or otherwise have the right to submit it under the project's open source licence.

Every commit in a pull request must carry a `Signed-off-by` trailer whose name and email match the commit author. Git adds it for you with `--signoff` (or `-s`):

```bash
git commit --signoff -m "fix(postgres): read the live Cluster when deciding the WAL-archive prefix"
```

If you forgot, add the sign-off to the last commit with `git commit --amend --signoff`, or to every commit on your branch with `git rebase --signoff main`, then force-push the branch.

The `DCO` check verifies this on every pull request and is required to pass before a pull request can be merged into `main`, together with `pre-commit`, `E2E Tests` and `CodeQL`.
