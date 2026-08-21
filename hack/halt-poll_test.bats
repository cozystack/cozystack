#!/usr/bin/env bats
# Covers hack/e2e-halt-poll.sh, which records -- and on request changes -- the
# KVM halt-polling window of the kernel that hosts the sandbox VMs.
#
# The script exists for one property, and it is the property these tests are
# about: a run that asked for a value it did not get must fail loudly rather
# than proceed. A sandbox that silently kept the kernel default while the run
# was labelled as having changed it produces a measurement of the default under
# another name, which is worse than no measurement.
#
# Both the parameter file and the record are seams, so every arm here runs
# against a staged file. Nothing in this suite reads or writes the real
# /sys/module/kvm/parameters/halt_poll_ns.
#
# The "did not stick" arm points the script at /dev/null, where a write is
# accepted and the value is not there afterwards. That is the shape a kernel
# parameter takes when the module ignores what it was given, and it is the one
# arm that cannot be staged with an ordinary file, since a write to a file the
# script then reads always sticks. It is also why this suite does not test an
# unwritable file: denying a write with permissions says nothing on a runner
# that happens to be root, and the dangerous case is the write that succeeds.

HALT_POLL=hack/e2e-halt-poll.sh

@test "the read-only path records the value the parameter holds" {
  tmp=$(mktemp -d)
  printf '200000\n' >"${tmp}/halt_poll_ns"
  COZY_HALT_POLL_FILE="${tmp}/halt_poll_ns" \
  COZY_HALT_POLL_RECORD="${tmp}/record.txt" \
    sh "${HALT_POLL}"
  grep -q 'value before: 200000' "${tmp}/record.txt"
  grep -q 'requested: none' "${tmp}/record.txt"
  # Untouched: the read-only path is what the campaign's baseline row runs, and
  # a baseline that moved the parameter would not be the baseline.
  grep -q '^200000$' "${tmp}/halt_poll_ns"
}

@test "the read-only path records an unreadable parameter instead of failing" {
  tmp=$(mktemp -d)
  COZY_HALT_POLL_FILE="${tmp}/absent" \
  COZY_HALT_POLL_RECORD="${tmp}/record.txt" \
    sh "${HALT_POLL}"
  grep -q 'value before: not readable' "${tmp}/record.txt"
}

@test "a requested value is written and recorded on both sides of the write" {
  tmp=$(mktemp -d)
  printf '200000\n' >"${tmp}/halt_poll_ns"
  COZY_HALT_POLL_FILE="${tmp}/halt_poll_ns" \
  COZY_HALT_POLL_RECORD="${tmp}/record.txt" \
  COZY_E2E_HALT_POLL_NS=0 \
    sh "${HALT_POLL}"
  grep -q '^0$' "${tmp}/halt_poll_ns"
  grep -q 'value before: 200000' "${tmp}/record.txt"
  grep -q 'requested: 0' "${tmp}/record.txt"
  grep -q 'value after: 0' "${tmp}/record.txt"
}

@test "a request the parameter already holds is not offered as the baseline" {
  # The bringup is retried, and a later attempt runs this step again on a
  # machine an earlier one already wrote to. Reading back our own value and
  # printing it plainly would hand the campaign this run's setting as the
  # baseline it is meant to be compared against.
  tmp=$(mktemp -d)
  printf '50000\n' >"${tmp}/halt_poll_ns"
  COZY_HALT_POLL_FILE="${tmp}/halt_poll_ns" \
  COZY_HALT_POLL_RECORD="${tmp}/record.txt" \
  COZY_E2E_HALT_POLL_NS=50000 \
    sh "${HALT_POLL}"
  grep -q 'value before: 50000 (already the requested value' "${tmp}/record.txt"
}

@test "a request that changes the value carries no already-there note" {
  # Without this the branch above is satisfied by an annotation that is always
  # printed, which would say nothing about anything.
  tmp=$(mktemp -d)
  printf '200000\n' >"${tmp}/halt_poll_ns"
  COZY_HALT_POLL_FILE="${tmp}/halt_poll_ns" \
  COZY_HALT_POLL_RECORD="${tmp}/record.txt" \
  COZY_E2E_HALT_POLL_NS=0 \
    sh "${HALT_POLL}"
  if grep -q 'already the requested value' "${tmp}/record.txt"; then
    echo "the already-there note appeared on a request that changed the value" >&2
    return 1
  fi
}

@test "a requested value that does not stick fails the step" {
  tmp=$(mktemp -d)
  rc=0
  COZY_HALT_POLL_FILE=/dev/null \
  COZY_HALT_POLL_RECORD="${tmp}/record.txt" \
  COZY_E2E_HALT_POLL_NS=50000 \
    sh "${HALT_POLL}" >"${tmp}/out" 2>"${tmp}/err" || rc=$?
  [ "${rc}" -ne 0 ]
  grep -q 'did not stick' "${tmp}/err"
  # Both figures, because a message naming only the one that was asked for
  # leaves the reader unable to tell a rejected write from a clamped one.
  grep -q '50000' "${tmp}/err"
  grep -q 'reads back as' "${tmp}/err"
}

@test "a requested value fails the step when the parameter is not there" {
  tmp=$(mktemp -d)
  rc=0
  COZY_HALT_POLL_FILE="${tmp}/absent" \
  COZY_HALT_POLL_RECORD="${tmp}/record.txt" \
  COZY_E2E_HALT_POLL_NS=0 \
    sh "${HALT_POLL}" >"${tmp}/out" 2>"${tmp}/err" || rc=$?
  [ "${rc}" -ne 0 ]
  grep -q 'not readable' "${tmp}/err"
}

@test "a request that is not a plain number is refused before anything is written" {
  tmp=$(mktemp -d)
  printf '200000\n' >"${tmp}/halt_poll_ns"
  rc=0
  COZY_HALT_POLL_FILE="${tmp}/halt_poll_ns" \
  COZY_HALT_POLL_RECORD="${tmp}/record.txt" \
  COZY_E2E_HALT_POLL_NS='50000; touch /tmp/nope' \
    sh "${HALT_POLL}" >"${tmp}/out" 2>"${tmp}/err" || rc=$?
  [ "${rc}" -ne 0 ]
  grep -q '^200000$' "${tmp}/halt_poll_ns"
}

@test "an empty request is the read-only path, the way an omitted input arrives" {
  tmp=$(mktemp -d)
  printf '200000\n' >"${tmp}/halt_poll_ns"
  COZY_HALT_POLL_FILE="${tmp}/halt_poll_ns" \
  COZY_HALT_POLL_RECORD="${tmp}/record.txt" \
  COZY_E2E_HALT_POLL_NS= \
    sh "${HALT_POLL}"
  grep -q 'requested: none' "${tmp}/record.txt"
  grep -q '^200000$' "${tmp}/halt_poll_ns"
}

@test "a record that cannot be written leaves the reading in the log and the step green" {
  tmp=$(mktemp -d)
  printf '200000\n' >"${tmp}/halt_poll_ns"
  # A record path under an existing FILE, so the directory cannot be created.
  printf 'x\n' >"${tmp}/blocker"
  COZY_HALT_POLL_FILE="${tmp}/halt_poll_ns" \
  COZY_HALT_POLL_RECORD="${tmp}/blocker/record.txt" \
    sh "${HALT_POLL}" >"${tmp}/out" 2>"${tmp}/err"
  grep -q 'value before: 200000' "${tmp}/out"
  grep -q 'could not be written' "${tmp}/err"
}

@test "the prepare-cluster suite runs the step before it boots the guests" {
  # The parameter is read per halt, so a change after the guests start would
  # apply to some of their halts and not others, and the recording would
  # describe a window it did not cover.
  step=$(grep -n 'e2e-halt-poll.sh' hack/e2e-prepare-cluster.bats | head -1 | cut -d: -f1)
  boot=$(grep -n '@test "Boot QEMU VMs"' hack/e2e-prepare-cluster.bats | head -1 | cut -d: -f1)
  # Both located before either is compared: with only one of them found, an
  # ordering test is satisfied by whichever line survived, which is green for a
  # renamed neighbour and for a deleted step alike.
  if [ -z "${step}" ] || [ -z "${boot}" ]; then
    echo "the halt-poll step or the guest launch it must precede is not in hack/e2e-prepare-cluster.bats" >&2
    return 1
  fi
  [ "${step}" -lt "${boot}" ]
}
