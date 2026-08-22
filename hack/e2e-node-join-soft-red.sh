#!/bin/sh
# Decide whether a failed Chainsaw run may leave the job green.
#
# Exactly one e2e failure is tolerated: the tenant node-join deadline in
# hack/e2e-chainsaw/_lib/run-kubernetes.sh. That wait stopped discriminating --
# nested virtualisation on the shared runners degrades far enough that a worker
# which registers in minutes elsewhere can miss any deadline the suite can
# afford -- so a red there says as much about the machine as about the product,
# and what it produces is re-runs.
#
# The softening lives HERE, one layer above the suite, and not in the suite,
# which is the whole design. The test still fails: Chainsaw records a failure,
# its catch runs in full (previous-instance container logs, the host snapshot,
# the data-plane capture, the event dump) and the JUnit report says so. A
# softening inside the script would have bought a green check by throwing all of
# that away, because every one of those collectors is keyed on the test failing.
#
# What changes is the lane's verdict -- and, with it, anything the lane gates on
# a failed job. Such a step stops firing on a softened run unless the lane hands
# it the decision separately, which is a coupling to look for rather than to
# assume; the guard file below holds the one that exists today.
#
# The discriminator is a marker file the failing test writes for exactly this
# purpose, never an inference about which suite failed or what its log said. A
# marker is written only on the deadline expiring -- not on the wait failing to
# run, and not on any assertion after the join.
#
# Every branch that cannot answer the question answers "block". A report that
# cannot be read, a failed test the report does not name, a failed test whose
# name no marker matches, a run with no failures recorded at all: each of those
# is a red this must not tolerate, because the one thing worse than a flaky gate
# is a gate that opens when it does not understand what it is looking at. That
# holds branch by branch and is not a claim that every shape has been thought of;
# what makes it hold is that reaching the tolerating exit takes a name, a lookup
# and an answer, so a name that goes missing anywhere between the report and the
# lookup ends up in the unexplained list rather than out of the accounting.
#
# Not every lane that runs the suite asks this. Which ones do, and why the rest
# stay hard, is held in hack/node-join-soft-red_test.bats.
#
# Usage: hack/e2e-node-join-soft-red.sh <sandbox-container>
#   exit 0  every recorded failure is a node-join deadline; the lane may pass
#   exit 1  something else failed, or the question could not be answered
set -eu

SANDBOX=${1:?usage: e2e-node-join-soft-red.sh <sandbox-container>}
# Both paths are the ones the sandbox itself uses: the report name comes from
# `report.name` in hack/e2e-chainsaw/.chainsaw.yaml and lands beside the suites,
# and the snapshot root is spelled the way _lib/run-kubernetes.sh spells it.
#
# The override in that expression is read in the SANDBOX's environment, not the
# runner's: `docker exec` starts a process with the container's environment and
# passes none of its own, so a COZY_REPORT_DIR exported beside this script has
# no effect here at all. Nothing in this repository exports it, so both sides
# take the default and agree by default rather than by coupling.
REPORT=/workspace/hack/e2e-chainsaw/chainsaw-report.xml
SNAPSHOT_ROOT_EXPR='${COZY_REPORT_DIR:-/workspace/_out/cozyreport}/snapshots'

xml=$(docker exec "$SANDBOX" sh -c "cat '$REPORT'" 2>/dev/null) || xml=
if [ -z "$xml" ]; then
  echo "the chainsaw report is missing or empty, so which tests failed is unknown; treating this run as a failure" >&2
  exit 1
fi

# One record per testcase, so a failure element can only be attributed to the
# testcase it is nested in. Splitting on the element rather than matching a line
# is what keeps a `failures="N"` attribute on the enclosing testsuite -- which
# carries no `<` -- from reading as a failed case.
#
# The name is how the marker below is looked up, so a failed case this cannot
# name is a failure it cannot attribute. Those are counted rather than skipped,
# and a tally that does not match the names collected ends the run here: without
# it an unnamed or empty-named failure drops silently out of the list and the
# gate opens on a failure it never read.
#
# The name is read out of the element's opening tag and NOT out of the record,
# which is the difference between attributing a failure and guessing at one. A
# record runs to the NEXT `<testcase`, so it carries the `</testsuite>` that
# closes this suite and the `<testsuite name="...">` that opens the following
# one; searching all of it lets an unnamed case inherit its neighbour's suite
# name, and the tally above then sees a case that is "named" and says nothing. A
# record with no `>` at all yields an empty head, no match, and a block.
#
# Inside that head the leading space is load-bearing for the same class of
# reason: `classname="..."` ends in `name="..."`, so without it a case carrying a
# classname and no name reports the classname as the test to look a marker up by.
failed=$(printf '%s\n' "$xml" | awk '
  BEGIN { RS = "<testcase" }
  NR == 1 { next }
  $0 ~ /<(failure|error)[ >\/]/ {
    seen++
    head = substr($0, 1, index($0, ">"))
    if (match(head, / name="[^"]+"/)) {
      print substr(head, RSTART + 7, RLENGTH - 8)
      named++
    }
  }
  END { if (seen != named) exit 3 }
') || {
  echo "the chainsaw report records a failed test it does not name, so the marker that would explain it cannot be looked up; that is a failure this cannot attribute, so it blocks" >&2
  exit 1
}

if [ -z "$failed" ]; then
  echo "the chainsaw run did not pass, yet its report records no failed test; that is a failure this cannot attribute, so it blocks" >&2
  exit 1
fi

# One name per line, read as a line rather than split into words. The report is
# input to this decision, not something it may assume the shape of, and word
# splitting silently DROPS a name made only of blanks: the loop body would not
# run for it, no marker would be looked up, and a failure nobody examined would
# leave the run green. Read as a line it survives, finds no marker, and blocks.
# Splitting also expands a name carrying `*` or `?` against the runner's working
# directory, which turns one unexplained failure into a handful of filenames.
#
# The lookup reads from /dev/null as a guard on what may be put in this loop
# later, not against anything measured: `docker exec` without `-i` leaves the
# list alone, and a command that did read stdin would eat it silently.
unexplained=
while IFS= read -r test_name; do
  # The name goes in as an argument rather than being pasted into the script
  # text: interpolating it would put report content inside a command the sandbox
  # then parses, and the quoting that holds for a Chainsaw Test name holds only
  # because of what such a name is allowed to contain.
  if docker exec "$SANDBOX" sh -c \
    "[ -f \"$SNAPSHOT_ROOT_EXPR/\$1/SOFT-RED-node-join.txt\" ]" \
    soft-red "$test_name" </dev/null 2>/dev/null; then
    echo "» ${test_name}: failed on the node-join deadline, which the lane does not block on"
    continue
  fi
  unexplained="${unexplained} ${test_name}"
done <<EOF
${failed}
EOF

if [ -n "$unexplained" ]; then
  echo "these tests failed for reasons the lane does not tolerate:${unexplained}" >&2
  exit 1
fi

# Deliberately not an annotation. The failing test already wrote one naming the
# deadline; a second one here would report the same run twice, and this line is
# about a different fact -- what the lane decided -- which belongs in the log
# beside the decision.
echo "» every failed test was the node-join deadline; the job is not blocked on it, and the marker beside each test's diagnostics in cozyreport.tgz records that this run did not prove what it set out to"
