#!/usr/bin/env bash
# Reject commit messages that break the attribution rules in
# docs/agents/contributing.md, "AI Agent Attribution":
#
#   - an attribution trailer (`Assisted-by:`, key matched case-insensitively)
#     carries the value `LLM` and nothing else, and appears at most once;
#   - no trailer names an assistant session, and no message links to one;
#   - no line is a generation byline naming the tool.
#
# All three are review blockers decided by the text alone, which is exactly the
# kind of thing a human should not be spending a review round on.
#
# Two shapes from that section are left to the reviewer, each for a measured
# reason rather than a guessed one:
#
#   - a model named in a `Co-authored-by:` line needs a list of model names to
#     tell from a person, and a list either misses the model released next month
#     or fires on somebody's colleague. Most of the 771 `Co-authored-by:` lines
#     on main name a model, so the list would decide a lot of pull requests;
#   - a `Generated-by:` trailer has never appeared here at all, and a rule with
#     no subject is a rule nobody maintains.
#
# A check that fires on something legitimate gets switched off, and then it
# guards nothing, so every rule above was run over the whole of main before it
# was written down — 6062 commits at the time, 1300 rejected, every one of them
# a true positive.
#
# Lines are read one at a time rather than through git's trailer parser, which
# reads only the last paragraph. A bad trailer with a paragraph after it is
# invisible to that parser and caught here. The cost is that a message quoting
# a rejected form as an example is rejected too.
#
# Usage: hack/check-commit-trailers.sh <range>   e.g. origin/main..HEAD
#
# Every commit in the range is checked as written; nothing is required of a
# commit, so a message with no trailer at all — a merge commit among them —
# passes. Only a trailer that exists and is wrong fails. An empty range fails:
# a check that examined nothing must not read as one that found nothing wrong.
set -euo pipefail

RANGE="${1:-}"
if [ -z "$RANGE" ]; then
  echo "usage: hack/check-commit-trailers.sh <range>" >&2
  exit 2
fi

# Hosts that serve transcripts rather than documentation. Of the three, only
# claude.ai has turned up in a commit message here so far, in two of them; the
# others are the same shape and cost nothing to carry. A vendor that also serves
# docs and pricing from its main domain is left out on purpose: a link to a
# manual is not a session, and a false red on a citation is a red nobody can
# argue with. The general shape is covered by the trailer key instead (any
# `*-Session:` key), which is how a session gets recorded in the first place.
#
# Matched as a URL only, and on a whole host: prose naming a host reads as
# prose, and notchatgpt.com and chatgpt.com.evil belong to somebody else. The
# closing class excludes a dot, so a bare host at the end of a sentence
# ("see https://claude.ai.") is not matched — the cost of keeping the lookalike
# hole shut. A real session link carries a path and matches on the slash.
# Lowercase, because the line is lowercased before the match. The optional
# leading group is userinfo, so https://user@claude.ai/x is still claude.ai;
# the closing class excludes "@" so that https://chatgpt.com@somewhere.example/x,
# whose actual host is somewhere.example, is not.
SESSION_URL='https?://([^/@[:space:]]*@)?([a-z0-9-]+\\.)*(claude\\.ai|chatgpt\\.com|chat\\.openai\\.com)([^a-z0-9.@-]|$)'

# Resolved up front rather than inside the `for` list: a command substitution
# there is not a command `set -e` looks at, so an unresolvable range would walk
# zero commits and report success.
SHAS="$(git rev-list "$RANGE")"

# An empty range is not a pass. The pre-push recipe in docs/agents/contributing.md
# resolves origin/main..HEAD against the working directory's HEAD, so an agent
# whose shell sits in a checkout at or behind the base resolves it to nothing
# here, and an "OK" over zero commits is a green nobody can tell from a real one.
# (A checkout on an unrelated branch resolves to that branch's commits instead,
# not to nothing — a wrong-branch check this guard does not claim to catch.) The CI
# path stays sound: it builds the range from the merge base and the head SHA of a
# pull request, which GitHub refuses to open with no commits between the two. It
# does go empty once the base branch has absorbed the head's commits, but only
# after the PR has nothing left to merge, where a red trailer check is harmless.
if [ -z "$SHAS" ]; then
  echo "check-commit-trailers: range '$RANGE' is empty; no commits were checked" >&2
  exit 1
fi

bad=0
for sha in $SHAS; do
  findings="$(
    git log -1 --format=%B "$sha" | awk -v url="$SESSION_URL" '
      {
        line = $0
        sub(/\r$/, "", line)
        # git folds an indented line into the value of the trailer above it, so
        # "Assisted-by: LLM" followed by " anything" is the value LLM anything.
        if (folds && line ~ /^[ \t]+[^ \t]/) {
          print "  " line "\n      continues the attribution trailer above; the value must be exactly LLM"
          next
        }
        folds = 0
        if (match(line, /^[^:]+:/)) {
          key = tolower(substr(line, 1, RLENGTH - 1))
          # git accepts a space before the separator and reads the key without
          # it, so "Assisted-by : x" is the same trailer as "Assisted-by: x".
          sub(/[ \t]+$/, "", key)
          val = substr(line, RLENGTH + 1)
          sub(/^[ \t]+/, "", val)
          sub(/[ \t]+$/, "", val)
          if (key == "assisted-by") {
            seen++
            folds = 1
            if (val != "LLM")
              print "  " line "\n      attribution trailer value must be exactly LLM"
          }
          if (key ~ /-session$/)
            print "  " line "\n      session trailer is not accepted"
        }
        # Lowercased for the match because a scheme and a host are
        # case-insensitive; the line is printed as written.
        if (tolower(line) ~ url)
          print "  " line "\n      links to an assistant session"
        # A generation byline, as opposed to an ordinary sentence: the first
        # letters on the line are a capitalised "Generated with", after any
        # emoji. That is 24 lines on main, all of them the same tool byline,
        # and no false positive — "Regenerated with cozyvalues-gen" and a
        # sentence wrapping onto "generated with ..." both keep their small g.
        if (line ~ /^[^A-Za-z]*Generated with /)
          print "  " line "\n      generation byline names the tool; use one Assisted-by: LLM trailer"
      }
      END {
        if (seen > 1)
          print "      " seen " attribution trailers; at most one is accepted"
      }
    '
  )"
  if [ -n "$findings" ]; then
    bad=$((bad + 1))
    git log -1 --format='%h %s' "$sha"
    printf '%s\n' "$findings"
  fi
done

if [ "$bad" -gt 0 ]; then
  cat >&2 <<'EOF'

Rejected commits above. Attribution discloses that a model took part; it does
not say which one, and it never carries a link to the session. The accepted
form is one line, next to Signed-off-by:

    Assisted-by: LLM

Reword with `git rebase --interactive` and force-push the branch.
See docs/agents/contributing.md, "AI Agent Attribution".
EOF
  exit 1
fi

echo "Commit trailers OK: $(git rev-list --count "$RANGE") commit(s) checked."
