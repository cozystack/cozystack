#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for hack/check-commit-trailers.sh.
#
# Each test builds a throwaway git repository, writes commits with the message
# shape under test, and runs the checker over base..HEAD — the same range shape
# the workflow computes from the merge base. Real commits rather than a canned
# list, because merge commits and the trailer block are git's behaviour, not the
# checker's.
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` on its
# own line; there is no bats `run` or `$status`. Assertions are direct shell
# tests, and an expected failure is written as `if CHECK ...; then ... exit 1`.
# Each test runs in its own subshell, so the `cd` into the fixture does not leak.
# -----------------------------------------------------------------------------

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
CHECK="$REPO_ROOT/hack/check-commit-trailers.sh"

# A repository with one seed commit on `base`, left as the current directory.
# Signing and hooks are off so the fixture behaves the same on a maintainer's
# machine as on a runner: core.hooksPath is inherited from the global config,
# and a global commit-msg hook would otherwise rewrite the messages under test.
make_repo() {
    dir="$(mktemp -d)"
    cd "$dir"
    git init -q .
    git config user.email ci@example.invalid
    git config user.name CI
    git config commit.gpgsign false
    git config tag.gpgsign false
    git config core.hooksPath /dev/null
    echo seed > file
    git add file
    git commit -q -m "seed"
    git branch -f base HEAD
    git tag start
}

# Message on stdin. A merge test moves `base`, so ranges are taken from the
# `start` tag, which nothing moves.
commit() {
    echo change >> file
    git add file
    git commit -q -F -
}

# Last line of a test body rather than a trap, which the runners forbid: both
# set -e, so a failing test never reaches this and its repository survives for
# inspection, which is what a failed test wants.
cleanup() {
    cd /
    rm -rf "$dir"
}

# The three `git config` lines in make_repo are there because a contributor's
# global configuration reaches a fresh repository, and a runner carries no such
# configuration: a guard that goes missing stays green in CI and turns red only
# on the machine of whoever signs their commits or tags, which is how tag.gpgsign
# came to be missing in the first place. This test brings the hostile
# configuration with it, so the guards are held by the suite rather than by
# whichever machine happens to run it. gpg.program is a command that always
# fails, so an attempt to sign is a deterministic error rather than a question
# about which keys the host holds.
@test "make_repo holds against a hostile global configuration" {
    home="$(mktemp -d)"
    mkdir -p "$home/hooks"
    cat > "$home/hooks/commit-msg" <<'HOOK'
#!/bin/sh
echo "rewritten by a global hook" > "$1"
HOOK
    chmod +x "$home/hooks/commit-msg"
    cat > "$home/.gitconfig" <<CONFIG
[user]
    signingkey = 0000000000000000
[commit]
    gpgsign = true
[tag]
    gpgsign = true
[gpg]
    program = false
[core]
    hooksPath = $home/hooks
CONFIG
    export HOME="$home"
    export XDG_CONFIG_HOME="$home/.config"
    unset GIT_CONFIG_GLOBAL GIT_CONFIG_SYSTEM

    make_repo

    # tag.gpgsign: a signed tag is an object of its own, a lightweight one
    # resolves straight to the commit it names.
    if [ "$(git cat-file -t "$(git rev-parse start)")" != commit ]; then
        echo "expected start to be a lightweight tag" >&2
        exit 1
    fi
    # core.hooksPath: the global commit-msg hook would have rewritten the
    # message, and commit.gpgsign would have failed the commit outright.
    if [ "$(git log -1 --format=%s base)" != seed ]; then
        echo "expected the global commit-msg hook to be inert" >&2
        exit 1
    fi

    cleanup
    rm -rf "$home"
}

@test "accepts the documented Assisted-by: LLM trailer" {
    make_repo
    commit <<'EOF'
feat(ci): add a thing

Signed-off-by: CI <ci@example.invalid>
Assisted-by: LLM
EOF
    "$CHECK" base..HEAD
    cleanup
}

@test "accepts a commit with no trailer at all" {
    make_repo
    commit <<'EOF'
fix(ci): repair a thing

A body with no trailer block, because no model took part.
EOF
    "$CHECK" base..HEAD
    cleanup
}

@test "fails on an empty range instead of reporting OK" {
    # The pre-push recipe resolves origin/main..HEAD against the working
    # directory's HEAD, so an agent whose shell sits in a different checkout
    # lands on an empty range. A run that examined nothing must not read as a
    # clean one; grepping the message, not just the exit code, pins that the
    # verdict names the empty case rather than dropping the OK line by accident.
    make_repo
    if out="$("$CHECK" base..HEAD 2>&1)"; then
        echo "expected an empty range to fail" >&2
        exit 1
    fi
    printf '%s\n' "$out" | grep -q "empty"
    if printf '%s\n' "$out" | grep -q "Commit trailers OK"; then
        echo "an empty range still printed the OK verdict" >&2
        exit 1
    fi
    cleanup
}

@test "rejects a vendor-named attribution trailer and names the commit" {
    make_repo
    commit <<'EOF'
feat(ci): add a thing

Signed-off-by: CI <ci@example.invalid>
Assisted-By: Sonnet <noreply@example.invalid>
EOF
    sha="$(git rev-parse --short HEAD)"
    if out="$("$CHECK" base..HEAD 2>&1)"; then
        echo "expected a vendor-named trailer to fail" >&2
        exit 1
    fi
    printf '%s\n' "$out" | grep -q "$sha"
    printf '%s\n' "$out" | grep -q "must be exactly LLM"
    printf '%s\n' "$out" | grep -q "Assisted-by: LLM"
    cleanup
}

@test "rejects an attribution value that is not LLM even without a vendor name" {
    make_repo
    commit <<'EOF'
feat(ci): add a thing

Assisted-by: AI
EOF
    if "$CHECK" base..HEAD >/dev/null 2>&1; then
        echo "expected Assisted-by: AI to fail" >&2
        exit 1
    fi
    cleanup
}

@test "ignores the casing of the trailer key but not of the value" {
    make_repo
    commit <<'EOF'
feat(ci): add a thing

Assisted-By: LLM
EOF
    "$CHECK" base..HEAD
    commit <<'EOF'
feat(ci): add another thing

Assisted-by: llm
EOF
    if "$CHECK" base..HEAD >/dev/null 2>&1; then
        echo "expected a lowercase value to fail" >&2
        exit 1
    fi
    cleanup
}

@test "rejects two attribution trailers on one commit" {
    make_repo
    commit <<'EOF'
feat(ci): add a thing

Assisted-by: LLM
Assisted-by: LLM
EOF
    if out="$("$CHECK" base..HEAD 2>&1)"; then
        echo "expected a duplicated trailer to fail" >&2
        exit 1
    fi
    printf '%s\n' "$out" | grep -q "at most one is accepted"
    cleanup
}

@test "rejects a session trailer" {
    make_repo
    commit <<'EOF'
feat(ci): add a thing

Assisted-by: LLM
Claude-Session: https://example.invalid/s/abc123
EOF
    if out="$("$CHECK" base..HEAD 2>&1)"; then
        echo "expected a session trailer to fail" >&2
        exit 1
    fi
    printf '%s\n' "$out" | grep -q "session trailer is not accepted"
    cleanup
}

@test "rejects a link to an assistant session anywhere in the message" {
    make_repo
    commit <<'EOF'
feat(ci): add a thing

Worked out in https://claude.ai/chat/00000000-0000-0000-0000-000000000000
EOF
    if out="$("$CHECK" base..HEAD 2>&1)"; then
        echo "expected a session link to fail" >&2
        exit 1
    fi
    printf '%s\n' "$out" | grep -q "links to an assistant session"
    cleanup
}

@test "reads an assistant host as prose when it is not a URL" {
    make_repo
    commit <<'EOF'
docs(agents): say where the rule comes from

The trailer rule applies to every assistant, chatgpt.com included.
EOF
    "$CHECK" base..HEAD
    cleanup
}

@test "passes a merge commit that carries no trailer of its own" {
    make_repo
    git checkout -q -b side
    commit <<'EOF'
feat(ci): add a thing on a side branch

Assisted-by: LLM
EOF
    git checkout -q base
    commit <<'EOF'
fix(ci): move base along
EOF
    git merge -q --no-ff --no-edit side
    n="$(git rev-list --count start..HEAD)"
    [ "$n" -ge 3 ]
    git rev-list --min-parents=2 start..HEAD | grep -q .
    "$CHECK" start..HEAD
    cleanup
}

@test "rejects a bad trailer carried by a merge commit itself" {
    make_repo
    git checkout -q -b side
    commit <<'EOF'
feat(ci): add a thing on a side branch
EOF
    git checkout -q base
    git merge -q --no-ff -m "Merge branch 'side'

Assisted-By: Sonnet <noreply@example.invalid>" side
    if "$CHECK" start..HEAD >/dev/null 2>&1; then
        echo "expected a merge commit with a bad trailer to fail" >&2
        exit 1
    fi
    cleanup
}

@test "reports every offending commit in the range, not just the first" {
    make_repo
    commit <<'EOF'
feat(ci): first

Assisted-By: Sonnet <noreply@example.invalid>
EOF
    first="$(git rev-parse --short HEAD)"
    commit <<'EOF'
feat(ci): second

Assisted-By: Opus <noreply@example.invalid>
EOF
    second="$(git rev-parse --short HEAD)"
    if out="$("$CHECK" base..HEAD 2>&1)"; then
        echo "expected two bad commits to fail" >&2
        exit 1
    fi
    printf '%s\n' "$out" | grep -q "$first"
    printf '%s\n' "$out" | grep -q "$second"
    cleanup
}

@test "requires a range argument" {
    # Grepping the usage line, not just the exit code: with the guard removed
    # an empty range makes git fail anyway, so the status alone proves nothing.
    make_repo
    if out="$("$CHECK" 2>&1)"; then
        echo "expected a missing range to fail" >&2
        exit 1
    fi
    printf '%s\n' "$out" | grep -q '^usage: '
    cleanup
}

@test "fails on a range git cannot resolve instead of reporting success" {
    # A misspelled remote, or a clone that never fetched origin/main, is the
    # shape a developer meets when running this by hand. Walking zero commits
    # and printing OK would be the worst possible answer.
    make_repo
    if "$CHECK" no-such-ref..HEAD >/dev/null 2>&1; then
        echo "expected an unresolvable range to fail" >&2
        exit 1
    fi
    cleanup
}

@test "accepts the documented form in a message with CRLF line endings" {
    # Trailing whitespace is stripped from a message under git's default
    # cleanup, but --cleanup=verbatim keeps it, and so does a message written by
    # an editor on Windows. The carriage return must not turn LLM into LLM\r.
    make_repo
    echo change >> file
    git add file
    printf 'feat(ci): add a thing\r\n\r\nAssisted-by: LLM\r\n' \
        | git commit -q --cleanup=verbatim -F -
    git log -1 --format=%B | grep -q "$(printf 'LLM\r')"
    "$CHECK" start..HEAD
    cleanup
}

@test "rejects a continuation line folded into the attribution trailer" {
    # git reads the two lines as the single value "LLM continuation", which is
    # not LLM. Reading them as two lines would accept it.
    make_repo
    commit <<'EOF'
feat(ci): add a thing

Assisted-by: LLM
 continuation
EOF
    git log -1 --format=%B | git interpret-trailers --parse \
        | grep -qx 'Assisted-by: LLM continuation'
    if "$CHECK" start..HEAD >/dev/null 2>&1; then
        echo "expected a folded continuation to fail" >&2
        exit 1
    fi
    cleanup
}

@test "rejects a generation byline naming the tool" {
    make_repo
    commit <<'EOF'
feat(ci): add a thing

🤖 Generated with [Some Tool](https://example.invalid/tool)
EOF
    if out="$("$CHECK" start..HEAD 2>&1)"; then
        echo "expected a generation byline to fail" >&2
        exit 1
    fi
    printf '%s\n' "$out" | grep -q "generation byline"
    cleanup
}

@test "leaves ordinary prose about generated files alone" {
    # Both shapes are real lines from this repository's history, and both keep
    # their small g. Failing either one would make the byline rule unusable.
    make_repo
    commit <<'EOF'
feat(ci): add a thing

Regenerated with cozyvalues-gen v1.5.0, which the CI pin has moved past. The
README in packages/apps/redis was
generated with cozyvalues-gen 1.5.0 while CI pins 1.6.0, and the older
version orders the table differently.
EOF
    "$CHECK" start..HEAD
    cleanup
}

@test "leaves the trailer keys it does not judge alone" {
    # These block at review, and the script says why it does not decide them:
    # telling a model from a person in a Co-authored-by line needs a list of
    # model names, and Generated-by has never appeared here. A later change
    # that starts failing these is a scope change, not a fix.
    make_repo
    commit <<'EOF'
feat(ci): add a thing

Co-authored-by: Sonnet <noreply@example.invalid>
Generated-by: Sonnet
EOF
    "$CHECK" start..HEAD
    cleanup
}

@test "matches a session link whose scheme and host are uppercase" {
    make_repo
    commit <<'EOF'
feat(ci): add a thing

See HTTPS://CLAUDE.AI/chat/00000000-0000-0000-0000-000000000000
EOF
    if "$CHECK" start..HEAD >/dev/null 2>&1; then
        echo "expected an uppercase session link to fail" >&2
        exit 1
    fi
    cleanup
}

@test "leaves a matching host that is only userinfo alone" {
    # The host of this URL is somewhere.example, not chatgpt.com.
    make_repo
    commit <<'EOF'
feat(ci): add a thing

Reported at https://chatgpt.com@somewhere.example/path
EOF
    "$CHECK" start..HEAD
    cleanup
}

@test "matches a session link carrying userinfo of its own" {
    # Here the host really is claude.ai; user@ in front of it changes nothing.
    make_repo
    commit <<'EOF'
feat(ci): add a thing

Worked out in https://user@claude.ai/chat/00000000-0000-0000-0000-000000000000
EOF
    if "$CHECK" start..HEAD >/dev/null 2>&1; then
        echo "expected a session link with userinfo to fail" >&2
        exit 1
    fi
    cleanup
}

@test "reads a space before the separator as git does" {
    make_repo
    commit <<'EOF'
feat(ci): add a thing

Assisted-by : Sonnet <noreply@example.invalid>
EOF
    git log -1 --format=%B | git interpret-trailers --parse \
        | grep -q '^Assisted-by: Sonnet'
    if "$CHECK" start..HEAD >/dev/null 2>&1; then
        echo "expected a spaced key to fail" >&2
        exit 1
    fi
    cleanup
}

@test "leaves a documentation link on a vendor site alone" {
    # cursor.com and gemini.google.com serve manuals and pricing, not only
    # transcripts. A citation must not turn a pull request red.
    make_repo
    commit <<'EOF'
docs(agents): cite the tooling

Background at https://cursor.com/docs and https://gemini.google.com/about
EOF
    "$CHECK" start..HEAD
    cleanup
}

@test "leaves a lookalike host alone" {
    # notchatgpt.com and chatgpt.com.evil belong to somebody else. Matching a
    # substring of the host would hand them a failing check they cannot fix.
    make_repo
    commit <<'EOF'
feat(ci): add a thing

Reported at https://notchatgpt.com/x and https://chatgpt.com.evil/y
EOF
    "$CHECK" start..HEAD
    cleanup
}

@test "matches a session link served from a subdomain" {
    make_repo
    commit <<'EOF'
feat(ci): add a thing

See https://www.chatgpt.com/share/00000000-0000-0000-0000-000000000000
EOF
    if "$CHECK" start..HEAD >/dev/null 2>&1; then
        echo "expected a subdomain session link to fail" >&2
        exit 1
    fi
    cleanup
}
