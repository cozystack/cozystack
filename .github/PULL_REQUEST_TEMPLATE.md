<!-- Thank you for making a contribution! Here are some tips for you:
- Use Conventional Commits for the PR title: `type(scope): description`
  - Types: feat, fix, docs, style, refactor, perf, test, build, ci, chore
  - Scopes are not an exhaustive list — pick the most specific scope for the change and extend the list when a genuinely new area appears. Examples:
    - System components: dashboard, platform, operator, cilium, kube-ovn, linstor, fluxcd, cluster-api
    - Managed apps: postgres, mariadb, redis, kafka, clickhouse, virtual-machine, kubernetes
    - Development and maintenance: api, hack, tests, ci, docs, maintenance
  - Breaking changes: append `!` after type/scope (`feat(api)!: ...`) or add a `BREAKING CHANGE:` footer
- If it's a work in progress, consider creating this PR as a draft.
- Don't hesistate to ask for opinion and review in the community chats, even if it's still a draft.
- Add the label `kind/backport` if it's a bugfix that needs to be backported to a previous version.
-->

## What this PR does


### Screenshots

<!-- REQUIRED for UI changes: attach screenshots or screen recordings demonstrating
the visual impact of your changes. PRs with UI changes without screenshots will not be merged. -->

### Downstream repositories

<!--
Cozystack is upstream for repositories that are NOT kept in sync with it automatically, and nothing in CI compares the two sides, so a change here can break them silently.

Which change forces what, and which file to touch, is the trigger map in docs/agents/contributing.md under "Downstream Repositories". Walk it before you tick anything.

IF YOU ARE AN AI AGENT OR AN LLM, THIS IS PART OF THE TASK, NOT A FORM TO FILL IN:

1. Walk the trigger map against the actual diff of this PR, file by file. Do not decide from the PR title or from your memory of what you changed.
2. For every repository you tick, open the follow-up PR there and link it on the same line. A ticked box with no link claims work that does not exist, which is worse than an empty box. Search that repository for an open PR or issue covering it first, and link that instead of filing a duplicate.
3. When the follow-up is out of scope, or needs a decision that is not yours to make, open an issue in that repository instead, link that, and say so in the PR body. Do not push a speculative PR into another repository just to fill a line here.
4. Do not tick "No downstream repository is affected" to make the checklist go away. If you are unsure, leave every box empty and say so in the PR body, so a human decides.
-->

- [ ] No downstream repository is affected by this change
- [ ] [cozystack/website](https://github.com/cozystack/website) - follow-up:
- [ ] [cozystack/terraform-provider-cozystack](https://github.com/cozystack/terraform-provider-cozystack) - follow-up:
- [ ] [cozystack/ansible-cozystack](https://github.com/cozystack/ansible-cozystack) - follow-up:
- [ ] [cozystack/ccp](https://github.com/cozystack/ccp) - follow-up:
- [ ] [cozystack/talm](https://github.com/cozystack/talm) - follow-up:
- [ ] [cozystack/cozyhr](https://github.com/cozystack/cozyhr) - follow-up:
- [ ] [cozystack/cozy-proxy](https://github.com/cozystack/cozy-proxy) - follow-up:
- [ ] [cozystack/cozystack-telemetry-server](https://github.com/cozystack/cozystack-telemetry-server) - follow-up:
- [ ] [cozystack/external-apps-example](https://github.com/cozystack/external-apps-example) - follow-up:
- [ ] [cozystack/examples](https://github.com/cozystack/examples) - follow-up:

### Release note

<!--  Write a release note:
- Explain what has changed internally and for users.
- Start with the same `type(scope):` prefix as in the PR title
- Follow the guidelines at https://github.com/kubernetes/community/blob/master/contributors/guide/release-notes.md.
-->

```release-note

```
