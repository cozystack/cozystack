#!/usr/bin/env bash
# hack/flake-triage.sh — tell a deterministic E2E regression apart from an
# intermittent flake across consecutive `main` runs.
#
# Why this exists: a case that has actually regressed on `main` reads as "just a
# flake" and nobody bisects it (cozystack/cozystack#3229 sat red on main for
# three consecutive nightlies while treated as noise). Per case:
#
#   REGRESSION — failed on the last N consecutive (non-infra) main runs. This
#                is not a flake; it is a deterministic break that landed in a
#                bounded commit window. Auto-opens/updates a tracking issue, and
#                auto-closes it once the case stops regressing across the window.
#   FLAKE      — fails intermittently within the window (streak below the
#                regression threshold). Tracked in the run log, not paged.
#   (passing)  — never failed in the window; emits nothing.
#
# The robust core signal is the streak across separate `main` runs (distinct
# run ids, e.g. successive nightlies): #3229 was three separate nightly runs, so
# a window of TRIAGE_WINDOW runs catches it regardless of within-run retries.
# As an additional best-effort defence against within-run retry-to-green
# (attempt 1 red, a re-run of the failed job green under the SAME run id), the
# triage step also unions the failed set across every `chainsaw-report` artifact
# a run retains, so a first-attempt failure still counts for that run WHEN
# GitHub keeps a per-attempt artifact. That per-attempt retention is a best-
# effort enhancement, not a correctness dependency: the cross-run streak stands
# on its own if only the latest attempt's artifact survives.
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
      # Anchor on a leading space so `classname="…"` (no space before `name`)
      # cannot be matched instead of the real `name="…"` attribute.
      if (match($0, / name="[^"]*"/)) name=substr($0, RSTART+7, RLENGTH-8)
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
    awk -F'\t' '{ if($1=="FAIL"){f[$2]=1} s[$2]=1 }
                END{ for(c in s) print (c in f ? "FAIL":"PASS") "\t" c }' \
      "$work/parsed.$id.raw" | sort -u > "$rf"
    run_files+=("$rf"); run_ids+=("$id")
  done < "$runs_index"

  [ "${#run_files[@]}" -gt 0 ] || { echo "triage: no run reports collected" >&2; return 0; }

  local verdicts="$work/verdicts.tsv"
  classify "${run_files[@]}" > "$verdicts" || true
  echo "triage: classification ->"; cat "$verdicts" >&2 || true

  # Manage issues: open/update for REGRESSION cases; then close any open
  # auto-issue whose case is no longer regressing across the window.
  local regressed="$work/regressed"; : > "$regressed"
  local kind case extra
  while IFS=$'\t' read -r kind case extra; do
    case "$kind" in
      REGRESSION) printf '%s\n' "$case" >> "$regressed"
                  triage_upsert_issue "$case" "$extra" "${run_ids[*]}" ;;
      FLAKE)      : ;;  # tracked in the run log; no page. Extend here if desired.
    esac
  done < "$verdicts"

  triage_close_resolved "$regressed"
}

# List every open auto-issue (matched by the "[flake-triage]" title prefix) and
# close the ones whose case is no longer in the current REGRESSION set, so a
# fixed regression does not leave its tracker open forever.
triage_close_resolved() {
  local regressed="$1"
  local n title c
  while IFS=$'\t' read -r n title; do
    [ -n "$n" ] || continue
    c=$(printf '%s' "$title" | sed -E 's/.*`([^`]*)`.*/\1/')   # case is backtick-quoted in the title
    [ -n "$c" ] || continue
    if ! grep -qxF -- "$c" "$regressed" 2>/dev/null; then
      gh issue close "$n" --repo "$TRIAGE_REPO" \
        --comment "Auto-closing: \`${c}\` is no longer classified as a regression across the last ${TRIAGE_WINDOW} \`main\` runs (now passing or only intermittent). It reopens automatically if it regresses again. ${TRIAGE_MARKER}" \
        >/dev/null 2>&1 || true
      echo "triage: closed resolved issue #$n for $c" >&2
    fi
  done < <(gh issue list --repo "$TRIAGE_REPO" --state open \
            --search 'flake-triage in:title' --limit 200 \
            --json number,title \
            --jq '.[] | select(.title|startswith("[flake-triage]")) | [(.number|tostring), .title] | @tsv' \
            2>/dev/null || true)
}

# Open or update the per-case tracking issue. Idempotency keys on the title
# prefix + the backtick-quoted case name (robust: GitHub full-text search
# tokenizes punctuation away, so a body-marker search is unreliable).
triage_upsert_issue() {
  local case="$1" streak="$2" run_id_list="$3"
  local title="[flake-triage] E2E case \`${case}\` regressed on main (${streak} consecutive runs)"
  local existing
  existing=$(gh issue list --repo "$TRIAGE_REPO" --state open --search 'flake-triage in:title' \
    --limit 200 --json number,title \
    --jq ".[] | select((.title|startswith(\"[flake-triage]\")) and (.title|contains(\"\`${case}\`\"))) | .number" \
    2>/dev/null | head -1 || true)
  local body
  body=$(cat <<EOF
${TRIAGE_MARKER}

Automated triage: the E2E chainsaw case \`${case}\` failed on **${streak}** consecutive
\`main\` runs, which crosses the deterministic-regression threshold
(\`TRIAGE_REGRESSION_STREAK=${TRIAGE_REGRESSION_STREAK}\`), so it is tracked as a
regression, not a flake.

Sampled main runs (newest last): ${run_id_list}

Next step: bisect the commit window between the last green and first red run for
this case, and capture the failing component's logs from a repro. This issue is
maintained by \`hack/flake-triage.sh\`; it auto-closes once the case stops
regressing across the last ${TRIAGE_WINDOW} \`main\` runs.
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
