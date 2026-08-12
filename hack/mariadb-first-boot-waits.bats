#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Contract: nothing that waits for a mariadb HelmRelease to become Ready may
# allow less than the mariadb chart itself allows a first boot. Both the backup
# walkthrough and the chainsaw suite carry such a wait, and both are checked.
#
# Why the two numbers are related at all. helm-controller leaves
# spec.waitStrategy unset for these releases, and its default is `poller`, so
# the install action waits through kstatus. kstatus has no type-specific rules
# for k8s.mariadb.com/MariaDB, so it falls through to its Ready-condition
# fallback, which maps a Ready condition of False onto InProgress. The operator
# holds Ready=False with reason StatefulSetNotReady until the StatefulSet is
# fully ready, so `helm install` -- and therefore HelmRelease readiness -- MAY
# span a first boot. A wait shorter than that fails runs the probe was still
# willing to wait for.
#
# "May", not "does": a poll landing before the operator's first status write
# reads a condition-less CR as Current and can end the wait early, which is why
# the suite keeps an endpoint assert after the HR-Ready one. The doc section and
# the suite comment both carry that qualifier; this header is the contract
# document, so it is the worst of the three places to lose it. No number moves
# either way -- the install-timeout floor binds regardless -- but the contract
# should not claim more than the mechanism gives.
#
# The comparison is strictly greater, not at-least. Clearing the startup budget
# exactly is not enough on its own: the readiness probe the operator installs
# has to pass afterwards, and the artifact fetch, the operator reconcile, the
# PVC bind and the image pull all sit ahead of it. Those are not derivable here,
# so the budget is the floor a wait must beat rather than a figure it may match.
#
# The budget is one boot, not one per replica: mariadb-operator hardcodes
# PodManagementPolicy: Parallel on the StatefulSet it builds
# (pkg/builder/statefulset_builder.go), so the replicas boot concurrently.
# That claim is about the operator and cannot be checked from this tree, so it
# is worth knowing the arithmetic survives it being wrong: under OrderedReady
# the two replicas would serialize to 620s, the floor would become 620 rather
# than the install timeout, and 660 still clears it. The claim decides the
# explanation here, not the numbers.
#
# The budget is derived here rather than read as a literal: failureThreshold
# comes from the chart, and initialDelaySeconds=20 / periodSeconds=10 are the
# operator's own probe defaults, which the chart deliberately does not
# override. Both probe shapes carry those two numbers, so the arithmetic holds
# whichever the operator picks: replicas>1 renders replication, which is HA, so
# the startup probe becomes the agent's HTTP one (mariadbAgentProbe), while a
# single replica keeps the exec probe (defaultProbe) -- both in
# pkg/builder/container_builder.go, and this chart renders both shapes.
# Deriving the budget rather than pinning it means the contract keeps
# describing something after the chart moves. It does not mean any change to
# failureThreshold is caught: the floor is the LARGER of that budget and the
# release's install timeout, and today the install timeout is the one that
# binds, so the budget half only starts deciding once it grows past it.
#
# Nor is the other way the install timeout could move. cozystack-api accepts
# --helmrelease-install-timeout too (pkg/cmd/server/start.go), so a manifest
# could in principle pass it and make the compiled default below irrelevant.
# No manifest does: the only deployment that passes the flag is
# packages/core/installer/templates/cozystack-operator.yaml, and it passes it to
# cozystack-operator, which stamps a different set of releases. The derivation
# is sound today because of that, and it is recorded here so the next reader
# does not have to re-derive it -- but nothing here would notice if a manifest
# started passing it to cozystack-api.
#
# Nor are the other ways the premise could stop holding. The derivation assumes
# the install polls through kstatus, and two settings would stop it. One is a
# spec.release.waitStrategy of `legacy` on mariadb-rd, which routes the wait
# through helm's own logic instead. The other is outside the package entirely:
# the UseHelm3Defaults feature gate flips helm-controller's own default to
# `legacy` for every release at once, and helm-controller here runs with
# --feature-gates=ExternalArtifact=true and nothing else. Nothing else stops
# the polling, and healthCheckExprs in particular does not: pkg/config's
# ResolveWaitStrategy returns nil only when strategy and exprs are both absent,
# but exprs alone resolve to `poller`, which is kstatus again. Returning nil and
# polling are different questions, and only the second one this contract rests
# on. No ApplicationDefinition in the tree sets a strategy at all; the two
# places that set one, packages/extra/monitoring and packages/extra/seaweedfs,
# are hand-written HelmRelease templates and both set `poller`. Both levers are
# unguarded deliberately: either appearing would make the raised numbers slack
# rather than short, so the failure direction is safe, and a guard whose breach
# cannot under-budget is not worth the extra coupling.
#
# Reach beyond the name: one test here also covers
# examples/backups/postgres/00-helpers.sh, because that copy carries the same
# timeout-branch code and nothing else in the tree exercises it, and it pins the
# two wait_hr_ready bodies byte-identical. Everything else in this file is about
# mariadb only, which is why the filename says so.
#
# What is NOT guarded: an operator bump that moves those two probe defaults
# drifts this arithmetic and the chart's own comment together, and neither
# would notice. Nor is the suite extractor's step boundary -- it treats any
# `- name:` line as closing a step, at any indent, since the pattern does not
# anchor the indent it matches. What makes that right today is not alignment
# but absence: no nested `- name:` list item appears ahead of the timeout in
# these steps. An op carrying its own name key inside one would close the step
# early and report a timed step as unset.
#
# Nor is a timed non-HelmRelease assert placed AHEAD of the HelmRelease one
# inside a wait-helmrelease-ready step. The extractor takes the first timeout in
# the step, which is right because each such step holds exactly one assert
# today; a timed Service or Endpoints assert inserted before it would be read
# instead, and the HelmRelease budget could then drop back under its floor with
# this file still green. Requiring the timeout to be followed by
# `kind: HelmRelease` would close it and would also couple the extractor to the
# assert's body, which is the trade being declined.
#
# Nor is an HR-Ready assert placed in a step NOT named wait-helmrelease-ready.
# The suite extractor selects by that step name, so such an assert is invisible
# to it, and the "at least two" count below still passes on the two steps that
# do carry the name -- the new one is simply never examined. Selecting by step
# name is what makes the extractor readable, and widening it to every assert in
# the file would pull in the Endpoints and StatefulSet ones, which have their
# own unrelated budgets. The gap is accepted, not overlooked.
#
# The relationship between these waits and the Chainsaw op that contains them is
# pinned by two tests, over two different quantities, and knowing which is which
# is the point. The op was raised by exactly the 720s the two waits gained,
# which is what keeps a late failure reported rather than SIGKILLed with the
# process group.
#
# The first holds the op's REMAINDER over the ceilings up to and including the
# SOURCE wait -- what the op must contain before the source instance can fail at
# all. The second holds the op minus BOTH mariadb waits -- what is left for the
# applies, the dump, the restore and the verifies once the waits have taken
# their ceilings. Two tests rather than one widened test, because extending the
# prefix past the source wait would sum ceilings the script reaches only on the
# happy path, which is neither quantity.
#
# Both sit at exactly their constant, so raising any mariadb wait, or any
# ceiling ahead of the source, reds unless the op rises in the same change.
# Raising the waits and the op together passes, which is the change that should
# pass.
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` on its
# own line; there is no bats `run` or `$status`, and no setup/teardown.
# Assertions are direct shell tests that exit non-zero. A `}` at column zero
# gets `return 0` inserted ahead of it, so the helpers below report through
# stdout only and never through an exit status.
#
# Run with: hack/cozytest.sh hack/mariadb-first-boot-waits.bats
# -----------------------------------------------------------------------------

MBW_SCRIPT="examples/backups/mariadb/run-all.sh"
MBW_CHART="packages/apps/mariadb/templates/mariadb.yaml"
MBW_SUITE="hack/e2e-chainsaw/mariadb/chainsaw-test.yaml"
MBW_APISERVER="pkg/cmd/server/start.go"
MBW_RD="packages/system/mariadb-rd/cozyrds/mariadb.yaml"
# Seconds the round-trip op must keep free after the ceilings ahead of the
# source wait. Ratchet on the figure the suite comment states, not a derived
# bound: raise it deliberately, never let it fall.
MBW_OP_REMAINDER=960
# How many ceilings that prefix is made of: bucket release, two bucket fields,
# source wait. Pinned exactly, because both ways of losing one shrink the sum
# and GROW the remainder, which is the loosening direction.
MBW_PREFIX_CEILINGS=4
# Seconds the round-trip op must keep free once BOTH mariadb waits are paid.
# The remainder above covers only the prefix, which ends at the source wait, so
# on its own it lets the target wait grow without limit: raising it spends the
# op's slack and reopens the SIGKILL band the op was raised to close, with
# every test still green. Ratchet, like the one above -- raise deliberately,
# never let it fall.
MBW_OP_MINUS_WAITS=1200

# Print one "<release-arg> <timeout>" line per wait_hr_ready call in $1 whose
# release argument names a mariadb application. A call that passes no timeout
# prints "default" for it, because the helper's own fallback then applies and
# the call site says nothing about the budget it accepted.
mbw_mariadb_waits() {
  grep -v '^[[:space:]]*#' "$1" \
    | awk '/^wait_hr_ready[[:space:]]/ && $2 ~ /^"mariadb-/ {
             print $2, ($3 == "" ? "default" : $3)
           }'
}

# Print every release argument wait_hr_ready is called with in $1, mariadb or
# not. Used to show the extractor above is selecting rather than matching
# everything.
mbw_all_waits() {
  grep -v '^[[:space:]]*#' "$1" \
    | awk '/^wait_hr_ready[[:space:]]/ { print $2 }'
}

# Print the timeout, in seconds, of the first assert in each
# `wait-helmrelease-ready` step of the Chainsaw suite $1 -- each such step holds
# exactly one, and it is the HelmRelease gate this contract is about. Chainsaw
# takes a Go duration, so m and s suffixes are converted and anything else is
# printed verbatim for the caller to reject. A step that states no timeout at
# all prints "unset" rather than nothing, the same way the script-side extractor
# reports "default": this repo sets timeouts.assert to 5m in
# hack/e2e-chainsaw/.chainsaw.yaml, under every floor here, so silently
# skipping such a step would drop the worst case.
mbw_suite_hr_waits() {
  awk '
    # Close the previous step first, so an untimed one is reported wherever it
    # sits rather than only in the first position. seen is per-step, not
    # per-file: leaving it set after the first timed step made the report below
    # unreachable for every step after it.
    /^[[:space:]]*-[[:space:]]+name:[[:space:]]*/ {
      if (instep && !seen) print "unset"
      instep = 0
    }
    /^[[:space:]]*-[[:space:]]+name:[[:space:]]*wait-helmrelease-ready[[:space:]]*$/ {
      instep = 1; seen = 0; next
    }
    instep && !seen && /^[[:space:]]*timeout:[[:space:]]*[^[:space:]]+[[:space:]]*$/ {
      sub(/^[[:space:]]*timeout:[[:space:]]*/, "")
      sub(/[[:space:]]*$/, "")
      if ($0 ~ /^[0-9]+m$/)      { sub(/m$/, ""); print $0 * 60 }
      else if ($0 ~ /^[0-9]+s$/) { sub(/s$/, ""); print $0 + 0 }
      else                       { print }
      seen = 1
    }
    END { if (instep && !seen) print "unset" }
  ' "$1"
}

# Print, in seconds, the Install.Timeout cozystack-api stamps on every
# generated HelmRelease. A waiter that expires at the same moment gives up
# exactly when the install records why it failed, so this is the second floor
# every wait here has to beat.
mbw_install_timeout() {
  awk '/HelmReleaseInstallTimeout:[[:space:]]*"[0-9]+[ms]"/ {
         match($0, /"[0-9]+[ms]"/)
         v = substr($0, RSTART + 1, RLENGTH - 2)
         if (v ~ /m$/) { sub(/m$/, "", v); print v * 60 } else { sub(/s$/, "", v); print v + 0 }
         exit
       }' "$1"
}

# Print "ok" when a wait of $1 seconds clears a floor of $2, "low" otherwise.
# Echoes rather than returning a status, like every helper here: cozytest
# injects `return 0` before a `}` at column zero, which would make an
# exit-status verdict always succeed.
#
# Split out so the comparison itself can be pinned. Inline it is not
# pinnable: every wait in the tree clears every floor, so a floor operand
# replaced by 0 leaves the live values green while the guard accepts any
# positive number. As a helper it takes fixtures.
mbw_wait_clears_floor() {
  if [ "$1" -gt "$2" ]; then printf 'ok\n'; else printf 'low\n'; fi
}

# Print the chart's first-boot startup budget for a failureThreshold of $1.
# initialDelaySeconds=20 and periodSeconds=10 are the operator's probe defaults,
# which the chart deliberately does not override; both probe shapes carry them,
# so the arithmetic holds whichever the operator picks. Extracted because the
# two call sites below would otherwise each carry their own copy of it, and a
# budget that differs between them is worse than one that is wrong in both.
mbw_startup_budget() {
  printf '%s\n' "$(( 20 + ($1 - 1) * 10 ))"
}

# Print the seconds budget of every wait in $1 that runs up to and including the
# first mariadb wait_hr_ready, one per line. These are the ceilings the Chainsaw
# script op has to contain before the source instance can even fail: the bucket
# release, the two bucket fields, and the source wait itself. Continuation lines
# are joined first, because two of the calls wrap.
#
# Anchored at column zero: the flow of this script runs unindented, while an
# indented call sits inside a helper definition -- wait_app_grant_ready wraps
# one whose budget arrives in a variable, and reading it as part of the flow
# would count a ceiling that never runs there.
#
# A call whose last field is not a bare integer prints `skip` rather than
# nothing. Dropping it silently would shrink the sum, and a smaller sum makes
# the remainder guard MORE permissive -- the one direction a ratchet must never
# drift in. The caller treats any `skip` as a failure, so a wait that starts
# taking its budget from a variable or a default stops the contract instead of
# quietly loosening it.
mbw_prefix_ceilings() {
  grep -v '^[[:space:]]*#' "$1" \
    | awk '{ if (sub(/\\$/, "")) { buf = buf $0 " "; next }
             line = buf $0; buf = ""
             # The split below names an explicit regex separator rather than
             # using the default one, so trailing whitespace becomes an empty
             # final field and a perfectly literal budget reads as absent. That
             # is a false red on the kind of edit an editor makes by itself,
             # and a false red is cheapest to green by deleting the guard.
             sub(/[[:space:]]+$/, "", line)
             if (line ~ /^wait_hr_ready|^wait_for_field/) {
               n = split(line, f, /[[:space:]]+/)
               if (f[n] ~ /^[0-9]+$/) print f[n]; else print "skip"
             }
             if (line ~ /^wait_hr_ready[[:space:]]+"mariadb-/) exit
           }'
}

# Print the seconds budget of the Chainsaw `- script:` op in $1 that runs the
# round-trip, identified by what it RUNS rather than by how big it is. Three ops
# in that suite carry a minute timeout -- an 8m pre-clean, this one, and an 8m
# cleanup -- and taking the largest looks equivalent while the round-trip op is
# the largest. It is not: raising an unrelated op past it silently moves the
# ratchet onto the wrong number, in the loosening direction, and nothing reds.
# Selecting on the script it invokes cannot drift that way, because the identity
# does not depend on the values being compared.
#
# Comment lines are excluded from that match, and the direction that exclusion
# protects is the opposite of the one above -- worth stating, since this file
# argues that drift direction decides whether a gap is acceptable. The full path
# written into a comment inside an EARLIER op would move the measurement onto
# that op; the only one ahead of the round-trip is an 8m pre-clean, so the
# remainder would compute as 480 - 1560 = -1080 and the test below would fail
# LOUDLY. The exclusion prevents a false red, not a silent pass. Loosening would
# need an earlier op above 2520s and none is. Today the four other mentions in
# that file all use the bare `run-all.sh`, so this is defence against a
# plausible edit rather than a present bug.
mbw_suite_script_op() {
  awk '/^[[:space:]]*-[[:space:]]+script:[[:space:]]*$/ { inop = 1; t = ""; next }
       inop && /^[[:space:]]*timeout:[[:space:]]*[0-9]+m[[:space:]]*$/ {
         match($0, /[0-9]+/); t = substr($0, RSTART, RLENGTH) * 60; next
       }
       inop && !/^[[:space:]]*#/ && /examples\/backups\/mariadb\/run-all\.sh/ {
         if (t != "") { print t; exit }
       }
       /^[[:space:]]*-[[:space:]]+[a-z]/ && !/script:/ { inop = 0 }
      ' "$1"
}

# Print the larger of the two floors a wait has to beat: $1 the chart's own
# first-boot startup budget, $2 the install timeout the generated release
# carries. Which of them binds is not fixed. Today the install timeout does, and
# the startup budget would only take over if failureThreshold grew past it, so
# neither may be assumed to be the operative one.
mbw_compute_floor() {
  if [ "$1" -ge "$2" ]; then printf '%s\n' "$1"; else printf '%s\n' "$2"; fi
}

# Print the failureThreshold the chart sets on the MariaDB CR's startupProbe.
# Scoped to the startupProbe block by INDENTATION, so a threshold added to some
# other probe later cannot be mistaken for this one. Closing the block on "the
# next key that is not failureThreshold" would instead close it on a sibling:
# adding periodSeconds under startupProbe is a legitimate chart edit, and it
# would leave this returning nothing and the contract red for the wrong reason.
mbw_failure_threshold() {
  awk '
    /^[[:space:]]*startupProbe:[[:space:]]*$/ {
      match($0, /^[[:space:]]*/); blockindent = RLENGTH; inblock = 1; next
    }
    inblock && /[^[:space:]]/ {
      match($0, /^[[:space:]]*/)
      if (RLENGTH <= blockindent) { inblock = 0 }
    }
    inblock && /^[[:space:]]*failureThreshold:[[:space:]]*[0-9]+[[:space:]]*$/ {
      sub(/^[[:space:]]*failureThreshold:[[:space:]]*/, "")
      sub(/[[:space:]]*$/, "")
      print
      exit
    }
  ' "$1"
}

# Print the first-boot budget the chart's own comment says its threshold buys,
# in seconds. The chart states it as "A threshold of 30 lifts the budget to
# 310s", one sentence below a paragraph explaining that a shorter budget leaves
# the datadir half-initialised and the pod crash-looping with no path back.
# Reading the requirement out of the chart keeps it derived: raise the
# threshold and update the sentence, and both sides move together.
mbw_documented_budget() {
  # Comment markers stripped and lines joined before matching, because the
  # sentence is wrapped: "lifts the" ends one line and "budget to 310s" starts
  # the next. A line-oriented read finds nothing and reports the chart as
  # silent, which is the loudest possible way to be wrong about a file that
  # does say it.
  sed 's/^[[:space:]]*#[[:space:]]*//' "$1" \
    | tr '\n' ' ' | tr -s ' ' \
    | grep -o 'lifts the budget to [0-9][0-9]*s' \
    | head -1 | tr -cd '0-9'
}

@test "the chart still declares the startup failure threshold the budget derives from" {
    # Without this the budget below silently becomes 20 + (0-1)*10 = 10 and
    # every wait passes it. The threshold is what the whole contract hangs on,
    # so its absence has to be a failure and not a default.
    mbw_threshold=$(mbw_failure_threshold "$MBW_CHART")
    case "$mbw_threshold" in
      ''|*[!0-9]*)
        echo "no numeric startupProbe.failureThreshold found in $MBW_CHART (got '${mbw_threshold}')" >&2
        exit 1
        ;;
    esac
    # Not a bare lower bound. `-ge 2` admitted a threshold of 3, which the
    # chart's own comment describes as leaving the datadir half-initialised and
    # the pod crash-looping permanently -- and the floor comparison cannot see
    # it either, because max(40, 600) is still 600 and every wait clears that.
    # The requirement comes from the chart rather than from a literal here.
    mbw_want=$(mbw_documented_budget "$MBW_CHART")
    case "$mbw_want" in
      ''|*[!0-9]*)
        echo "$MBW_CHART no longer states what budget its threshold buys" >&2
        echo "expected a sentence of the form 'lifts the budget to <N>s'; without it this contract cannot tell a tuned threshold from a catastrophic one" >&2
        exit 1
        ;;
    esac
    mbw_budget=$(mbw_startup_budget "$mbw_threshold")
    [ "$mbw_budget" -ge "$mbw_want" ] || {
        echo "startupProbe.failureThreshold ${mbw_threshold} gives a ${mbw_budget}s first-boot budget, under the ${mbw_want}s the chart states it needs" >&2
        echo "the chart's comment explains what a short budget costs: a datadir with system tables but no root grant, and a pod that crash-loops with no path back" >&2
        exit 1
    }
}

@test "the install-timeout extractor converts its unit rather than assuming minutes" {
    # start.go carries "10m" today, so the seconds arm never runs against the
    # tree. A default written as "600s" read as 600 minutes would raise the
    # floor thirtyfold and red every wait; written the other way, a minutes
    # value read as seconds would drop it to a number every wait clears. Neither
    # is reachable from live values, so both are fixtures.
    mbw_tmp=$(mktemp)
    printf 'HelmReleaseInstallTimeout: "600s",\n' > "$mbw_tmp"
    mbw_got=$(mbw_install_timeout "$mbw_tmp")
    rm -f "$mbw_tmp"
    [ "$mbw_got" = "600" ] || { echo "600s gave '${mbw_got}', expected 600" >&2; exit 1; }

    mbw_tmp=$(mktemp)
    printf 'HelmReleaseInstallTimeout: "10m",\n' > "$mbw_tmp"
    mbw_got=$(mbw_install_timeout "$mbw_tmp")
    rm -f "$mbw_tmp"
    [ "$mbw_got" = "600" ] || { echo "10m gave '${mbw_got}', expected 600" >&2; exit 1; }
}

@test "the api server still declares the install timeout the floor derives from" {
    # The second half of the floor. Absent or unreadable, max() below collapses
    # to the startup budget alone and the contract silently stops pinning the
    # reason both call sites give for their current values.
    mbw_install=$(mbw_install_timeout "$MBW_APISERVER")
    case "$mbw_install" in
      ''|*[!0-9]*)
        echo "no numeric HelmReleaseInstallTimeout default found in $MBW_APISERVER (got '${mbw_install}')" >&2
        exit 1
        ;;
    esac
    [ "$mbw_install" -ge 60 ] || {
        echo "HelmReleaseInstallTimeout default parsed as ${mbw_install}s, which is too small to be the real default; the extractor is probably reading the wrong literal" >&2
        exit 1
    }
}

@test "a wait clears the floor only when it exceeds it" {
    # The comparison the whole contract ends in. Every wait in the tree clears
    # every floor, so this is another place where live values cannot tell a
    # correct implementation from a broken one; fixtures can.
    mbw_got=$(mbw_wait_clears_floor 601 600)
    [ "$mbw_got" = "ok" ] || { echo "601 vs 600 gave '${mbw_got}', expected ok" >&2; exit 1; }
    mbw_got=$(mbw_wait_clears_floor 600 600)
    [ "$mbw_got" = "low" ] || { echo "600 vs 600 gave '${mbw_got}', expected low: equal is not enough" >&2; exit 1; }
    mbw_got=$(mbw_wait_clears_floor 599 600)
    [ "$mbw_got" = "low" ] || { echo "599 vs 600 gave '${mbw_got}', expected low" >&2; exit 1; }
    mbw_got=$(mbw_wait_clears_floor 1 600)
    [ "$mbw_got" = "low" ] || { echo "1 vs 600 gave '${mbw_got}', expected low; a floor operand replaced by 0 would say ok here" >&2; exit 1; }
}

@test "the startup budget is the probe arithmetic and not some other formula" {
    # Fixtures for the same reason as the two below. With the live threshold of
    # 30 the budget is 310, the floor is max(310, 600) = 600 either way, and
    # both 660s waits clear it -- so `20 +` silently becoming `20 *` (580) or
    # the -1 being dropped (320) leaves every test in this file green while the
    # number the header explains is no longer the number computed.
    mbw_got=$(mbw_startup_budget 30)
    [ "$mbw_got" = "310" ] || { echo "budget(30) = ${mbw_got}, expected 310 = 20 + 29*10" >&2; exit 1; }
    mbw_got=$(mbw_startup_budget 1)
    [ "$mbw_got" = "20" ] || { echo "budget(1) = ${mbw_got}, expected 20: one failure allowed is initialDelaySeconds alone" >&2; exit 1; }
    mbw_got=$(mbw_startup_budget 2)
    [ "$mbw_got" = "30" ] || { echo "budget(2) = ${mbw_got}, expected 30; a dropped -1 would say 40 here" >&2; exit 1; }
    mbw_got=$(mbw_startup_budget 100)
    [ "$mbw_got" = "1010" ] || { echo "budget(100) = ${mbw_got}, expected 1010; this is the case where the budget half becomes the binding floor" >&2; exit 1; }
}

@test "the floor is the larger of the two bounds, whichever way they are ordered" {
    # The only arithmetic the whole contract rests on, and the one nothing else
    # exercises: with the real values 310 and 600 both waits clear either bound,
    # so a max silently turned into a min would go unnoticed and the guard would
    # start accepting anything above 310. Fixtures, because the tree cannot
    # currently produce a case where the two disagree in the other direction.
    mbw_got=$(mbw_compute_floor 310 600)
    [ "$mbw_got" = "600" ] || { echo "floor(310,600) = ${mbw_got}, expected the larger 600" >&2; exit 1; }
    mbw_got=$(mbw_compute_floor 600 310)
    [ "$mbw_got" = "600" ] || { echo "floor(600,310) = ${mbw_got}, expected the larger 600" >&2; exit 1; }
    mbw_got=$(mbw_compute_floor 910 600)
    [ "$mbw_got" = "910" ] || { echo "floor(910,600) = ${mbw_got}, expected the larger 910; a startup budget past the install timeout must take over" >&2; exit 1; }
    mbw_got=$(mbw_compute_floor 600 600)
    [ "$mbw_got" = "600" ] || { echo "floor(600,600) = ${mbw_got}, expected 600" >&2; exit 1; }
}

@test "the startup threshold is read from the startupProbe block and no other" {
    # Third helper in the same class as the two above. The chart carries exactly
    # one failureThreshold today, so a first-match read returns the same 30 and
    # every test that reads the TREE stays green with the indentation scoping
    # deleted. The fixture below is what makes that loss red, which is the whole
    # reason it is a fixture and not a tree read.
    # The break would also be quiet rather than red: a liveness threshold of 3
    # yields a 40s budget, which still passes the >= 2 guard, and max(40, 600)
    # is 600, which both 660s waits clear. Wrong budget, green suite, and it
    # starts deciding the moment the budget half becomes the binding floor.
    mbw_got=$(mbw_failure_threshold - <<'MBW_FIXTURE'
        livenessProbe:
          failureThreshold: 3
        startupProbe:
          failureThreshold: 30
MBW_FIXTURE
)
    [ "$mbw_got" = "30" ] || { echo "a livenessProbe threshold ahead of the startupProbe gave '${mbw_got}', expected 30" >&2; exit 1; }

    mbw_got=$(mbw_failure_threshold - <<'MBW_FIXTURE'
        startupProbe:
          periodSeconds: 10
          failureThreshold: 30
MBW_FIXTURE
)
    [ "$mbw_got" = "30" ] || { echo "a sibling key inside startupProbe gave '${mbw_got}', expected 30; closing the block on the next non-failureThreshold key would report nothing here" >&2; exit 1; }

    mbw_got=$(mbw_failure_threshold - <<'MBW_FIXTURE'
        startupProbe:
          periodSeconds: 10
        livenessProbe:
          failureThreshold: 3
MBW_FIXTURE
)
    [ -z "$mbw_got" ] || { echo "a startupProbe carrying no threshold gave '${mbw_got}', expected nothing so the guard above fires instead of a neighbouring probe's number being used" >&2; exit 1; }
}

@test "the prefix extractor reports a wait it cannot parse instead of dropping it" {
    # The `skip` branch has no live trigger: every prefix call in the script
    # states a literal today, so removing the branch leaves the whole file green.
    # It guards a future state, and a guard against a future state can only be
    # pinned by a fixture. The direction matters -- a dropped ceiling shrinks the
    # sum and LOOSENS the remainder ratchet, so silence here is worse than a
    # false red.
    mbw_tmp=$(mktemp)
    {
        printf 'wait_hr_ready "bucket-x" 300\n'
        printf 'wait_for_field foo bar baz qux "$NAMESPACE" "$some_variable"\n'
        printf 'wait_hr_ready "mariadb-src" 660\n'
    } > "$mbw_tmp"
    mbw_out=$(mbw_prefix_ceilings "$mbw_tmp" | tr '\n' ' ')
    rm -f "$mbw_tmp"
    [ "$mbw_out" = "300 skip 660 " ] || {
        echo "expected '300 skip 660 ', got: '${mbw_out}'" >&2
        exit 1
    }

    # And an indented call belongs to a helper definition, not to the flow.
    mbw_tmp=$(mktemp)
    {
        printf 'wait_app_grant_ready() {\n'
        printf '    wait_for_field grants.k8s.mariadb.com "$cr" x y "$ns" "$timeout"\n'
        printf '}\n'
        printf 'wait_hr_ready "bucket-x" 300\n'
        printf 'wait_hr_ready "mariadb-src" 660\n'
    } > "$mbw_tmp"
    mbw_out=$(mbw_prefix_ceilings "$mbw_tmp" | tr '\n' ' ')
    rm -f "$mbw_tmp"
    [ "$mbw_out" = "300 660 " ] || {
        echo "an indented call inside a helper was read as flow: '${mbw_out}', expected '300 660 '" >&2
        exit 1
    }
}

@test "the op is identified by the script it runs, not by being the largest" {
    # The live tree cannot separate the two rules: the round-trip op IS the
    # largest today, so selecting on size and selecting on identity agree, and a
    # fixture is the only thing that can tell them apart. The fixture puts a
    # bigger unrelated op first, which is the only state in which selecting by
    # size and selecting by identity disagree.
    mbw_tmp=$(mktemp)
    {
        printf '  - script:\n      timeout: 60m\n      content: |\n        echo unrelated\n'
        printf '  - script:\n      timeout: 42m\n      content: |\n        examples/backups/mariadb/run-all.sh\n'
    } > "$mbw_tmp"
    mbw_got=$(mbw_suite_script_op "$mbw_tmp")
    rm -f "$mbw_tmp"
    [ "$mbw_got" = "2520" ] || {
        echo "with a larger unrelated op present the helper returned '${mbw_got}', expected 2520" >&2
        exit 1
    }

    # And it must not return the unrelated op's budget when the round-trip op is
    # absent: reporting a number for a suite that does not run the script would
    # make the ratchet measure something with no relation to it at all.
    mbw_tmp=$(mktemp)
    printf '  - script:\n      timeout: 60m\n      content: |\n        echo unrelated\n' > "$mbw_tmp"
    mbw_got=$(mbw_suite_script_op "$mbw_tmp")
    rm -f "$mbw_tmp"
    [ -z "$mbw_got" ] || {
        echo "with no round-trip op the helper returned '${mbw_got}', expected nothing" >&2
        exit 1
    }
}

@test "the timeout dump survives the errexit its own caller sets" {
    # The only behavioural test here, and it exists because a static check
    # cannot see this class at all. wait_hr_ready's timeout branch reads
    # .status.history into a variable, and under the caller's `set -e` an
    # assignment takes its command substitution's exit status. The branch is
    # reachable with the release ABSENT -- the existence check above it gates
    # only the ready/stalled reads -- and `kubectl get` exits 1 on a missing
    # object. Without a guard the script dies AT the assignment: no dump, no
    # return, and the diagnostic dies in the case it was added for.
    #
    # Run in a subshell with the SAME options run-all.sh sets. A weaker set --
    # `set -u` without errexit, say -- cannot observe this at all: the failing
    # assignment is only fatal when errexit is live, so a check that omits it
    # reports the branch working while production dies at that line.
    # Both copies, not just the mariadb one. The postgres helper carries the
    # same edit and is outside this contract's usual reach -- the mutation
    # harness cannot touch it either -- so without this loop its half of the
    # change is verified by nothing but the identity check below.
    for mbw_helper in examples/backups/mariadb/00-helpers.sh \
                      examples/backups/postgres/00-helpers.sh; do
    # Run under bash EXPLICITLY, not under the runner's shell. run-all.sh and
    # both helpers are #!/bin/bash; cozytest.sh is #!/bin/sh and sources this
    # file, so on the CI runner the body would execute under dash, where
    # `set -o pipefail` is an illegal option and the helper's `local x=()` is a
    # parse error. On macOS /bin/sh is bash, so the wrong-interpreter version
    # runs green locally and proves nothing -- which is the same defect this
    # test exists to catch, one level up.
    mbw_out=$(bash -c '
        set -euo pipefail
        NAMESPACE=unit
        # shellcheck disable=SC1090
        . "$1"
        kubectl() { return 1; }
        wait_hr_ready "nosuch" 0 2>&1
    ' _ "$mbw_helper") || true
    case "$mbw_out" in
      *"(none recorded)"*) ;;
      *)
        echo "$mbw_helper: the timeout branch did not reach its history line under set -euo pipefail" >&2
        printf '%s\n' "$mbw_out" >&2
        exit 1
        ;;
    esac

    # Second arm: an EXISTING release whose history is simply empty. kubectl
    # defaults --allow-missing-template-keys to true, so that prints nothing and
    # exits 0 -- indistinguishable from the missing-release case above by output
    # alone. Both must reach the same line, which is why the line says only that
    # nothing was recorded and does not claim a cause.
    mbw_out=$(bash -c '
        set -euo pipefail
        NAMESPACE=unit
        # shellcheck disable=SC1090
        . "$1"
        kubectl() { return 0; }
        wait_hr_ready "nosuch" 0 2>&1
    ' _ "$mbw_helper") || true
    case "$mbw_out" in
      *"(none recorded)"*) ;;
      *)
        echo "$mbw_helper: an existing release with empty history did not reach the history line" >&2
        printf '%s\n' "$mbw_out" >&2
        exit 1
        ;;
    esac

    # Third arm: history PRESENT. Both arms above stub an empty kubectl, so both
    # land in the else and the printf that actually emits the history is never
    # executed -- the whole conditional could be replaced by the bare
    # "(none recorded)" line with every test here still green, which is the
    # diagnostic this change exists to add being deletable in silence. A branch
    # the tree does reach is exactly the kind this file argues elsewhere must be
    # pinned.
    mbw_out=$(bash -c '
        set -euo pipefail
        NAMESPACE=unit
        # shellcheck disable=SC1090
        . "$1"
        # Discriminate on the query: a stub that answers everything with the
        # same string makes the conditions dump above emit it too, and the
        # assertion below would then pass with the history branch deleted.
        kubectl() {
            case "$*" in
              *status.history*) printf "  history: deployed 1.2.3 2026-01-01T00:00:00Z\n" ;;
              # The conditions dump is matched on `.reason`, which the query
              # this change introduced asks for and the single-Ready-message
              # query it replaced does not. Reverting that jsonpath therefore
              # falls to the catch-all below and emits nothing, so the
              # assertion fails. Discriminating on the QUERY rather than on
              # the rendered output is what makes this a pin: a stub can be
              # told to return anything, but it cannot be asked the old
              # question and answer the new one.
              *.reason*) printf "  Ready=False (Progressing): install in progress\n" ;;
              *) : ;;
            esac
            return 0
        }
        wait_hr_ready "nosuch" 0 2>&1
    ' _ "$mbw_helper") || true
    case "$mbw_out" in
      *"history: deployed 1.2.3"*) ;;
      *)
        echo "$mbw_helper: a release WITH history did not reach the printf branch" >&2
        printf '%s\n' "$mbw_out" >&2
        exit 1
        ;;
    esac
    case "$mbw_out" in
      *"Ready=False (Progressing)"*) ;;
      *)
        echo "$mbw_helper: the timeout branch did not ask for every condition with its reason" >&2
        echo "the single-Ready-message query this replaced loses the reason a failing install records" >&2
        printf '%s\n' "$mbw_out" >&2
        exit 1
        ;;
    esac
    done

}

@test "the two wait_hr_ready copies have not diverged" {
    # A separate contract from the errexit one above, and it fails for a
    # separate reason, so it gets a name of its own: a divergence introduced by
    # editing one helper would otherwise surface only when the other example
    # ran. Compare the function bodies rather than the files, because the files
    # legitimately differ in their app-specific blocks.
    mbw_a=$(sed -n '/^wait_hr_ready()/,/^}/p' examples/backups/mariadb/00-helpers.sh)
    mbw_b=$(sed -n '/^wait_hr_ready()/,/^}/p' examples/backups/postgres/00-helpers.sh)
    [ -n "$mbw_a" ] || { echo "wait_hr_ready not found in the mariadb helper" >&2; exit 1; }
    [ "$mbw_a" = "$mbw_b" ] || {
        echo "the two wait_hr_ready copies have diverged" >&2
        printf '%s\n' "--- mariadb ---" "$mbw_a" "--- postgres ---" "$mbw_b" >&2
        exit 1
    }
}

@test "a trailing space does not turn a literal budget into skip" {
    # The extractor splits on an explicit regex, so whitespace at end of line
    # becomes an empty final field. Pinned against a fixture rather than the
    # tree, because the tree has no such line -- and if one ever appears, this
    # test is what says the contract still reads it rather than reporting a
    # budget that is not there.
    mbw_out=$(printf 'wait_hr_ready "bucket-x" 300   \nwait_hr_ready "mariadb-src" 660\n' | mbw_prefix_ceilings -)
    mbw_want="300
660"
    [ "$mbw_out" = "$mbw_want" ] || {
        echo "trailing whitespace changed the extraction:" >&2
        printf 'got:\n%s\nwant:\n%s\n' "$mbw_out" "$mbw_want" >&2
        exit 1
    }
    # The skip path still has to work: a budget that is genuinely not a literal
    # must still stop the contract rather than be silently dropped.
    mbw_out=$(printf 'wait_hr_ready "bucket-x" "$SOME_VAR"\nwait_hr_ready "mariadb-src" 660\n' | mbw_prefix_ceilings -)
    case "$mbw_out" in
      skip*) ;;
      *) echo "a non-literal budget no longer reports skip; got: ${mbw_out}" >&2; exit 1 ;;
    esac
}

@test "the round-trip op keeps the remainder its own comment claims" {
    # The 42m literal is the one number this branch derived and did not pin, and
    # leaving it unpinned is worse than merely undefended: the floor test above
    # can force the breach. Raise the api-server install-timeout default and the
    # floor rises with it, that test reds, and the cheapest way to green it is to
    # raise the two waits -- which the op then has to absorb or the SIGKILL band
    # it was raised to close reopens.
    #
    # What is pinned is the REMAINDER, not the sum. "The op exceeds the ceilings
    # ahead of the source wait" is the obvious form and it is vacuous here: at
    # 2520 against 1560 it holds, and it still holds after the waits go to 960s
    # (2520 against 1860) -- which is exactly the case that must not pass. The
    # remainder is what the flow actually spends on everything the ceilings do
    # not name, the comment in the suite states it as about sixteen minutes, and
    # a raise paid out of it is precisely the silent breach.
    #
    # The margin is zero today: 2520 - 1560 is exactly MBW_OP_REMAINDER, so the
    # next raise anywhere in the prefix reds this immediately. That is the
    # intent, not an accident of the numbers.
    #
    # MBW_OP_REMAINDER is a ratchet on that stated figure, not a derived bound:
    # nothing in the tree can compute what the applies and the secret
    # materialisation need. It may be raised deliberately; it may not fall.
    mbw_ceilings=$(mbw_prefix_ceilings "$MBW_SCRIPT")
    case "$mbw_ceilings" in
      '') echo "no wait ceilings found ahead of the source wait in $MBW_SCRIPT" >&2; exit 1 ;;
    esac
    case "$mbw_ceilings" in
      *skip*)
        echo "a wait ahead of the source wait states no literal budget:" >&2
        printf '%s\n' "$mbw_ceilings" >&2
        echo "the sum below would silently shrink, which loosens the ratchet" >&2
        exit 1
        ;;
    esac
    # The `skip` branch above closes one route to a shrunk sum, a budget that
    # stops being a literal. This closes the other: the extractor anchors at
    # column zero, so indenting a flow call -- wrapping it in an `if` or a retry
    # loop -- drops its ceiling silently. Measured: indenting the bucket wait
    # yields `300 300 660`, a 1260s remainder, and the ratchet below still
    # passes. The suite does red today, but through an unrelated test whose
    # fixture premise happens to name that wait, which is not a guard this
    # contract may lean on.
    mbw_count=$(printf '%s\n' "$mbw_ceilings" | wc -l | tr -d ' ')
    [ "$mbw_count" -eq "$MBW_PREFIX_CEILINGS" ] || {
        echo "found ${mbw_count} prefix ceilings ahead of the source wait, expected ${MBW_PREFIX_CEILINGS}:" >&2
        printf '%s\n' "$mbw_ceilings" >&2
        echo "a lost ceiling shrinks the sum and loosens the remainder ratchet; a new one needs the remainder re-derived" >&2
        exit 1
    }
    mbw_sum=0
    for mbw_c in $mbw_ceilings; do mbw_sum=$(( mbw_sum + mbw_c )); done
    [ "$mbw_sum" -ge 600 ] || {
        echo "prefix ceilings sum to ${mbw_sum}s, too small to be the real set; the extractor is reading the wrong lines" >&2
        exit 1
    }
    mbw_op=$(mbw_suite_script_op "$MBW_SUITE")
    case "$mbw_op" in
      ''|*[!0-9]*)
        echo "no script-op timeout found in $MBW_SUITE (got '${mbw_op}')" >&2
        exit 1
        ;;
    esac
    mbw_remainder=$(( mbw_op - mbw_sum ))
    [ "$mbw_remainder" -ge "$MBW_OP_REMAINDER" ] || {
        echo "the round-trip op leaves ${mbw_remainder}s after the ${mbw_sum}s of ceilings ahead of and including the source wait" >&2
        echo "ceilings: $(printf '%s ' $mbw_ceilings)" >&2
        echo "that is below the ${MBW_OP_REMAINDER}s the suite comment claims is left for the applies and the secret materialisation" >&2
        echo "three edits reach this state: a ceiling ahead of the source wait was raised, one was added, or the op was lowered" >&2
        echo "the fix is whichever of those you meant, plus the op in the same change -- not raising MBW_OP_REMAINDER" >&2
        echo "a wait was raised without the op absorbing it, and a late failure is now SIGKILLed with the process group instead of reported" >&2
        exit 1
    }
}

@test "the round-trip op keeps its slack once both mariadb waits are paid" {
    # The remainder test above stops at the source wait, because that is where
    # the prefix ends. The target wait is past it and therefore invisible
    # there. This one spans both, so the quantity it holds is the one the
    # suite comment actually reasons about: what the op has left for the
    # applies, the dump, the restore and the verifies once the two HelmRelease
    # waits have taken their ceilings.
    mbw_op=$(mbw_suite_script_op "$MBW_SUITE")
    case "$mbw_op" in
      ''|*[!0-9]*) echo "no script-op timeout found in $MBW_SUITE (got '${mbw_op}')" >&2; exit 1 ;;
    esac
    mbw_waits=$(mbw_mariadb_waits "$MBW_SCRIPT")
    [ -n "$mbw_waits" ] || { echo "no mariadb waits found in $MBW_SCRIPT" >&2; exit 1; }
    mbw_sum=0
    mbw_n=0
    mbw_ifs=$IFS
    IFS='
'
    for mbw_line in $mbw_waits; do
        IFS=$mbw_ifs
        mbw_t=${mbw_line##* }
        case "$mbw_t" in
          ''|*[!0-9]*)
            echo "a mariadb wait states no literal budget: ${mbw_line}" >&2
            echo "the sum below would shrink, which loosens this guard" >&2
            exit 1
            ;;
        esac
        mbw_sum=$(( mbw_sum + mbw_t ))
        mbw_n=$(( mbw_n + 1 ))
    done
    IFS=$mbw_ifs
    [ "$mbw_n" -eq 2 ] || {
        echo "expected exactly 2 mariadb HelmRelease waits, found ${mbw_n}:" >&2
        printf '%s\n' "$mbw_waits" >&2
        echo "a lost wait shrinks the sum and loosens this guard; a new one needs the slack re-derived" >&2
        exit 1
    }
    mbw_slack=$(( mbw_op - mbw_sum ))
    [ "$mbw_slack" -ge "$MBW_OP_MINUS_WAITS" ] || {
        echo "the round-trip op leaves ${mbw_slack}s once both mariadb waits (${mbw_sum}s) are paid" >&2
        echo "that is below the ${MBW_OP_MINUS_WAITS}s this suite reserves for the applies, the dump, the restore and the verifies" >&2
        echo "two edits reach this state: a mariadb wait was raised, or the op was lowered" >&2
        echo "the fix is the op in the same change -- not raising MBW_OP_MINUS_WAITS" >&2
        exit 1
    }
}

@test "the mariadb ApplicationDefinition does not override that install timeout" {
    # The floor uses the server-wide default. An ApplicationDefinition may
    # override it per kind, and four in this tree do, so if mariadb ever joins
    # them the derivation below is reading the wrong number and has to be
    # taught the override rather than left silently wrong.
    #
    # The existence check is load-bearing, not defensive: grep exits non-zero on
    # a file it cannot open, so without it a moved or renamed RD sends both
    # checks below down their false branch and this test reports green having
    # read nothing. RD paths in this tree have moved before.
    [ -f "$MBW_RD" ] || {
        echo "$MBW_RD not found, so the floor derivation cannot be checked against a per-application override" >&2
        exit 1
    }
    if grep -q "release.cozystack.io/helm-install-timeout" "$MBW_RD"; then
        echo "$MBW_RD now sets a per-application install timeout; the floor in this file derives from the server default and must be taught to read the override" >&2
        exit 1
    fi
    # The other annotation that invalidates the derivation, and more completely:
    # with waiting disabled the install stops spanning first boot at all, so the
    # premise this whole file rests on is gone rather than merely mis-numbered.
    if grep -q "release.cozystack.io/helm-install-disable-wait" "$MBW_RD"; then
        echo "$MBW_RD disables the install wait; HelmRelease readiness no longer spans a first boot and this contract no longer describes anything" >&2
        exit 1
    fi
}

@test "the example waits for a HelmRelease of both MariaDB applications it deploys" {
    # Guards the assertion below against going vacuous: renaming, removing or
    # reformatting a call site would otherwise leave nothing to check and the
    # contract would pass by finding zero waits.
    mbw_out=$(mbw_mariadb_waits "$MBW_SCRIPT")
    mbw_count=$(printf '%s\n' "$mbw_out" | grep -c 'mariadb-' || true)
    [ "$mbw_count" -ge 2 ] || {
        echo "expected at least 2 mariadb wait_hr_ready calls in $MBW_SCRIPT, found ${mbw_count}:" >&2
        printf '%s\n' "$mbw_out" >&2
        exit 1
    }
    printf '%s\n' "$mbw_out" | grep -q 'MARIADB_SRC_NAME' || {
        echo "no wait_hr_ready call covers the source MariaDB application" >&2
        printf '%s\n' "$mbw_out" >&2
        exit 1
    }
    printf '%s\n' "$mbw_out" | grep -q 'MARIADB_TARGET_NAME' || {
        echo "no wait_hr_ready call covers the target MariaDB application" >&2
        printf '%s\n' "$mbw_out" >&2
        exit 1
    }
}

@test "every mariadb HelmRelease wait in the example beats the first-boot budget" {
    mbw_threshold=$(mbw_failure_threshold "$MBW_CHART")
    mbw_budget=$(mbw_startup_budget "$mbw_threshold")
    mbw_install=$(mbw_install_timeout "$MBW_APISERVER")
    mbw_floor=$(mbw_compute_floor "$mbw_budget" "$mbw_install")
    mbw_out=$(mbw_mariadb_waits "$MBW_SCRIPT")
    # Inline, not delegated to the neighbouring count test: piping an empty
    # extractor into `while` passes on zero iterations, so without this the
    # loop below is vacuous whenever extraction breaks. The suite-side check
    # carries the same guard for the same reason.
    mbw_count=$(printf '%s\n' "$mbw_out" | grep -c 'mariadb-' || true)
    [ "$mbw_count" -ge 2 ] || {
        echo "expected at least 2 mariadb waits to check, found ${mbw_count}" >&2
        exit 1
    }
    # Split on newlines in the CURRENT shell rather than piping into `while`.
    # A pipeline runs its loop in a subshell, so an `exit 1` inside the body
    # kills only that subshell and the test carries on; the usual repair is a
    # `|| flag=1` after `done` plus a check. That repair is one token wide --
    # replacing it with `|| true` leaves every test green while both loops stop
    # deciding anything, which is a guard deletable in silence. Removing the
    # subshell removes the thing that would have to be pinned.
    mbw_ifs=$IFS
    IFS='
'
    for mbw_line in $mbw_out; do
        IFS=$mbw_ifs
        mbw_name=${mbw_line% *}
        mbw_timeout=${mbw_line##* }
        case "$mbw_timeout" in
          ''|*[!0-9]*)
            echo "wait_hr_ready ${mbw_name} states no literal timeout (got '${mbw_timeout}'); this contract can only check a literal, and the budget must be above ${mbw_floor}s" >&2
            exit 1
            ;;
        esac
        [ "$(mbw_wait_clears_floor "$mbw_timeout" "$mbw_floor")" = "ok" ] || {
            echo "wait_hr_ready ${mbw_name} allows ${mbw_timeout}s; the floor is ${mbw_floor}s (startup budget ${mbw_budget}s from failureThreshold=${mbw_threshold}, release install timeout ${mbw_install}s) and a wait must outlast both" >&2
            exit 1
        }
    done
    IFS=$mbw_ifs
}

@test "the chainsaw suite's HelmRelease-ready asserts beat the same budget" {
    # Same wait, same mechanism, different file. This assert is the first gate
    # in each suite, so if it expires below a legal first boot the suite reds on
    # a healthy instance -- the defect this contract exists to stop, in the
    # place its justification was written down.
    mbw_threshold=$(mbw_failure_threshold "$MBW_CHART")
    mbw_budget=$(mbw_startup_budget "$mbw_threshold")
    mbw_install=$(mbw_install_timeout "$MBW_APISERVER")
    mbw_floor=$(mbw_compute_floor "$mbw_budget" "$mbw_install")
    mbw_out=$(mbw_suite_hr_waits "$MBW_SUITE")
    mbw_count=$(printf '%s\n' "$mbw_out" | grep -c '[0-9]' || true)
    [ "$mbw_count" -ge 2 ] || {
        echo "expected at least 2 wait-helmrelease-ready timeouts in $MBW_SUITE, found ${mbw_count}:" >&2
        printf '%s\n' "$mbw_out" >&2
        exit 1
    }
    # Current shell, not a pipeline subshell -- see the loop above.
    mbw_ifs=$IFS
    IFS='
'
    for mbw_timeout in $mbw_out; do
        IFS=$mbw_ifs
        case "$mbw_timeout" in
          *[!0-9]*)
            echo "unparsed wait-helmrelease-ready timeout '${mbw_timeout}' in $MBW_SUITE" >&2
            exit 1
            ;;
        esac
        [ "$(mbw_wait_clears_floor "$mbw_timeout" "$mbw_floor")" = "ok" ] || {
            echo "a wait-helmrelease-ready assert allows ${mbw_timeout}s; the floor is ${mbw_floor}s (startup budget ${mbw_budget}s, release install timeout ${mbw_install}s) and a wait must outlast both" >&2
            exit 1
        }
    done
    IFS=$mbw_ifs
}

@test "the extractor rejects a mariadb wait that states no timeout" {
    # The helper's own fallback is 300s, below any realistic budget, so a call
    # site that omits the argument must not read as compliant. Checked against
    # a fixture: the real script is expected to state its budgets, so this
    # branch of the contract has no cover in the tree itself.
    mbw_tmp=$(mktemp)
    printf 'wait_hr_ready "mariadb-${MARIADB_SRC_NAME}"\n' > "$mbw_tmp"
    mbw_out=$(mbw_mariadb_waits "$mbw_tmp")
    rm -f "$mbw_tmp"
    [ "$mbw_out" = '"mariadb-${MARIADB_SRC_NAME}" default' ] || {
        echo "expected the timeout to be reported as 'default', got: ${mbw_out}" >&2
        exit 1
    }
}

@test "the suite extractor reports a wait-helmrelease-ready step that states no timeout" {
    # hack/e2e-chainsaw/.chainsaw.yaml sets timeouts.assert to 5m, under every
    # floor this file computes, so a step that omits `timeout:` is the worst
    # case and must not be skipped in silence. Fixtures, because the tree
    # states every timeout.
    mbw_tmp=$(mktemp)
    {
        printf '  - name: wait-helmrelease-ready\n    try:\n    - assert:\n        timeout: 11m\n        resource: {}\n'
        printf '  - name: wait-helmrelease-ready\n    try:\n    - assert:\n        resource: {}\n'
        printf '  - name: next-step\n'
    } > "$mbw_tmp"
    mbw_out=$(mbw_suite_hr_waits "$mbw_tmp" | tr '\n' ' ')
    rm -f "$mbw_tmp"
    # The timed step goes FIRST on purpose. With only an untimed step the
    # fixture cannot tell "reports it" from "reports it only in first
    # position", and an extractor that resets its state per step and one that
    # never resets it both pass a single-element fixture.
    [ "$mbw_out" = "660 unset " ] || {
        echo "expected '660 unset ', got: '${mbw_out}'" >&2
        exit 1
    }
    # Units other than minutes, and a unit the extractor does not know. Every
    # timeout in the tree is written Nm, so the seconds arm and the verbatim
    # fallback are both unreachable from live values -- the same argument this
    # file makes for the arithmetic helpers, applied to the branch that decides
    # what a number MEANS. A seconds budget silently read as minutes would be a
    # sixtyfold error in the safe-looking direction.
    mbw_tmp=$(mktemp)
    {
        printf '  - name: wait-helmrelease-ready\n    try:\n    - assert:\n        timeout: 90s\n        resource: {}\n'
        printf '  - name: wait-helmrelease-ready\n    try:\n    - assert:\n        timeout: 2h\n        resource: {}\n'
        printf '  - name: next-step\n'
    } > "$mbw_tmp"
    mbw_out=$(mbw_suite_hr_waits "$mbw_tmp" | tr '\n' ' ')
    rm -f "$mbw_tmp"
    [ "$mbw_out" = "90 2h " ] || {
        echo "expected '90 2h ' (seconds converted, unknown unit passed through), got: '${mbw_out}'" >&2
        exit 1
    }

    # Third fixture: the file ENDS inside an untimed wait step, so no `- name:`
    # line ever closes it. That is the awk END rule, and the two fixtures above
    # cannot reach it -- both end on `- name: next-step`, which closes the block
    # first. Without this the END branch is the one unexercised path in an
    # extractor every other branch of which is pinned.
    mbw_tmp=$(mktemp)
    {
        printf '  - name: wait-helmrelease-ready\n    try:\n    - assert:\n        timeout: 11m\n        resource: {}\n'
        printf '  - name: wait-helmrelease-ready\n    try:\n    - assert:\n        resource: {}\n'
    } > "$mbw_tmp"
    mbw_out=$(mbw_suite_hr_waits "$mbw_tmp" | tr '\n' ' ')
    rm -f "$mbw_tmp"
    [ "$mbw_out" = "660 unset " ] || {
        echo "a file ending inside an untimed step gave: '${mbw_out}', expected '660 unset '" >&2
        exit 1
    }
    mbw_tmp=$(mktemp)
    {
        printf '  - name: wait-helmrelease-ready\n    try:\n    - assert:\n        resource: {}\n'
        printf '  - name: wait-helmrelease-ready\n    try:\n    - assert:\n        timeout: 6m\n        resource: {}\n'
        printf '  - name: next-step\n'
    } > "$mbw_tmp"
    mbw_out=$(mbw_suite_hr_waits "$mbw_tmp" | tr '\n' ' ')
    rm -f "$mbw_tmp"
    # Two consecutive wait steps, the first untimed: the close-previous rule has
    # to fire before the open-new one or the first is swallowed.
    [ "$mbw_out" = "unset 360 " ] || {
        echo "expected 'unset 360 ', got: '${mbw_out}'" >&2
        exit 1
    }
}

@test "the extractor selects mariadb waits and leaves the others alone" {
    # The example also waits for the Bucket release, and this contract is
    # deliberately scoped to the mariadb ones. The reason is scope, not the
    # floor: the startup-budget half is mariadb-specific, but the install-timeout
    # half is release-generic, and packages/system/bucket-rd sets neither
    # helm-install-timeout nor helm-install-disable-wait, so the bucket release
    # carries the same 600s floor and its 300s wait is under it. A sweep would
    # therefore not be holding a foreign chart to a foreign contract -- it would
    # be reporting a real under-budget in this same file. That belongs to
    # whoever raises the example's non-mariadb waits, and this guard stays
    # mariadb-scoped so its failures keep naming one cause.
    mbw_all=$(mbw_all_waits "$MBW_SCRIPT")
    printf '%s\n' "$mbw_all" | grep -q '^"bucket-' || {
        echo "expected $MBW_SCRIPT to wait for a bucket release; the fixture this test reasons about is gone" >&2
        printf '%s\n' "$mbw_all" >&2
        exit 1
    }
    mbw_selected=$(mbw_mariadb_waits "$MBW_SCRIPT")
    # `if !` rather than `grep -q ... && { ... }`: the latter evaluates to the
    # grep's own status when it does not match, which under set -e ends the test
    # unless a trailing `exit 0` follows it -- and that terminator silently makes
    # anything appended below it dead code.
    if printf '%s\n' "$mbw_selected" | grep -q 'bucket-'; then
        echo "the mariadb extractor picked up a non-mariadb release:" >&2
        printf '%s\n' "$mbw_selected" >&2
        exit 1
    fi
}
