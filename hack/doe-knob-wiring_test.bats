#!/usr/bin/env bats
# Holds the places a dispatch-time knob has to appear together, so that a knob
# wired into one of them and not the others cannot go unnoticed.
#
# A knob is read inside the sandbox container, by the Chainsaw suite's fixture
# or by the bringup step. It reaches that container only if
# packages/core/testing/Makefile forwards it on `docker run`, and it reaches
# that Makefile only if the lane dispatching the run puts it in the job
# environment. Miss the middle link and every run silently uses the default
# while its dispatch says otherwise -- which is the one outcome that turns a
# series of runs into a series of measurements of the same thing under
# different labels.
#
# The knob NAMES are derived rather than listed, so a knob added to one of the
# readers below is covered on the day it is added. The readers themselves are
# two hardcoded paths, which is the limit of that: a knob read by a third script
# is invisible here until that script joins READERS. What makes even the name
# derivation possible is a naming rule, and the rule is load-bearing: a knob a
# run is dispatched with is named COZY_E2E_*, and a seam that exists only so a
# test can point a script at a staged file is named something else. Break that
# rule and this file compares the wrong sets while still passing.
#
# What it does not check is that a forwarded knob has any effect. That belongs
# to the suites covering each reader: hack/run-kubernetes-fixture_test.bats for
# the worker sizing and hack/halt-poll_test.bats for the polling window.

READERS="hack/e2e-chainsaw/_lib/run-kubernetes.sh hack/e2e-halt-poll.sh"
FORWARDER=packages/core/testing/Makefile
LANE=.github/workflows/e2e-experiment.yaml

# `|| :` on the extractions below: grep exits 1 when it selects nothing, and
# an empty set is a reading this file acts on rather than an error to abort on.
# Each caller checks for emptiness itself and says which side was empty.
knob_names() {
  # shellcheck disable=SC2086  # the file list is a word list on purpose
  grep -ohE 'COZY_E2E_[A-Z0-9_]+' $1 2>/dev/null | sort -u || :
  }

@test "the suite reads at least one dispatch knob" {
  # Without this the comparisons below are satisfied by empty sets.
  read_names=$(knob_names "${READERS}")
  [ -n "${read_names}" ]
}

@test "every knob the suite reads is forwarded into the sandbox container" {
  read_names=$(knob_names "${READERS}")
  forwarded=$(grep -oE '[-]e COZY_E2E_[A-Z0-9_]+' "${FORWARDER}" | sed 's/^-e //' | sort -u || :)
  if [ "${read_names}" != "${forwarded}" ]; then
    echo "the knobs the suite reads and the knobs docker run forwards are not the same set; a knob missing from the forwarder reads as its default in every run" >&2
    printf 'read by the suite:\n%s\nforwarded:\n%s\n' "${read_names}" "${forwarded}" >&2
    return 1
  fi
}

@test "every forwarded knob is set by the experiment lane" {
  forwarded=$(grep -oE '[-]e COZY_E2E_[A-Z0-9_]+' "${FORWARDER}" | sed 's/^-e //' | sort -u || :)
  in_lane=$(knob_names "${LANE}")
  if [ "${forwarded}" != "${in_lane}" ]; then
    echo "the knobs docker run forwards and the knobs the experiment lane sets are not the same set; a knob the lane never sets cannot be varied by a dispatch" >&2
    printf 'forwarded:\n%s\nset by the lane:\n%s\n' "${forwarded}" "${in_lane}" >&2
    return 1
  fi
}

@test "each knob the lane sets comes from a dispatch input" {
  # A knob pinned to a literal in the lane is worse than an absent one: the
  # dispatch form offers a field that changes nothing.
  for name in $(knob_names "${LANE}"); do
    if ! grep -qE "^ +${name}: \\\$\{\{ inputs\.[a-z_]+ \}\}\$" "${LANE}"; then
      echo "${name} is not assigned from a dispatch input in ${LANE}" >&2
      return 1
    fi
  done
}

@test "the lane puts the forwarder back after staging the published tree" {
  # The published artifact carries the whole packages tree, the forwarder
  # included, so the staging copy replaces it with the pinned build's copy. A
  # build published before a knob existed does not forward that knob, and the
  # run then measures the default while printing the dispatched value. Order is
  # the whole of it, so both lines are located and compared.
  stage=$(grep -n 'cp -r _rw/\. packages/' "${LANE}" | head -1 | cut -d: -f1)
  restore=$(grep -n "git checkout -- ${FORWARDER}" "${LANE}" | head -1 | cut -d: -f1)
  if [ -z "${stage}" ] || [ -z "${restore}" ]; then
    echo "the lane stages the published packages tree without putting ${FORWARDER} back, so a build older than a knob silently drops it" >&2
    return 1
  fi
  [ "${restore}" -gt "${stage}" ]
}

@test "the lane runs one named suite and takes it from nobody" {
  # The suite is a constant of the experiment rather than a knob: a series
  # compares settings against one workload, and the second Kubernetes suite
  # runs after the first on the same host, which makes it a repeated measure.
  # Being a constant is also what keeps operator text out of the shell recipe
  # this value is interpolated into.
  grep -qE 'CHAINSAW_SUITES="[a-z0-9-]+"' "${LANE}"
  if grep -q 'inputs.chainsaw_suites' "${LANE}"; then
    echo "the suite is dispatched again; this lane fixes it on purpose" >&2
    return 1
  fi
}

@test "the experiment lane is started by a dispatch and by nothing else" {
  # The lane is deliberately absent from select-e2e.sh's escalation list, and
  # what makes that safe is that no push and no pull request starts it. A
  # trigger added later would leave it inert to the selector while CI fired it
  # by itself, which is the silent-inert shape that list exists to prevent. The
  # comment there calls this a property to check on the file; this is that
  # check, rather than a second place asking to be remembered.
  triggers=$(awk '
    /^on:/            { inside = 1; next }
    inside && /^[^[:space:]]/ { exit }
    inside && /^  [a-z_]+:/   { gsub(/[ :]/, ""); print }
  ' "${LANE}")
  if [ "${triggers}" != "workflow_dispatch" ]; then
    echo "the experiment lane's triggers are '${triggers}' rather than workflow_dispatch alone; a lane CI starts by itself belongs in select-e2e.sh's escalation list" >&2
    return 1
  fi
}

@test "the lane checks a dispatched value whole, not one line of it" {
  # A line-oriented matcher used as a validator accepts a value whose first
  # line is clean and passes the rest to whatever consumes it. Every check here
  # is a shell `case`, which compares the entire value, and the absence of the
  # other form is asserted because that is the shape the defect takes.
  grep -q 'case "${setting#\*=}" in' "${LANE}"
  grep -q 'case "${VERSION}" in' "${LANE}"
  if grep -qE "grep -q[A-Za-z]* '\^" "${LANE}"; then
    echo "a dispatched value is validated line by line in ${LANE}" >&2
    return 1
  fi
}
