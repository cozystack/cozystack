## Changelogs

### Where the canonical process lives

[`agents/changelog.md`](../agents/changelog.md) is the source of truth. Read it end-to-end before generating; do not infer the process from past commits or memory. The CI runs it under Copilot via the reusable `changelog-generate.yaml` — called by `promote-rc.yaml::changelog` at promote time and by the optional `changelog-rc.yaml` at rc time — and, as a backstop, `tags.yaml::generate-changelog`; locally the same prompt is replayable by following the doc directly.

### Common changelog failure modes

These are mistakes that have shipped in real changelogs. Verify against each before merging the changelog PR.

**1. Commits from outside the release range.** Caused by running `git log <prev>..HEAD` from `main` while generating a patch changelog — `HEAD` on `main` contains everything that landed since the tag plus backports merged to `release-X.Y` after the tag was cut. Use `git log <prev>..<new_tag>` explicitly. The doc was updated to require this after v1.3.1's changelog shipped with 14 entries for what was actually a 1-commit release.

**2. Original + backport listed as two separate entries.** Each backport must coalesce with its original into a single entry of the form `(in #orig, backport #bp)`. To verify after generation:

```bash
# Every entry that mentions "backport" should also reference the original PR number
grep -E 'backport #[0-9]+' docs/changelogs/v<new>.md | grep -v '#[0-9]*, backport'
```

Output should be empty.

**3. Wrong PR author.** The squash-merge commit's author is whoever clicked "Merge" — not the person who wrote the code. **Always** resolve via `gh pr view <N> --json author --jq .author.login`, never `git log --format=%an`. This bites hardest for website-repo entries where the same merger handles most PRs.

**4. Superseding patch, stale changelog.** Tags are write-once, so a critical fix after `vX.Y.Z` ships as the next patch `vX.Y.(Z+1)` rather than moving the tag. If a changelog PR already exists for a tag you are superseding, the changelog work transfers to the new patch. Compare `git log <prev>..<new>` against what the existing `changelog-vX.Y.Z` branch already documents; only add/remove the deltas. If the only new commits are CI-internal or a revert of a feature that never reached a stable tag, no changelog edit is needed.

**5. PR numbers swapped inside prose.** The entry-format validator checks bullet entries but ignores Feature Highlights paragraphs and Upgrade Notes. Both have caused wrong PR references in shipped changelogs. Verify every `#NNNN` in prose with `gh pr view <N>`.

**6. Hallucinated entries.** Every entry must correspond to a commit in `git log <prev>..<new>`. Verify by extracting PR numbers from the file and grep-searching the commit range:

```bash
grep -oE '#[0-9]+' docs/changelogs/v<new>.md | sort -u | while read pr; do
  n=${pr#\#}
  if ! git log <prev>..<new> --grep="#${n}" --oneline | grep -q .; then
    echo "PR $pr in changelog but not in commit range"
  fi
done
```

**7. Bot accounts as human contributors.** `cozystack-ci[bot]`, `github-actions`, `dependabot`, `renovate`, and any `app/*` login must NOT appear in the `## Contributors` list. They legitimately appear in **per-entry attribution** (e.g. a Renovate-authored PR) — but not in the human roll-call.

**8. Title duplicated as description.** `* **fix(foo): X**: fix(foo): X (...)` means you wrote the conventional-commit subject twice and never wrote a user-facing description. The brief and the detail must say different things — the detail explains what the change means for users.

### Changelog pre-publish verification

Run these checks before merging the changelog PR. Catches problems in roughly the order they occur:

```bash
PREV=v1.2.0; NEW=v1.2.1

# A. Every chart bump is mentioned somewhere in the changelog
git diff --name-only $PREV..$NEW -- 'packages/*/charts/*/Chart.yaml' 'packages/*/*/charts/*/Chart.yaml' | while read f; do
  old=$(git show "$PREV:$f" 2>/dev/null | yq '.version // ""')
  new=$(git show "$NEW:$f" 2>/dev/null | yq '.version // ""')
  if [ "$old" != "$new" ]; then
    component=$(basename "$(dirname "$f")")
    if ! grep -qi "$component" docs/changelogs/$NEW.md; then
      echo "MISSING: $component $old -> $new ($f)"
    fi
  fi
done

# B. Out-of-Chart.yaml pins (Talos, tenant K8s, vendored images)
for path in packages/core/talos/images/talos/profiles/installer.yaml packages/core/installer/values.yaml packages/apps/kubernetes/values.yaml; do
  git diff $PREV..$NEW -- "$path" | head -40
done
git diff --name-only $PREV..$NEW -- 'images/*/Dockerfile'

# C. Every cited #NNNN exists and is a merged PR (not an issue)
grep -oE '#[0-9]+' docs/changelogs/$NEW.md | sort -u | sed 's/#//' | xargs -P 20 -I{} sh -c '
  state=$(gh api /repos/cozystack/cozystack/issues/{} --jq ".pull_request.merged_at // \"ISSUE\"" 2>/dev/null)
  echo "#{} $state"
' | grep -E 'ISSUE|null|^#[0-9]+ $'

# D. URL liveness
grep -oE 'https://[^)" ]+' docs/changelogs/$NEW.md | sort -u | xargs -P 10 -I{} sh -c '
  code=$(curl -s -o /dev/null -w "%{http_code}" {})
  if [ "$code" != "200" ]; then echo "$code {}"; fi
'

# E. Website PR authors are PR authors, not commit authors
grep -oE 'cozystack/website#[0-9]+' docs/changelogs/$NEW.md | sort -u | sed 's|cozystack/website#||' | while read n; do
  pr_author=$(gh pr view $n --repo cozystack/website --json author --jq .author.login)
  echo "website#$n -> @$pr_author"
done

# F. Working tree clean except for the changelog
git status --porcelain | grep -v "docs/changelogs/$NEW.md"
# Should output nothing.
```

Ship criterion: every chart bump surfaced, no issue-cited-as-PR, no 404 URLs, every website entry attributed via `gh pr view` (not commit author), `git status` shows only the changelog.

### Cozystack-specific URL quirks

`cozystack.io` paths are versioned: `/docs/operations/foo/` and `/docs/next/foo/` both 404. Only `/docs/vX.Y/foo/` and the unversioned canonical `/docs/foo/` serve content. AI-generated changelogs invent the unversioned form. Verify URLs with the `curl` loop above.

### Platform-component coverage

For a minor release (`vX.Y.0`), changelog entries should surface what an upstream chart bump actually brought in — features, breaking changes, security fixes. PR commit messages usually don't capture this; the upstream `CHANGELOG.md` or GitHub Release notes do. For each bumped `Chart.yaml`:

1. Read the `sources:` field — typically a GitHub URL.
2. Fetch release notes strictly inside `old < x <= new` via `gh release view <tag> --repo <owner>/<repo> --json body,name`.
3. Summarize in 2–5 user-impact bullets per component (new features, breaking changes, CVEs, important deprecations). Don't reproduce upstream notes verbatim; link to them.
4. If a PR-level entry already mentions the bump, enrich that entry with bullets — don't duplicate the entry.
5. If 3+ bumps have no PR-level mention, introduce a `## Platform Components` section.

If you can't find upstream notes for a bumped component, list it with version-only info and no bullets. Hallucinated bullets are worse than missing detail.
