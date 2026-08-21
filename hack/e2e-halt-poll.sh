#!/bin/sh
# e2e-halt-poll.sh -- record, and on request change, the KVM halt-polling window
# of the kernel that hosts the sandbox VMs.
#
# WHICH LAYER THIS IS. The suite runs in a privileged container on the runner,
# and hack/e2e-prepare-cluster.bats starts the three Talos nodes inside that
# container with accel=kvm, so the runner kernel is their hypervisor and this
# parameter governs their vCPU threads. It does NOT govern the vCPU threads of a
# tenant worker: those are guests of the Talos nodes and answer to the KVM in
# each node's own kernel, which nothing here touches. Reading a figure from this
# file as a statement about tenant workers is the layer confusion that this
# paragraph exists to prevent.
#
# WHY IT MAY MATTER. Halt polling spins a vCPU thread in the host for up to this
# many nanoseconds before sleeping on a guest halt. The sandbox commits 24 guest
# vCPUs to a runner with fewer physical cores, so time spent spinning is time a
# runnable sibling did not get; whether that trade is worth taking here is a
# measurement rather than a known answer, which is why this is a knob and not a
# setting.
#
# WHAT IT PROMISES. A run that asked for a value and did not get it fails here,
# before the guests boot. A run that asks for nothing changes nothing and still
# records what the kernel had, because a measurement of the default is only
# usable next to the figure the default turned out to be -- the upstream default
# is not something this script assumes.
#
# Environment:
#   COZY_E2E_HALT_POLL_NS   value to write; unset or empty reads only
#   COZY_HALT_POLL_FILE     the parameter (default the real one, seam for tests)
#   COZY_HALT_POLL_RECORD   where the record is written; it also goes to stdout,
#                           so a run whose record could not be written still has
#                           the reading in its log
set -eu

halt_poll_file=${COZY_HALT_POLL_FILE:-/sys/module/kvm/parameters/halt_poll_ns}
halt_poll_record=${COZY_HALT_POLL_RECORD:-${COZY_REPORT_DIR:-/workspace/_out/cozyreport}/snapshots/sandbox-prepare/halt-poll.txt}
requested=${COZY_E2E_HALT_POLL_NS:-}

record="=== runner kernel KVM halt polling ===
[this is the halt-polling window of the kernel hosting the sandbox VMs, so it governs the vCPU threads of the three Talos nodes. It does not govern a tenant worker's vCPUs, which are guests of those nodes]
parameter file: ${halt_poll_file}"

# Reads the parameter into halt_poll_value, and reports which outcome it was:
# a value, an empty file, or nothing readable. Empty is kept
# apart from unreadable because the check after a write turns on it -- a write
# the module ignored leaves a file that reads back as nothing.
read_halt_poll() {
  halt_poll_value=''
  if [ ! -r "$halt_poll_file" ]; then
    halt_poll_state=unreadable
    return 0
  fi
  if ! halt_poll_raw=$(cat "$halt_poll_file" 2>/dev/null); then
    halt_poll_state=unreadable
    return 0
  fi
  halt_poll_value=$(printf '%s' "$halt_poll_raw" | tr -d '[:space:]')
  if [ -z "$halt_poll_value" ]; then
    halt_poll_state=empty
  else
    halt_poll_state=value
  fi
}

# Prints the record and stores it, in that order, so the arm where the store
# fails still leaves the reading somewhere. A record that cannot be stored is a
# warning and not a failure: it costs the artifact a file, while the figure it
# holds is in the log either way.
emit_record() {
  printf '%s\n' "$record"
  _hp_dir=$(dirname "$halt_poll_record")
  if mkdir -p "$_hp_dir" 2>/dev/null &&
    printf '%s\n' "$record" >"$halt_poll_record" 2>/dev/null; then
    return 0
  fi
  echo "WARNING: the halt-polling record could not be written to ${halt_poll_record}; the reading is in this log only" >&2
  return 0
}

describe_state() {
  case "$1" in
    value) printf '%s' "$2" ;;
    empty) printf '(empty)' ;;
    *) printf 'not readable' ;;
  esac
}

read_halt_poll
before_state=$halt_poll_state
before_value=$halt_poll_value
record="${record}
value before: $(describe_state "$before_state" "$before_value")"

# A reading that already equals the request is not a reading of the untouched
# parameter, and saying so matters because the bringup is retried: an attempt
# that fails is followed by another that recreates the container and runs this
# step again, and that one reads back what the previous attempt wrote. Left
# plain, that line would offer this run's own value as the baseline it is
# supposed to be compared against.
if [ -n "$requested" ] && [ "$before_state" = value ] &&
  [ "$before_value" = "$requested" ]; then
  record="${record} (already the requested value, so this is not a reading of the untouched parameter: either this step ran earlier on this machine, or the default coincides with the request)"
fi

if [ -z "$requested" ]; then
  record="${record}
requested: none"
  emit_record
  exit 0
fi

# Checked before anything is written, and by shape rather than by range: the
# parameter is an unsigned integer, and a value carrying anything else would be
# rejected or truncated by the kernel and then show up as a mismatch below,
# which reads like a kernel that ignored a sound request.
case "$requested" in
  *[!0-9]*)
    record="${record}
requested: ${requested} (refused: not a plain number)"
    emit_record
    echo "ERROR: COZY_E2E_HALT_POLL_NS is '${requested}', which is not a plain number of nanoseconds; nothing was written" >&2
    exit 1
    ;;
esac

record="${record}
requested: ${requested}"

if [ "$before_state" = unreadable ]; then
  emit_record
  echo "ERROR: ${halt_poll_file} is not readable, so the requested halt-polling window of ${requested} cannot be applied; this run would have measured the kernel default under another name" >&2
  exit 1
fi

# Written and not put back. That is a property of the machine rather than an
# omission: cncf/automation's OCI cloudrunner builds one ephemeral VM per job
# (`NewEphemeralMachine` in ci/cloudrunners/oci/main.go) and deletes it when the
# job ends, so no later job runs on this kernel. On a pool that reuses machines
# this would need a teardown.
if ! printf '%s\n' "$requested" >"$halt_poll_file" 2>/dev/null; then
  record="${record}
value after: (write failed)"
  emit_record
  echo "ERROR: writing ${requested} to ${halt_poll_file} failed" >&2
  exit 1
fi

read_halt_poll
record="${record}
value after: $(describe_state "$halt_poll_state" "$halt_poll_value")"
emit_record

# One comparison and not two. A request is a non-empty run of digits, so it
# cannot equal the empty string an unreadable or ignored parameter reads as,
# and a second test on the read state could never change the outcome.
if [ "$halt_poll_value" != "$requested" ]; then
  echo "ERROR: the halt-polling window did not stick: ${requested} was written to ${halt_poll_file} and it reads back as $(describe_state "$halt_poll_state" "$halt_poll_value")" >&2
  exit 1
fi
