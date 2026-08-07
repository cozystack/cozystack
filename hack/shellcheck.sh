#!/bin/sh
set -eu

# Runs shellcheck over every shell script this repository owns and compares the
# result against hack/shellcheck-baseline.txt.
#
# Two things make this more than a `shellcheck **/*.sh`, and the tests in
# hack/shellcheck.bats pin both:
#
# Enumeration is a union of shebang and `.sh` suffix, and dropping either half
# loses files. What the run then reports never names the enumeration as the
# cause, and differs by half -- measured, not assumed. A script that leaves
# scope carrying baseline entries goes STALE; one carrying none, which is most
# of them, leaves in silence. Dropping the suffix branch also removes the
# sourced libraries from the command line, so the scripts that source them gain
# SC1091 as NEW. So the reader sees a fixed finding to regenerate away, or a new
# finding to go fix, and in neither case a narrower gate.
#
# The comparison is a ratchet in both directions: an unrecorded finding fails,
# and so does a baseline entry whose findings are gone. Without the second half
# a fixed script leaves a standing licence to reintroduce what was removed.
#
# Note for anyone editing these comments: shellcheck reads any comment whose
# first word is `shellcheck` as a directive, and a malformed one costs more than
# the SC1073 it prints -- analysis of that file STOPS there, so everything below
# goes unlinted while the run still looks like an ordinary finding. Measured: a
# parse error from a redirection recovers and reports the rest of the file, one
# from a directive does not. No script in scope has the second kind; keep it
# that way by never wrapping a line onto that word.
#
# Usage:
#   hack/shellcheck.sh              lint the tree, compare against the baseline
#   hack/shellcheck.sh --regenerate rewrite the baseline from the current tree
#   hack/shellcheck.sh --list       print the scripts the gate covers
#   hack/shellcheck.sh FILE...      lint exactly these files (used by the tests)
#
# The two do not combine into a partial refresh: `--regenerate FILE...` rewrites
# the whole baseline from those files alone and drops every other entry. Only
# the tests pass both, against a baseline of their own.
#
# Environment:
#   SHELLCHECK_BIN       shellcheck executable, default `shellcheck`
#   SHELLCHECK_BASELINE  baseline path, default hack/shellcheck-baseline.txt

# Pinned: shellcheck emits different findings across releases, so an unpinned
# binary turns the baseline into a version detector. The same version is
# installed by the unit-test job in .github/workflows/pull-requests.yaml. Move
# both together and regenerate the baseline in the same commit.
SHELLCHECK_PINNED_VERSION=0.11.0

SHELLCHECK_BIN=${SHELLCHECK_BIN:-shellcheck}
SHELLCHECK_BASELINE=${SHELLCHECK_BASELINE:-hack/shellcheck-baseline.txt}

REGENERATE=0
LIST_ONLY=0
case "${1:-}" in
--regenerate)
    REGENERATE=1
    shift
    ;;
--list)
    LIST_ONLY=1
    shift
    ;;
esac

# Every tracked file that is a shell script: a .sh suffix, or a shell shebang on
# the first line. Vendored upstream charts are excluded because `make update`
# regenerates them. The .bats arm is a guard rather than an exclusion: those
# files open with `#!/usr/bin/env bats`, which neither branch below matches, so
# dropping the arm changes nothing today. shellcheck does parse them -- it has a
# bats dialect and its own bats checks -- they are simply not baselined here.
#
# The list is newline separated, so a tracked filename containing a newline
# would be mis-split -- the one whitespace character reject_whitespace_paths
# below cannot see, because it is the separator. No such name exists in the
# tree. BATS_UNIT_FILES in the Makefile carries a related caveat about a
# different character: `$(wildcard)` splits its result on spaces.
#
# The existence check covers both branches. A tracked .sh deleted but not yet
# staged is still listed by git, and handing one to shellcheck exits 2, which
# this script correctly refuses to read as clean -- but the report is then the
# whole findings dump around one "does not exist", for an ordinary local state.
list_scripts() {
    git ls-files | while IFS= read -r f; do
        case "$f" in
        */charts/*) continue ;;
        *.bats) continue ;;
        esac
        [ -f "$f" ] || continue
        case "$f" in
        *.sh)
            printf '%s\n' "$f"
            continue
            ;;
        esac
        first=$(head -n 1 "$f" 2>/dev/null || true)
        case "$first" in
        '#!'*sh | '#!'*sh[!a-zA-Z0-9_]*) printf '%s\n' "$f" ;;
        esac
    done
}

if [ "$LIST_ONLY" -eq 1 ]; then
    list_scripts
    exit 0
fi

# A missing shellcheck is a failure, never a skip. A gate that quietly passes
# when its tool is absent is the exact shape of protection this script exists
# to replace.
if ! command -v "$SHELLCHECK_BIN" >/dev/null 2>&1; then
    echo "ERROR: shellcheck not found (SHELLCHECK_BIN=$SHELLCHECK_BIN)." >&2
    echo "Install v$SHELLCHECK_PINNED_VERSION from https://github.com/koalaman/shellcheck/releases" >&2
    exit 1
fi

found_version=$("$SHELLCHECK_BIN" --version 2>/dev/null | sed -n 's/^version: //p')
if [ "$found_version" != "$SHELLCHECK_PINNED_VERSION" ]; then
    echo "ERROR: shellcheck $SHELLCHECK_PINNED_VERSION is pinned, found '${found_version:-unknown}'." >&2
    echo "Findings differ between releases, so another version reports against a baseline not written for it." >&2
    exit 1
fi

# `path SCxxxx count`, sorted. Line and column numbers are dropped so an edit
# that shifts a line does not redden the ratchet; the count is what still
# catches a new finding in an already-listed script.
#
# The gap this leaves, stated so nobody has to rediscover it: one change that
# both removes and introduces a finding of the same check in the same script
# leaves the count equal and passes. Everything else is caught -- a new script, a
# check the script did not have, one more of a check it did -- and fixing a
# finding without adding one fails as STALE. Closing the last case means pinning
# identity to something inside the line, which puts the noise back; the diff is
# the reviewer's instrument there, not this file.
summarise() {
    sed -nE 's/^(.*):[0-9]+:[0-9]+: [a-z]+: .*\[(SC[0-9]+)\]$/\1 \2/p' |
        sort | uniq -c |
        awk '{ print $2, $3, $1 }' | sort
}

count_lines() {
    grep -c . || true
}

# A baseline line is `path SCxxxx count`, separated by spaces, so a path holding
# a space or a tab cannot be written down: the summary would split it into
# fields and record an entry naming something that is not the file. Refusing is
# the only honest handling of one, and refusing here is also what licenses the
# word splitting below -- the tree having no such name becomes a checked fact
# rather than a comment asserting it.
reject_whitespace_paths() {
    bad=$(printf '%s\n' "$1" | grep '[[:space:]]' || true)
    if [ -n "$bad" ]; then
        echo "ERROR: path contains whitespace, which a baseline entry cannot record:" >&2
        printf '%s\n' "$bad" >&2
        exit 1
    fi
}

# Explicit arguments are checked as given and left in "$@". Routing them through
# the same newline-joined string as the enumeration would split one on its own
# spaces before the check above ever saw them.
if [ "$#" -eq 0 ]; then
    FILES=$(list_scripts)
    if [ -z "$FILES" ]; then
        echo "ERROR: no shell scripts found to lint." >&2
        exit 1
    fi
    reject_whitespace_paths "$FILES"
    # Splitting is wanted here; globbing is not. Unquoted, a tracked name
    # holding [ or * would also be matched against the working directory,
    # which drops the real file and can count an unrelated one in its place.
    set -f
    # shellcheck disable=SC2086
    set -- $FILES
    set +f
else
    reject_whitespace_paths "$(printf '%s\n' "$@")"
fi

# Not piped through xargs, which reports a status of its own and so cannot tell
# findings from a failure to analyse: a child exiting 1 and a child exiting 2
# reach the caller as the same xargs status, and that distinction is the whole
# point of the check below. shellcheck
# exits 0 clean and 1 with findings; anything above means it could not do the
# job -- a file it could not read, an option it did not understand. Reporting
# "no drift" off a run that never analysed some of its inputs, or regenerating a
# baseline shrunk by the files it skipped, is exactly the silent pass this gate
# exists to remove.
RAW=$("$SHELLCHECK_BIN" --format=gcc "$@" 2>&1) && rc=0 || rc=$?
if [ "$rc" -gt 1 ]; then
    echo "ERROR: shellcheck exited $rc; some scripts were not analysed." >&2
    printf '%s\n' "$RAW" >&2
    exit 1
fi

CURRENT=$(printf '%s\n' "$RAW" | summarise)

if [ "$REGENERATE" -eq 1 ]; then
    {
        echo "# shellcheck baseline. Regenerate with: make shellcheck-baseline"
        echo "#"
        echo "# Generated $(date -u +%Y-%m-%d) against shellcheck $SHELLCHECK_PINNED_VERSION."
        echo "#"
        echo "# One line per (script, check) pair with the number of findings hack/shellcheck.sh"
        echo "# tolerates there today. The list is allowed to shrink and nothing else: a finding"
        echo "# it does not record fails the run, and so does an entry whose findings have been"
        echo "# fixed. Adding a line is a visible act in review, not a side effect of writing"
        echo "# new code."
        echo "#"
        echo "# Every shell script this repository owns is scanned, at shellcheck's default"
        echo "# severity: no file among them is skipped, and nothing is excluded on the"
        echo "# command line. In-file '# shellcheck disable=' directives are still honoured,"
        echo "# so a finding suppressed that way is absent from this list rather than"
        echo "# recorded in it."
        echo "#"
        echo "# Two classes sit outside the scan and are not represented here at all: the"
        echo "# vendored chart scripts under packages/*/*/charts/, which make update"
        echo "# regenerates, and the .bats files, whose bats shebang the enumeration does"
        echo "# not match. shellcheck can lint those with --shell=bats; they are not"
        echo "# baselined here."
        echo "#"
        echo "# These are the findings that predate the gate."
        printf '%s\n' "$CURRENT"
    } > "$SHELLCHECK_BASELINE"
    echo "Wrote $SHELLCHECK_BASELINE ($(printf '%s\n' "$CURRENT" | count_lines) entries)."
    exit 0
fi

if [ ! -f "$SHELLCHECK_BASELINE" ]; then
    echo "ERROR: baseline $SHELLCHECK_BASELINE not found. Create it with: make shellcheck-baseline" >&2
    exit 1
fi

BASE=$(grep -v '^[[:space:]]*#' "$SHELLCHECK_BASELINE" | grep . || true)

# Each stream is tagged rather than counted into awk, so an empty baseline and a
# clean tree are both ordinary inputs instead of edge cases in an offset.
DRIFT=$(
    {
        printf '%s\n' "$BASE" | sed 's/^/B /'
        printf '%s\n' "$CURRENT" | sed 's/^/C /'
    } | awk '
        NF == 4 && $1 == "B" { base[$2 " " $3] = $4 }
        NF == 4 && $1 == "C" { cur[$2 " " $3] = $4 }
        END {
            for (k in cur) {
                b = (k in base) ? base[k] : 0
                if (cur[k] > b)
                    printf "NEW %s %d %d\n", k, cur[k], b
                else if (cur[k] < b)
                    printf "STALE %s %d %d\n", k, cur[k], b
            }
            for (k in base)
                if (!(k in cur))
                    printf "STALE %s 0 %d\n", k, base[k]
        }
    ' | sort
)

if [ -z "$DRIFT" ]; then
    echo "shellcheck: $# scripts, $(printf '%s\n' "$BASE" | count_lines) baselined entries, no drift."
    exit 0
fi

echo "ERROR: shellcheck results do not match $SHELLCHECK_BASELINE:" >&2
printf '%s\n' "$DRIFT" |
    awk '{ printf "  %-6s %s %s: %s finding(s), baseline records %s\n", $1, $2, $3, $4, $5 }' >&2

NEW_FILES=$(printf '%s\n' "$DRIFT" | awk '$1 == "NEW" { print $2 }' | sort -u)
if [ -n "$NEW_FILES" ]; then
    echo >&2
    echo "Findings in the affected scripts:" >&2
    printf '%s\n' "$NEW_FILES" | while IFS= read -r f; do
        printf '%s\n' "$RAW" | grep -F "$f:" >&2 || true
    done
    echo >&2
    echo "Fix them, or -- where the construct is genuinely intended -- annotate the" >&2
    echo "line with a '# shellcheck disable=SCxxxx' carrying the reason." >&2
fi

# The regenerate advice is withheld while anything is NEW, even if something is
# also STALE. Printed together, the last line read wins, and regenerating a run
# that carries both writes the new findings into the baseline as well: the fix
# offered for one half launders the other, and the next run is green.
if printf '%s\n' "$DRIFT" | grep -q '^STALE'; then
    echo >&2
    echo "STALE means the baseline records findings the tree no longer has --" >&2
    echo "fixed, or in a script that was deleted or renamed." >&2
    if [ -n "$NEW_FILES" ]; then
        echo "Do not regenerate yet: this run also has NEW findings, which a" >&2
        echo "regenerate would record instead of reporting. Fix those first --" >&2
        echo "unless the NEW and STALE entries are the same findings under a" >&2
        echo "different path, which is a rename, and regenerating is correct." >&2
    else
        echo "Run: make shellcheck-baseline" >&2
    fi
fi

exit 1
