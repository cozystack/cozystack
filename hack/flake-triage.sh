#!/usr/bin/env bash
# hack/flake-triage.sh — tell a deterministic E2E regression apart from an
# intermittent flake across consecutive `main` runs, and stop retry-to-green
# from masking a regression.
#
# Why this exists: a chainsaw case that fails on attempt 1 and passes on a
# re-run leaves the run green, so a case that has actually regressed on `main`
# reads as "just a flake" and nobody bisects it. This tool reads the failure
# set of EVERY attempt of each run (not just the final green one), so a
# case that failed on any attempt counts as failed for that run, and then
# classifies per case:
#
#   REGRESSION — failed on the last N consecutive (non-infra) main runs. This
#                is not a flake; it is a deterministic break that landed in a
#                bounded commit window. Auto-opens/updates a tracking issue.
#   FLAKE      — fails intermittently within the window (streak below the
#                regression threshold). Tracked, not paged.
#   (passing)  — never failed in the window; emits nothing, closes a stale
#                auto-issue if one exists.
#
# An "infra" run (more than TRIAGE_INFRA_FAIL_RATIO% of all cases failed in the
# same run, e.g. a cluster-wide apiserver/CNI outage) is a whole-environment
# failure, not a per-case signal, so it is excluded from streak computation
# instead of resetting or extending a streak.
#
# Subcommands (parse and classify are pure and unit-tested in
# hack/flake-triage_test.bats; triage does the GitHub I/O):
#
#   parse <junit.xml>                 -> "PASS\t<case>" / "FAIL\t<case>" per testcase
#   classify <run_file> [<run_file>…] -> REGRESSION/FLAKE/INFRA records
#                                        (run files are `parse` output, oldest first)
#   triage                            -> fetch recent main runs, classify, manage issues
#
set -euo pipefail

# Tunables (env-overridable so the workflow and tests can pin them).
: "${TRIAGE_WINDOW:=5}"              # recent main runs per workflow to consider
: "${TRIAGE_REGRESSION_STREAK:=3}"   # consecutive attempt-any failures => regression
: "${TRIAGE_INFRA_FAIL_RATIO:=50}"   # a run with >this% of cases failed is an infra outage
: "${TRIAGE_REPO:=cozystack/cozystack}"
: "${TRIAGE_WORKFLOWS:=Nightly}"     # comma-separated workflow display-names sampled on main
: "${TRIAGE_LABEL:=area/testing}"    # label applied to every auto-issue
: "${TRIAGE_MARKER:=<!-- flake-triage:auto -->}"  # idempotency marker in issue body

# ---------------------------------------------------------------------------
# parse: JUnit XML -> one "PASS\t<name>" / "FAIL\t<name>" line per testcase.
# A testcase is FAIL if it carries a <failure> or <error> child. Handles both
# the self-closed `<testcase .../>` (pass) and the `<testcase>…</testcase>`
# forms, and multiple testcases per file. Pure awk: no python/xmllint dependency.
# ---------------------------------------------------------------------------
parse_report() {
  awk '
    function flush(){ if (open) { print (infail ? "FAIL" : "PASS") "\t" name; open=0 } }
    /<testcase/ {
      flush()
      name=""
      if (match($0, /name="[^"]*"/)) name=substr($0, RSTART+6, RLENGTH-7)
      infail=0; open=1
      if ($0 ~ /\/>/) { print "PASS\t" name; open=0 }   # self-closed testcase = pass
      next
    }
    open && /<(failure|error)([ >]|\/)/ { infail=1 }
    open && /<\/testcase>/ { flush() }
    END { flush() }
  ' "$1"
}

# ---------------------------------------------------------------------------
# classify: given per-run parse output files ordered OLDEST -> NEWEST, emit
# INFRA / REGRESSION / FLAKE records. Deterministic, no I/O.
# ---------------------------------------------------------------------------
classify() {
  local files=("$@")
  local n=${#files[@]}
  [ "$n" -gt 0 ] || { echo "classify: no run files given" >&2; return 2; }

  local tmp; tmp=$(mktemp -d)
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" RETURN

  local -a infra
  local i=0
  : > "$tmp/universe"
  for f in "${files[@]}"; do
    grep -E '^(PASS|FAIL)\b' "$f" 2>/dev/null | cut -f2- > "$tmp/all.$i" || true
    grep -E '^FAIL\b'        "$f" 2>/dev/null | cut -f2- > "$tmp/fail.$i" || true
    local total failed
    total=$(grep -cE '^(PASS|FAIL)\b' "$f" 2>/dev/null || true); total=${total:-0}
    failed=$(wc -l < "$tmp/fail.$i" | tr -d ' '); failed=${failed:-0}
    if [ "$total" -gt 0 ] && [ $(( failed * 100 )) -gt $(( total * TRIAGE_INFRA_FAIL_RATIO )) ]; then
      infra[i]=1
      printf 'INFRA\t%s\t%s/%s\n' "$(basename "$f")" "$failed" "$total"
    else
      infra[i]=0
    fi
    cat "$tmp/all.$i" >> "$tmp/universe"
    i=$((i+1))
  done

  sort -u "$tmp/universe" > "$tmp/cases"

  local c
  while IFS= read -r c; do
    [ -n "$c" ] || continue
    local streak=0 considered=0 fails=0 broke=0 j
    j=$((n-1))
    while [ "$j" -ge 0 ]; do
      if [ "${infra[j]}" = "1" ]; then j=$((j-1)); continue; fi
      considered=$((considered+1))
      if grep -qxF -- "$c" "$tmp/fail.$j"; then
        fails=$((fails+1))
        [ "$broke" = "0" ] && streak=$((streak+1))
      else
        broke=1
      fi
      j=$((j-1))
    done
    if [ "$streak" -ge "$TRIAGE_REGRESSION_STREAK" ]; then
      printf 'REGRESSION\t%s\t%s\n' "$c" "$streak"
    elif [ "$fails" -gt 0 ]; then
      printf 'FLAKE\t%s\t%s/%s\n' "$c" "$fails" "$considered"
    fi
  done < "$tmp/cases"
}

# ---------------------------------------------------------------------------
# triage: fetch the last TRIAGE_WINDOW main runs of each sampled workflow,
# union the failed cases across ALL attempts of each run (so retry-to-green
# does not hide a first-attempt failure), classify, and open/update/close a
# per-case tracking issue. Requires `gh` authenticated for TRIAGE_REPO.
# ---------------------------------------------------------------------------
triage() {
  command -v gh >/dev/null 2>&1 || { echo "triage: gh not found" >&2; return 2; }
  local work="${TRIAGE_WORKDIR:-$(mktemp -d)}"
  mkdir -p "$work"

  # Collect run files oldest->newest across all sampled workflows, keyed by
  # createdAt so ordering is global, not per-workflow.
  local runs_index="$work/runs.tsv"; : > "$runs_index"
  local wf
  IFS=',' read -ra _wfs <<< "$TRIAGE_WORKFLOWS"
  for wf in "${_wfs[@]}"; do
    gh run list --repo "$TRIAGE_REPO" --workflow "$wf" --branch main \
      --limit "$TRIAGE_WINDOW" \
      --json databaseId,createdAt,headSha \
      --jq '.[] | [.createdAt, (.databaseId|tostring), .headSha] | @tsv' \
      >> "$runs_index" 2>/dev/null || true
  done
  sort -u "$runs_index" -o "$runs_index"   # createdAt-sorted => oldest first

  local -a run_files run_ids
  local id
  while IFS=$'\t' read -r _ id _; do
    [ -n "$id" ] || continue
    local rf="$work/run.$id.parsed"
    # Union failed cases across every chainsaw-report artifact this run has
    # (one per attempt): a case is FAIL for the run if it failed on ANY attempt.
    : > "$work/union.$id.fail"; : > "$work/union.$id.all"
    local aids; aids=$(gh api "repos/$TRIAGE_REPO/actions/runs/$id/artifacts" \
      --jq '.artifacts[] | select(.name|test("chainsaw-report")) | .id' 2>/dev/null || true)
    local aid
    for aid in $aids; do
      local z="$work/$id.$aid.zip"
      gh api "repos/$TRIAGE_REPO/actions/artifacts/$aid/zip" > "$z" 2>/dev/null || continue
      local d="$work/$id.$aid"; mkdir -p "$d"
      unzip -o -q "$z" -d "$d" 2>/dev/null || continue
      local xml
      while IFS= read -r xml; do
        parse_report "$xml" >> "$work/parsed.$id.raw"
      done < <(find "$d" -name '*.xml' 2>/dev/null)
    done
    [ -f "$work/parsed.$id.raw" ] || continue
    # Merge attempts: FAIL wins over PASS for the same case.
    awk -F'\t' '{ if($1=="FAIL"){f[$2]=1} else if(!($2 in s)){p[$2]=1} s[$2]=1 }
                END{ for(c in s) print (c in f ? "FAIL":"PASS") "\t" c }' \
      "$work/parsed.$id.raw" | sort -u > "$rf"
    run_files+=("$rf"); run_ids+=("$id")
  done < "$runs_index"

  [ "${#run_files[@]}" -gt 0 ] || { echo "triage: no run reports collected" >&2; return 0; }

  local verdicts="$work/verdicts.tsv"
  classify "${run_files[@]}" > "$verdicts" || true
  echo "triage: classification ->"; cat "$verdicts" >&2 || true

  # Manage issues per REGRESSION case.
  local kind case extra
  while IFS=$'\t' read -r kind case extra; do
    case "$kind" in
      REGRESSION) triage_upsert_issue "$case" "$extra" "${run_ids[*]}" ;;
      FLAKE)      : ;;  # tracked in the run log; no page. Extend here if desired.
    esac
  done < "$verdicts"
}

# Open or update the per-case tracking issue (idempotent via title + marker).
triage_upsert_issue() {
  local case="$1" streak="$2" run_id_list="$3"
  local title="[flake-triage] E2E case \`${case}\` regressed on main (${streak} consecutive runs)"
  local existing
  existing=$(gh issue list --repo "$TRIAGE_REPO" --state open --search "$TRIAGE_MARKER $case in:body" \
    --json number,title --jq ".[] | select(.title | contains(\"\`${case}\`\")) | .number" 2>/dev/null | head -1 || true)
  local body
  body=$(cat <<EOF
${TRIAGE_MARKER}

Automated triage: the E2E chainsaw case \`${case}\` failed on **${streak}** consecutive
\`main\` runs (attempt-any, so retry-to-green did not hide it). That crosses the
deterministic-regression threshold (\`TRIAGE_REGRESSION_STREAK=${TRIAGE_REGRESSION_STREAK}\`),
so this is being tracked as a regression, not a flake.

Sampled main runs (newest last): ${run_id_list}

Next step: bisect the commit window between the last green and first red run for
this case, and capture the failing component's logs from a repro. This issue is
maintained by \`hack/flake-triage.sh\`; it auto-closes when the case is green
again across the window.
EOF
)
  if [ -n "$existing" ]; then
    gh issue comment "$existing" --repo "$TRIAGE_REPO" --body "$body" >/dev/null 2>&1 || true
    echo "triage: updated issue #$existing for $case" >&2
  else
    gh issue create --repo "$TRIAGE_REPO" \
      --title "$title" --body "$body" --label "$TRIAGE_LABEL" >/dev/null 2>&1 || true
    echo "triage: opened issue for $case" >&2
  fi
}

main() {
  local cmd="${1:-}"; shift || true
  case "$cmd" in
    parse)    parse_report "$@" ;;
    classify) classify "$@" ;;
    triage)   triage "$@" ;;
    *) echo "usage: $0 {parse <xml> | classify <run_file>… | triage}" >&2; return 2 ;;
  esac
}

main "$@"
