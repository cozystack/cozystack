#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Contract: nothing that waits for the mariadb HelmRelease to become Ready may
# allow less than that release can legitimately take. The chainsaw suite's
# HR-Ready asserts are such waits and are checked here. The other suites'
# HR-Ready asserts are not checked here.
#
# A wait has two floors and must beat the larger one by MBW_START_MARGIN. The
# margin is a policy value, not a measurement: it stands for the time the
# wait's clock runs before the install's -- the Application reconcile, the
# chart artifact fetch and the helm-controller queue -- none of which the tree
# bounds. It equals the headroom every wait held here already carries.
#
# The first floor is the chart's first-boot budget: helm-controller defaults
# spec.waitStrategy to `poller`, kstatus has no rule for
# k8s.mariadb.com/MariaDB and reads its Ready=False as InProgress, so the
# install MAY span a first boot. "May", because a poll landing before the
# operator's first status write reads a condition-less CR as Current, which is
# why the suite keeps an endpoint assert after the HR-Ready one. The budget is
# initialDelaySeconds + (failureThreshold - 1) * periodSeconds, with the
# threshold from the chart and the operator's defaults of 20 and 10 (both
# probe shapes, in mariadb-operator's pkg/builder/container_builder.go). It
# is one boot, not one per replica, because the operator sets
# PodManagementPolicy: Parallel. Were that wrong, two replicas would serialize
# to 620s, the floor would bind there, and 660 would fall short of the margin
# over it.
#
# The second floor is the install timeout cozystack-api stamps on the release
# (pkg/cmd/server/start.go). A wait equal to it expires first, because its clock
# starts when the step runs and the install's only after the Application is
# reconciled and the chart fetched. Of the two floors it is the one that binds
# today.
#
# What is not guarded, because each would make the numbers slack rather than
# short: cozystack-api taking --helmrelease-install-timeout from a manifest
# (only cozystack-operator is passed it today); a waitStrategy of `legacy` on
# the RD; the UseHelm3Defaults feature gate. healthCheckExprs alone
# resolves to `poller`, so it is not a lever. Also unguarded: an operator bump
# that moves the probe defaults; a timed non-HelmRelease assert placed ahead of
# the HelmRelease one inside a wait-helmrelease-ready step, or an HR-Ready
# assert in a step with another name, since the suite extractor selects by step
# name and takes the first timeout in the step.
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` on its
# own line; there is no bats `run` or `$status`, and no setup/teardown.
# Assertions are direct shell tests that exit non-zero. A `}` at column zero
# gets `return 0` inserted ahead of it, so the helpers below report through
# stdout only and never through an exit status.
#
# Run with: hack/cozytest.sh hack/mariadb-first-boot-waits.bats
# -----------------------------------------------------------------------------

load test_helper

MBW_CHART="packages/apps/mariadb/templates/mariadb.yaml"
MBW_SUITE="hack/e2e-chainsaw/mariadb/chainsaw-test.yaml"
MBW_APISERVER="pkg/cmd/server/start.go"
# Seconds a wait must exceed its floor by. Policy, not measurement; the header
# says what it stands for.
MBW_START_MARGIN=60

# Print the timeout, in seconds, of the first assert in each
# `wait-helmrelease-ready` step of the Chainsaw suite $1 -- each such step holds
# exactly one, and it is the HelmRelease gate this contract is about. Chainsaw
# takes a Go duration, so m and s suffixes are converted and anything else is
# printed verbatim for the caller to reject. A step that states no timeout at
# all prints "unset" rather than nothing: this repo sets timeouts.assert to 5m
# in hack/e2e-chainsaw/.chainsaw.yaml, under every floor here, so silently
# skipping such a step would drop the worst case.
mbw_suite_hr_waits() {
  awk '
    # Close the previous step first, so an untimed one is reported wherever it
    # sits. seen is per-step, not per-file: set per file, it would hide every
    # untimed step after the first timed one.
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

# Print "ok" when a wait of $1 seconds is at least MBW_START_MARGIN above a
# floor of $2, "low" otherwise.
# Echoes rather than returning a status, like every helper here: cozytest
# injects `return 0` before a `}` at column zero, which would make an
# exit-status verdict always succeed.
#
# Split out so the comparison itself can be pinned. Inline it is not
# pinnable: every wait in the tree clears every floor, so a floor operand
# replaced by 0 leaves the live values green while the guard accepts any
# positive number. As a helper it takes fixtures.
mbw_wait_clears_floor() {
  if [ "$1" -ge $(( $2 + MBW_START_MARGIN )) ]; then printf 'ok\n'; else printf 'low\n'; fi
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
    # Not a bare lower bound: a threshold of 3 is positive, and the chart's own
    # comment describes it as leaving the datadir half-initialised and the pod
    # crash-looping permanently. The floor comparison cannot see that either,
    # because max(40, 600) is still 600 and every wait clears it.
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

@test "a wait clears the floor only by the start margin" {
    # The comparison the whole contract ends in. Every wait in the tree clears
    # every floor, so this is another place where live values cannot tell a
    # correct implementation from a broken one; fixtures can. The literals
    # assume the 60s policy margin, so changing MBW_START_MARGIN means
    # restating them here on purpose.
    [ "$MBW_START_MARGIN" = "60" ] || { echo "MBW_START_MARGIN is '${MBW_START_MARGIN}', the fixtures below are written for 60" >&2; exit 1; }
    mbw_got=$(mbw_wait_clears_floor 660 600)
    [ "$mbw_got" = "ok" ] || { echo "660 vs 600 gave '${mbw_got}', expected ok: the margin is met exactly" >&2; exit 1; }
    mbw_got=$(mbw_wait_clears_floor 659 600)
    [ "$mbw_got" = "low" ] || { echo "659 vs 600 gave '${mbw_got}', expected low: one second short of the margin" >&2; exit 1; }
    mbw_got=$(mbw_wait_clears_floor 601 600)
    [ "$mbw_got" = "low" ] || { echo "601 vs 600 gave '${mbw_got}', expected low: a second over the floor is not the margin" >&2; exit 1; }
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
    # start measuring from 310 instead of 600. Fixtures, because the tree cannot
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

@test "the chainsaw suite's HelmRelease-ready asserts beat the same budget" {
    # This assert is the first gate
    # in each Test, so if it expires below a legal first boot the suite reds on
    # a healthy instance -- the defect this contract exists to stop.
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
    # Split in the current shell rather than piped into `while`: a pipeline
    # runs its loop in a subshell, where `exit 1` ends only the subshell and
    # the test carries on green.
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
            echo "a wait-helmrelease-ready assert allows ${mbw_timeout}s; the floor is ${mbw_floor}s (startup budget ${mbw_budget}s, release install timeout ${mbw_install}s) and a wait must exceed it by MBW_START_MARGIN=${MBW_START_MARGIN}s" >&2
            exit 1
        }
    done
    IFS=$mbw_ifs
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
