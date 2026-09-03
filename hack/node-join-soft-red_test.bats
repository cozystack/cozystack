#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for hack/e2e-node-join-soft-red.sh -- the decision one layer above
# the suite about whether a failed Chainsaw run may leave the job green.
#
# Named without the `e2e-` prefix the script itself carries, and deliberately:
# the root Makefile builds the unit lane as
# `$(filter-out hack/e2e-%.bats,$(wildcard hack/*.bats))`, so a file named after
# the script would be excluded from `make unit-tests` and run nowhere.
#
# What these pin is the direction the gate fails in. It opens on exactly one
# shape -- every failed test carrying the marker the node-join deadline writes
# -- and blocks on everything else, including every way it can fail to answer
# the question. A gate that opens because it could not read its own input is
# worse than no gate, because the run looks examined.
#
# `docker` is stubbed rather than run: the real script reaches into the sandbox
# container for the report and for each marker, and the stub is what lets a test
# say "this report, these markers" without one.
#
# cozytest.sh's awk parser ends an @test block at the first bare closing brace,
# so the mocks stay at top level. EXIT-trap cleanup is banned in hack/*.bats, so
# each test removes its own temp dir on its last line.
#
# Run with: hack/cozytest.sh hack/node-join-soft-red_test.bats
# -----------------------------------------------------------------------------

GATE=hack/e2e-node-join-soft-red.sh

# The sandbox stands in for a container, and it has to be a real executable on
# PATH rather than a shell function: the script under test is invoked through
# `sh`, and a function does not survive a new shell. It answers the two
# questions the script asks -- cat the report, test for a marker -- off files a
# test writes, so a test states its world instead of staging a container.
#
# `rm -f` before the redirect is the house idiom for staging a fake binary: a
# name that already exists as a symlink to a real one is written THROUGH.
stage_sandbox_stub() {
  mkdir -p "$1/bin"
  rm -f "$1/bin/docker"
  cat >"$1/bin/docker" <<'STUB'
#!/bin/sh
case "$*" in
  *chainsaw-report.xml*)
    [ -f "${COZY_STUB_DIR}/report-fails" ] && exit 1
    cat "${COZY_STUB_DIR}/report"
    exit 0
    ;;
  *SOFT-RED-node-join.txt*)
    # The test name is the LAST argument, not part of the script text: the gate
    # hands it to the sandbox shell as an argument so that report content never
    # reaches a command the sandbox parses. Reading it from anywhere else here
    # would leave that property untested.
    for _a in "$@"; do _name=$_a; done
    # Logged verbatim: a test that cares whether a name reached the lookup at all
    # cannot tell that from the exit code, which is the same for a name that
    # missed and for a lookup that never happened.
    printf '%s\n' "$_name" >>"${COZY_STUB_DIR}/lookups"
    grep -qxF "$_name" "${COZY_STUB_DIR}/markers" 2>/dev/null && exit 0
    exit 1
    ;;
esac
echo "unexpected docker call: $*" >&2
exit 99
STUB
  chmod +x "$1/bin/docker"
  export COZY_STUB_DIR="$1"
  : >"$1/report"
  : >"$1/markers"
  : >"$1/lookups"
  PATH="$1/bin:$PATH"
}

# A report in the shape chainsaw 0.2.15 writes: one testsuite element per suite
# directory, one testcase per Test, and a nested failure element on the ones
# that failed. Taking the real shape matters here -- the enclosing testsuite
# carries a `failures="N"` attribute, and a matcher that read that as a failed
# case would report every suite as failing.
report_with() {
  printf '%s\n' '<testsuites name="chainsaw-report" time="1" tests="2" failures="1">'
  for _spec in "$@"; do
    _name=${_spec%:*}
    _verdict=${_spec##*:}
    printf '  <testsuite name="%s" tests="1" failures="1" errors="0" id="0" time="">\n' "${_name}"
    printf '    <testcase name="%s" classname="" time="1">\n' "${_name}"
    [ "${_verdict}" = failed ] && printf '      <failure message="exit status 1"></failure>\n'
    printf '    </testcase>\n  </testsuite>\n'
  done
  printf '%s\n' '</testsuites>'
}

@test "a run whose only failure carries the node-join marker does not block the job" {
  tmp=$(mktemp -d)
  stage_sandbox_stub "$tmp"
  report_with kubernetes-latest:failed bucket:passed >"$tmp/report"
  echo kubernetes-latest >"$tmp/markers"

  rc=0
  ( set +x; sh "$GATE" sandbox ) >"$tmp/out" 2>&1 || rc=$?

  [ "$rc" -eq 0 ]
  grep -q 'kubernetes-latest' "$tmp/out"
  rm -rf "$tmp"
}

@test "a failed test without the marker blocks the job even beside one that has it" {
  # The pair matters: a gate that answered "some failure was a deadline" instead
  # of "every failure was" would open here, and this is the shape that happens
  # on a real run -- the node-join deadline expires on one suite while something
  # unrelated breaks in another.
  tmp=$(mktemp -d)
  stage_sandbox_stub "$tmp"
  report_with kubernetes-latest:failed bucket:failed >"$tmp/report"
  echo kubernetes-latest >"$tmp/markers"

  rc=0
  ( set +x; sh "$GATE" sandbox ) >"$tmp/out" 2>&1 || rc=$?

  [ "$rc" -ne 0 ]
  grep -q 'bucket' "$tmp/out"
  rm -rf "$tmp"
}

@test "a failed kubernetes suite with no marker blocks, because only the deadline writes one" {
  # Every other way the kubernetes suites can go red -- an assertion after the
  # join, the wait failing to run at all -- leaves no marker, and must be
  # indistinguishable to this gate from any other suite failing.
  tmp=$(mktemp -d)
  stage_sandbox_stub "$tmp"
  report_with kubernetes-previous:failed >"$tmp/report"

  rc=0
  ( set +x; sh "$GATE" sandbox ) >/dev/null 2>&1 || rc=$?

  [ "$rc" -ne 0 ]
  rm -rf "$tmp"
}

@test "a report that cannot be read blocks rather than opening on an unknown" {
  tmp=$(mktemp -d)
  stage_sandbox_stub "$tmp"
  : >"$tmp/report-fails"
  echo kubernetes-latest >"$tmp/markers"

  rc=0
  ( set +x; sh "$GATE" sandbox ) >/dev/null 2>&1 || rc=$?

  [ "$rc" -ne 0 ]
  rm -rf "$tmp"
}

@test "a report recording no failure at all blocks, because the run failed anyway" {
  # This is reached when chainsaw died before it could record why, and the
  # temptation is to read "no failures" as "nothing wrong". The step only calls
  # this after a non-zero make, so an empty failure list means the report and
  # the run disagree, and the run is the one that happened.
  tmp=$(mktemp -d)
  stage_sandbox_stub "$tmp"
  report_with bucket:passed >"$tmp/report"

  rc=0
  ( set +x; sh "$GATE" sandbox ) >/dev/null 2>&1 || rc=$?

  [ "$rc" -ne 0 ]
  rm -rf "$tmp"
}

# A report whose failed case cannot be named, with a NAMED suite after it. The
# order is the whole point rather than a detail of the fixture: an awk record
# split on `<testcase` runs to the next one, so it carries the following
# `<testsuite name="...">` with it, and a case that cannot be named from its own
# opening tag can be named from that. Put the unnameable case last and every
# assertion below passes against a gate that has the bug -- which is what these
# three fixtures did before they were written this way.
#
# The suite that follows is the one carrying a marker, so a gate that leaks the
# name across the boundary attributes BOTH failures to it, finds a marker for
# both, and exits zero on a failure it never read.
report_with_unnameable() {
  printf '%s\n' '<testsuites name="chainsaw-report" tests="2" failures="2">'
  printf '  <testsuite name="kafka" tests="1" failures="1" errors="0" id="0" time="">\n'
  printf '    <testcase %s time="1">\n' "$1"
  printf '      <failure message="exit status 1"></failure>\n'
  printf '    </testcase>\n  </testsuite>\n'
  printf '  <testsuite name="kubernetes-latest" tests="1" failures="1" errors="0" id="0" time="">\n'
  printf '    <testcase name="kubernetes-latest" classname="" time="1">\n'
  printf '      <failure message="exit status 1"></failure>\n'
  printf '    </testcase>\n  </testsuite>\n'
  printf '%s\n' '</testsuites>'
}

@test "a failed testcase the report leaves unnamed blocks instead of dropping out of the list" {
  # The name is the only key a marker can be looked up by, so a failed case with
  # no name is a failure the gate cannot attribute. Enumerating names alone would
  # skip it silently and open on a report whose failures it never finished
  # reading -- the failure mode this counts against the names collected to catch.
  tmp=$(mktemp -d)
  stage_sandbox_stub "$tmp"
  report_with_unnameable 'classname=""' >"$tmp/report"
  echo kubernetes-latest >"$tmp/markers"

  rc=0
  ( set +x; sh "$GATE" sandbox ) >"$tmp/out" 2>&1 || rc=$?

  [ "$rc" -ne 0 ]
  rm -rf "$tmp"
}

@test "a failed testcase with a classname and no name blocks, rather than being read as the classname" {
  # `classname="..."` ends in `name="..."`. A match that does not require the
  # attribute boundary reads the classname as the test name -- and the classname
  # here is the name of the suite that DOES carry a marker, so that misreading
  # opens the gate rather than merely mislabelling a line.
  tmp=$(mktemp -d)
  stage_sandbox_stub "$tmp"
  report_with_unnameable 'classname="kubernetes-latest"' >"$tmp/report"
  echo kubernetes-latest >"$tmp/markers"

  rc=0
  ( set +x; sh "$GATE" sandbox ) >"$tmp/out" 2>&1 || rc=$?

  [ "$rc" -ne 0 ]
  rm -rf "$tmp"
}

@test "a failed testcase named with the empty string blocks, and blocks as one it cannot name" {
  # `name=""` is the shape a pattern written for "a name attribute is present"
  # accepts and a lookup cannot use: it matches and yields nothing.
  #
  # The REASON is asserted, not only the exit. Reading the failed list a line at
  # a time means an empty name survives to the lookup, misses, and blocks as an
  # unexplained test -- so a pattern that accepts it still ends in a red, and an
  # assertion on the exit alone could not tell the two apart. What the exit code
  # cannot say, the message can: a case the report did not name is an accounting
  # failure, and saying so is what sends a reader at the report rather than at a
  # test with a blank name.
  tmp=$(mktemp -d)
  stage_sandbox_stub "$tmp"
  report_with_unnameable 'name="" classname=""' >"$tmp/report"
  echo kubernetes-latest >"$tmp/markers"

  rc=0
  ( set +x; sh "$GATE" sandbox ) >"$tmp/out" 2>&1 || rc=$?

  [ "$rc" -ne 0 ]
  grep -q 'does not name' "$tmp/out"
  rm -rf "$tmp"
}

@test "a failed test whose name is only blanks blocks, instead of dropping out of the loop" {
  # The report is input, not something this gate gets to assume the shape of, so
  # a name of blanks is a name it has to survive. Split into words it disappears:
  # the loop body never runs for it, no marker is looked up, and the run ends
  # green having examined nothing.
  #
  # The REASON is asserted, not only the exit, because two different repairs end
  # in a red here and only one of them is this one. A name that reaches the
  # lookup and misses is reported as a failure the lane does not tolerate; a name
  # mangled on the way there -- by dropping `IFS=`, say, which hands the lookup
  # an empty string -- blocks as well, and an assertion on the exit alone would
  # call that fixed.
  tmp=$(mktemp -d)
  stage_sandbox_stub "$tmp"
  {
    printf '%s\n' '<testsuites name="chainsaw-report" tests="1" failures="1">'
    printf '  <testsuite name="weird" tests="1" failures="1" errors="0" id="0" time="">\n'
    printf '    <testcase name=" " classname="" time="1">\n'
    printf '      <failure message="exit status 1"></failure>\n'
    printf '    </testcase>\n  </testsuite>\n'
    printf '%s\n' '</testsuites>'
  } >"$tmp/report"

  rc=0
  ( set +x; sh "$GATE" sandbox ) >"$tmp/out" 2>&1 || rc=$?

  [ "$rc" -ne 0 ]
  grep -qxF ' ' "$tmp/lookups"
  rm -rf "$tmp"
}

# The lanes that run the Chainsaw suite, split by whether a failed run there
# blocks a merge. Named rather than derived because "does this lane block a
# merge" is not a property any grep over the file can read, and checked against
# the derived set below so that a lane added to the tree has to be placed here
# before either guard passes.
SOFT_LANES=".github/workflows/pull-requests.yaml .github/workflows/e2e-fork.yaml"
HARD_LANES=".github/workflows/nightly.yaml .github/workflows/e2e-tag.yaml"

chainsaw_lanes() {
  grep -lE 'make -C packages/core/testing .*test-chainsaw' .github/workflows/*.yaml | sort
}

@test "the merge-blocking lanes ask this gate and the other two never do" {
  # The tolerance buys one thing -- that a runner degraded enough to miss the
  # node-join deadline does not stand between a correct change and its merge --
  # and it costs the ability to see that failure. So it belongs where a merge is
  # at stake and nowhere else, and the two lanes left hard are hard for reasons
  # of their own.
  #
  # e2e-tag is the release candidate's own e2e check. A soft pass there ships a
  # release whose tenant clusters brought up no worker nodes, which is the one
  # question this suite exists to answer.
  #
  # nightly is the only place an e2e failure reaches anyone who did not open a
  # pull request: no lane in this tree notifies anywhere, so the red job IS the
  # notification, and a soft pass turns it into silence.
  lanes=$(chainsaw_lanes)
  if [ -z "$lanes" ]; then
    echo "found no workflow running the chainsaw suite at all, so this guard checked nothing" >&2
    return 1
  fi
  expected=$(printf '%s\n' $SOFT_LANES $HARD_LANES | sort)
  if [ "$lanes" != "$expected" ]; then
    echo "the workflows running the chainsaw suite are not the ones this guard knows about; a new lane has to be declared as blocking or not before it can run the suite" >&2
    printf 'found:\n%s\nknown:\n%s\n' "$lanes" "$expected" >&2
    return 1
  fi
  for lane in $SOFT_LANES; do
    if ! grep -q 'e2e-node-join-soft-red.sh' "$lane"; then
      echo "$lane blocks merges and runs the chainsaw suite, but never asks hack/e2e-node-join-soft-red.sh, so the same failed run blocks there and not in the other blocking lane" >&2
      return 1
    fi
  done
  for lane in $HARD_LANES; do
    if grep -q 'e2e-node-join-soft-red.sh' "$lane"; then
      echo "$lane tolerates the node-join deadline; it must not -- e2e-tag would pass a release candidate whose workers never joined, and nightly is the only surface that reports an e2e failure to anyone" >&2
      return 1
    fi
  done
}

@test "a soft lane hands the softening decision to whatever it gates on a failed job" {
  # A softened red finishes the job GREEN, so the job's result stops answering
  # "did the suite pass". Two kinds of consumer read it and each breaks its own
  # way: a step gated on `failure()` silently stops running, and a job that
  # REPORTS the result silently starts reporting a pass. Both have to be handed
  # the decision instead of inferring it.
  #
  # The reporting handover is four links, and each is read inside the block it
  # has to live in -- the step with the gate's id, the job that runs the suite,
  # the job that reports its result -- because a link matched anywhere in the
  # file passes while it sits on a neighbour, and the value is then empty for
  # good with nothing to say so. Link 1 is read tighter still: the decision has
  # to be written in the same branch as the exit that makes the job green, and
  # no other exit may leave that block, or a run the gate REFUSED to soften
  # leaves green with nothing recorded.
  #
  # What this does NOT do, and the list is the part to keep honest when this
  # comment is next rewritten, because dropping it is how the previous two
  # versions came to promise more than they held. It cannot tell one reporter
  # from two: a second one added later rides on the strings the first put in its
  # job. It reads the branch as text, so it holds that the softened case is
  # written before the general one and reports success, not that it is the branch
  # GitHub evaluates -- which also means a legitimate refactor into a ternary or
  # a negated guard reds it. It knows nothing of a reader that consumes the
  # result through a composite action or a reusable workflow. Every block here is
  # delimited by indentation, so a shape that indents differently is outside what
  # it can see rather than something it will report. And it reads the step as
  # lines, so an `exit 0` inside a quoted script the step hands to something else
  # counts the same as one of its own.
  for lane in $SOFT_LANES; do
    id=$(awk '/^        id: / { last = $2 } /e2e-node-join-soft-red\.sh/ { print last; exit }' "$lane")
    if [ -z "$id" ]; then
      echo "$lane calls the gate from a step with no id, so nothing downstream can read what the gate decided" >&2
      return 1
    fi
    job=$(awk '/^  [A-Za-z0-9_-]+:$/ { j = substr($1, 1, length($1) - 1) }
               /make -C packages\/core\/testing .*test-chainsaw/ { print j; exit }' "$lane")
    if [ -z "$job" ]; then
      echo "could not find which job in $lane runs the chainsaw suite; without it this guard reports success for having lost its input" >&2
      return 1
    fi
    # Link 1, and the exit it has to travel with. Taken from the step's own first
    # line rather than from its id, so a key written above the id is inside the
    # block too, and ended at the next step whatever key THAT one leads with: a
    # step led by `id:` or `uses:` is a step, and a terminator that only knows
    # `- name:` reads it as more of this one and accepts a write that belongs to
    # somebody else.
    id_line=$(grep -n "^        id: ${id}\$" "$lane" | head -n 1 | cut -d: -f1)
    step_start=$(awk -v i="$id_line" 'NR <= i && /^      - / { l = NR } END { print l }' "$lane")
    if [ -z "$step_start" ]; then
      echo "could not find where step ${id} of $lane begins; without that boundary the checks below cannot tell what is inside it" >&2
      return 1
    fi
    step=$(awk -v s="$step_start" 'NR < s { next } NR > s && /^      - / { exit } { print }' "$lane")
    # A step that cannot fail the job cannot block on a red the gate refused to
    # soften either: it would go green with nothing recorded, and the status
    # would call that run a pass. Read as a key with a value rather than as the
    # word `true`, because the value may be an expression, and one that reads
    # `${{ github.event_name == 'pull_request' }}` is `true` on this lane.
    coe=$(printf '%s\n' "$step" | grep -E '^ *continue-on-error:' | head -n 1)
    case "${coe##*: }" in
      ""|false) ;;
      *)
        echo "step ${id} in $lane carries continue-on-error (${coe# }), so a failure the gate REFUSED to soften leaves the job green with nothing recorded and the status calls it a pass" >&2
        return 1
        ;;
    esac
    gate_i=$(printf '%s\n' "$step" | grep -n 'e2e-node-join-soft-red\.sh' | head -n 1 | cut -d: -f1)
    if [ -z "$gate_i" ]; then
      echo "step ${id} in $lane does not call the gate, so the id this guard followed is not the step that decides" >&2
      return 1
    fi
    gate_indent=$(printf '%s\n' "$step" | sed -n "${gate_i}p" | sed 's/[^ ].*//')
    end_i=$(printf '%s\n' "$step" | awk -v s="$gate_i" -v ind="$gate_indent" 'NR > s && $0 == ind "fi" { print NR; exit }')
    if [ -z "$end_i" ]; then
      echo "the gate's branch in step ${id} of $lane does not close where this can see it; without that boundary the checks below cannot tell what is inside it" >&2
      return 1
    fi
    branch=$(printf '%s\n' "$step" | awk -v a="$gate_i" -v b="$end_i" 'NR > a && NR < b')
    # Both spellings of the sink: `${GITHUB_OUTPUT}` is the same write. What is
    # NOT the same write is $GITHUB_ENV, which reaches the same job and publishes
    # nothing.
    write=$(printf '%s\n' "$branch" | grep -F 'soft_red=true' | head -n 1)
    if ! printf '%s\n' "$write" | grep -q 'GITHUB_OUTPUT'; then
      echo "the branch the gate opens in step ${id} of $lane does not write soft_red=true to \$GITHUB_OUTPUT; whatever it writes instead is invisible to the job output below it, so everything downstream reads this run as one where the suite passed" >&2
      return 1
    fi
    # And writes it whenever that branch is taken. A write put under a condition
    # of its own -- scoped to full runs, say -- records the decision on some
    # softened runs and not others, and the ones it skips go green with nothing
    # recorded, which reads downstream as a suite that passed. Held as a shape
    # rather than by parsing: a bare statement at the branch's own indentation.
    # A write spelled some third way is outside this and reds rather than being
    # judged.
    case "$write" in
      "${gate_indent}  echo "*|"${gate_indent}  printf "*) ;;
      *)
        echo "the write in step ${id} of $lane is not a bare statement in the branch the gate opens (${write}); a conditional or nested write records the decision on some runs and not others, and a softened run it skipped is reported as a pass" >&2
        return 1
        ;;
    esac
    if ! printf '%s\n' "$branch" | grep -qE '^ *exit 0$'; then
      echo "the branch the gate opens in step ${id} of $lane records the decision and does not exit zero, so the record is written for a run that goes on to fail anyway" >&2
      return 1
    fi
    # In that order. A write standing after the exit is a write that never runs,
    # and every check above it passes on a branch that records nothing.
    write_i=$(printf '%s\n' "$branch" | grep -nF 'soft_red=true' | head -n 1 | cut -d: -f1)
    exit_i=$(printf '%s\n' "$branch" | grep -nE '^ *exit 0$' | head -n 1 | cut -d: -f1)
    if [ "$write_i" -gt "$exit_i" ]; then
      echo "the branch the gate opens in step ${id} of $lane exits before it records the decision, so the record is never written and the run goes green carrying nothing" >&2
      return 1
    fi
    # Outside that branch the step may do neither of the two things the handover
    # rests on. Both are asked as "not at all" rather than "not this spelling",
    # because a spelling is what an edit changes: the sink is named, not the
    # value written to it, and an exit is judged by its operand rather than by
    # matching the digit zero -- `exit $rc` is a green exit on every run where
    # that variable is zero, and the decision can be assembled from a variable
    # just as easily.
    outside=$(printf '%s\n' "$step" | awk -v a="$gate_i" -v b="$end_i" 'NR <= a || NR >= b')
    if printf '%s\n' "$outside" | grep -q 'GITHUB_OUTPUT'; then
      echo "step ${id} in $lane writes to the output sink outside the branch the gate opens; a run the gate never softened would carry a decision anyway, and the status would report a passing suite as one that missed the deadline" >&2
      return 1
    fi
    green=$(printf '%s\n' "$outside" | grep -oE '(^|[^[:alnum:]_])exit[[:space:]]+[^;&|)]*' \
      | grep -vE 'exit[[:space:]]+[1-9][0-9]*$' | head -n 1)
    if [ -n "$green" ]; then
      echo "step ${id} in $lane can leave without a red exit outside the branch the gate opens (${green# }); a failure the gate REFUSED to soften would then go green with nothing recorded, and the status would call it a pass" >&2
      return 1
    fi
    # And what the branch falls through to has to be a non-zero exit written
    # out. Falling out of the block instead reaches whatever the step ends with,
    # which on a step that ends by announcing success is a green exit; and an
    # exit through a variable is a green exit whenever that variable is zero.
    # The first EXIT after the branch, not the first line: a comment or a line
    # of logging between the two leaves the property untouched, and a check that
    # reds on one is a check somebody switches off.
    after=$(printf '%s\n' "$step" | awk -v b="$end_i" -v ind="$gate_indent" 'NR > b && $0 ~ "^" ind "exit " { print; exit }')
    case "$after" in
      "${gate_indent}exit "[1-9]*) ;;
      *)
        echo "the branch the gate opens in step ${id} of $lane does not fall through to a written non-zero exit (${after:-nothing follows it}); a failure the gate refused to soften would then leave by whatever ends the step, and a step that ends by announcing success ends green" >&2
        return 1
        ;;
    esac
    # Link 2: the job that ran the suite publishes that step's decision, read
    # inside that job's block.
    jobblock=$(awk -v want="$job" '
      $0 ~ "^  " want ":$" { inj = 1; next }
      inj && /^  [A-Za-z0-9_-]+:$/ { exit }
      inj { print }' "$lane")
    # The same question one level up, and it is not the same answer: a job that
    # cannot fail reports `success` to everything that reads its result, so the
    # step's own exit stops meaning anything at all.
    jcoe=$(printf '%s\n' "$jobblock" | grep -E '^    continue-on-error:' | head -n 1)
    case "${jcoe##*: }" in
      ""|false) ;;
      *)
        echo "job ${job} in $lane carries continue-on-error (${jcoe# }), so its result is success even when it failed, and a red the gate refused to soften reaches the status as a pass" >&2
        return 1
        ;;
    esac
    if ! printf '%s\n' "$jobblock" | grep -q '^    outputs:$'; then
      echo "job ${job} in $lane declares no outputs at all, so step ${id}'s decision stops at the job boundary" >&2
      return 1
    fi
    if ! printf '%s\n' "$jobblock" | grep -vE '^ *#' | grep -qF "      soft_red: \${{ steps.${id}.outputs.soft_red }}"; then
      echo "job ${job} in $lane does not publish step ${id}'s decision as its own output; an output on another job, or one naming a step that does not exist, resolves to the empty string in silence" >&2
      return 1
    fi
    # Links 3 and 4 live in the job that reports the result, so they are read
    # there. Found by which job reads that result rather than by name.
    # The reporter is the job that reads the suite's result AND publishes the
    # check a merge waits on, not merely the first job that mentions the result:
    # a notify or cleanup job keyed on the same result is not what a reader sees
    # in front of the merge button. `E2E Tests` is that check's name, and the
    # only literal here about another file.
    reporter=""
    for candidate in $(awk '/^  [A-Za-z0-9_-]+:$/ { print substr($1, 1, length($1) - 1) }' "$lane"); do
      cblock=$(awk -v want="$candidate" '
        $0 ~ "^  " want ":$" { inj = 1; next }
        inj && /^  [A-Za-z0-9_-]+:$/ { exit }
        inj { print }' "$lane")
      if printf '%s\n' "$cblock" | grep -qF "needs.${job}.result" \
        && printf '%s\n' "$cblock" | grep -qE "context: ['\"]E2E Tests['\"]"; then
        reporter="$candidate"
        break
      fi
    done
    if [ -z "$reporter" ]; then
      echo "no job in $lane both reads the ${job} job's result and publishes the E2E Tests check, so nothing in it can carry the softening decision to what a merge waits on" >&2
      return 1
    fi
    repblock=$(awk -v want="$reporter" '
      $0 ~ "^  " want ":$" { inj = 1; next }
      inj && /^  [A-Za-z0-9_-]+:$/ { exit }
      inj { print }' "$lane")
    seen_as=$(printf '%s\n' "$repblock" | grep -F "needs.${job}.outputs.soft_red" | grep -E '^ *[A-Z0-9_]+:' | head -n 1)
    seen_as=$(printf '%s' "${seen_as%%:*}" | tr -d ' \t')
    if [ -z "$seen_as" ]; then
      echo "job ${reporter} in $lane reports the ${job} job's result without reading what that job decided; a red the lane tolerated finishes green, so that report would tell a reader the suite passed on a run where it failed" >&2
      return 1
    fi
    result_as=$(printf '%s\n' "$repblock" | grep -F "needs.${job}.result" | grep -E '^ *[A-Z0-9_]+:' | head -n 1)
    result_as=$(printf '%s' "${result_as%%:*}" | tr -d ' \t')
    if [ -z "$result_as" ]; then
      echo "could not read the name job ${reporter} in $lane knows the ${job} result by; without it the ordering check below has nothing to order against and would report success for having lost its input" >&2
      return 1
    fi
    # Link 4: and branches on it, ahead of the branch that maps a successful
    # result to "the suite passed", and to a success of its own. Comment lines
    # are excluded on both sides: a branch named in a comment is not a branch.
    soft_line=$(printf '%s\n' "$repblock" | grep -nF "${seen_as} === 'true'" | grep -vE ':[[:space:]]*//' | head -n 1 | cut -d: -f1)
    plain_line=$(printf '%s\n' "$repblock" | grep -nF "${result_as} === 'success'" | grep -vF "${seen_as}" | grep -vE ':[[:space:]]*//' | head -n 1 | cut -d: -f1)
    if [ -z "$soft_line" ] || [ -z "$plain_line" ]; then
      echo "job ${reporter} in $lane passes ${seen_as} to its reporting script and never branches on it, so the status it publishes is the one it published before the softening existed" >&2
      return 1
    fi
    if [ "$soft_line" -gt "$plain_line" ]; then
      echo "job ${reporter} in $lane branches on ${seen_as} behind the branch that already answers a successful result; the softened case is written and never reached" >&2
      return 1
    fi
    verdict=$(printf '%s\n' "$repblock" | awk -v s="$soft_line" 'NR > s && /^ *}/ { exit } NR > s && /state = / { print; exit }')
    case "$verdict" in
      *"'success'"*) ;;
      *)
        echo "job ${reporter} in $lane branches on ${seen_as} and that branch does not report success (${verdict:-no verdict in its body}); the lane decided not to block, and the check a merge waits on has to say so" >&2
        return 1
        ;;
    esac
  done
  # The step side, and the one step that stands on `failure()` today. The SSH
  # breakpoint is the only interactive way into a sandbox whose node-join never
  # finished -- the failure this lane stopped blocking on -- so skipping it there
  # would take the one debugging path away on exactly the run it was built for.
  checked=0
  for lane in $SOFT_LANES; do
    if ! grep -q 'name: Breakpoint on E2E failure' "$lane"; then
      continue
    fi
    checked=$((checked + 1))
    id=$(awk '/^        id: / { last = $2 } /e2e-node-join-soft-red\.sh/ { print last; exit }' "$lane")
    block=$(awk '/name: Breakpoint on E2E failure/ { inb = 1; next }
                 inb && /^        uses: / { exit }
                 inb { print }' "$lane")
    case "$block" in
      *"steps.${id}.outputs.soft_red"*) ;;
      *)
        echo "the breakpoint in $lane opens only on a failed job, and a softened node-join red is a job that succeeded; it would be skipped on the one failure worth attaching to" >&2
        return 1
        ;;
    esac
    # And it must not be able to fail the job it now runs inside. On the
    # softened path everything else has already succeeded, so this step is the
    # only thing left that could turn the lane's decision not to block into a
    # block, decided by a debugging tool rather than by the tests.
    case "$block" in
      *"continue-on-error: true"*) ;;
      *)
        echo "the breakpoint in $lane can fail the job, and on a softened red that is a job which had decided not to block; a debugging session that cannot open would then be what blocks the merge" >&2
        return 1
        ;;
    esac
  done
  if [ "$checked" -eq 0 ]; then
    echo "no lane that tolerates the node-join deadline carries a breakpoint step any more; this guard now checks nothing and should go with the step it was written for" >&2
    return 1
  fi
}

@test "the e2e job cap covers the kubernetes pair at its worst, and every lane carries the same one" {
  # What this holds is the part that can be read out of source: the two bringup
  # Tests run sequentially (parallel: 1), each can spend its whole operation
  # ceiling and then the failure catch behind it, and the job cap has to sit
  # above that or the run ends `cancelled` with cozyreport.tgz and the job log
  # both lost.
  #
  # What it does NOT hold, stated because the cap in the workflows is larger
  # than this check needs and a reader is owed the reason. The full worst case
  # of those suites counts their `finally` legs and the storageclass-fallback
  # Test beside them too: 67m operation, 19m catch and a 10m `finally` for each
  # of the two bringup Tests, plus 15m and a 19m catch for the fallback Test,
  # which is 226 minutes. No cap in this file has ever covered that sum -- the
  # same arithmetic came to 192 against the previous 180-minute cap -- and one
  # that did would be a cap that never fires.
  #
  # The margin above this bound is empirical, and this is the measurement: three
  # nightlies that failed BOTH bringup Tests ran their full-suite E2E job in 149,
  # 158 and 160 minutes under the previous ceilings (runs 32324686063,
  # 32208653925, 32348997754). A run of that shape is the one that gains here,
  # and it gains at most 12 minutes per suite -- 11 from the node-join deadline,
  # 1 from the diagnostics phase budget -- so the top of that range projects to
  # about 184 minutes, which the cap clears by about 30 where the old one cleared
  # 160 by 20. The scoped lanes run a subset of that suite, so the full run is
  # the one worth sizing against.
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  op=$(grep -oE '^COZY_OP_CEILING=[0-9]+' "$lib" | head -n 1 | sed -E 's/.*=//')
  # Both catch legs, read from the ops that carry them rather than restated: the
  # previous-instance logs and the host snapshot each declare their own ceiling
  # in the global config, and they run one after the other on a failed test.
  #
  # The sum reads whole minutes only, so a leg written in any other unit would
  # be dropped from it silently and priced at zero. Counting the legs against
  # the ones this could add up is what turns that into a red.
  config=hack/e2e-chainsaw/.chainsaw.yaml
  legs=$(grep -cE '^        timeout: ' "$config" || true)
  priced=$(grep -cE '^        timeout: [0-9]+m$' "$config" || true)
  if [ "$legs" != "$priced" ]; then
    echo "a catch leg in $config declares its ceiling in something other than whole minutes ($legs legs, $priced this can add up); the sum below would price the rest at zero" >&2
    return 1
  fi
  catch=$(grep -oE '^        timeout: [0-9]+m$' "$config" \
    | grep -oE '[0-9]+' | awk '{ total += $1 } END { print total }')
  for v in op catch; do
    eval "n=\$$v"
    if [ -z "$n" ] || [ "$n" -lt 1 ]; then
      echo "expected to read $v from source; without it this guard reports success for having lost its input" >&2
      return 1
    fi
  done
  worst=$(( 2 * ((op / 60) + catch) ))
  # The cap of the job that runs chainsaw, not every cap in the file: take the
  # last one declared before the chainsaw invocation, which is the enclosing
  # job's. Picking by value or by position in the file would be right only until
  # a job is added above. Derived over every lane that runs the suite, so a new
  # one is held to the same figure without being listed here.
  caps=$(for lane in $(chainsaw_lanes); do
    awk '/^    timeout-minutes: [0-9]+$/ { cap = $2 }
         /make -C packages\/core\/testing .*test-chainsaw/ { if (cap) print cap; exit }' "$lane"
  done | sort -u)
  # One figure across all of them, so a lane cannot be raised alone and leave the
  # others killing the same run they were meant to survive. The split in the
  # guard above does not reach this: a hard lane runs the same suites for the
  # same time and only decides differently at the end.
  if [ "$(printf '%s\n' "$caps" | grep -c .)" -ne 1 ]; then
    echo "the e2e lanes carry different job caps ($(printf '%s ' $caps)); the same failed run would then be cancelled in one lane and finish in another" >&2
    return 1
  fi
  if [ "$caps" -lt "$worst" ]; then
    echo "the e2e job cap is ${caps}m against a kubernetes pair that can spend ${worst}m on its own (2 x (${op}s ceiling + ${catch}m catch)); the cap would kill the run and take cozyreport.tgz and the job log with it" >&2
    return 1
  fi
}
