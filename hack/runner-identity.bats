#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for hack/e2e-runner-identity.sh -- the record of which machine an
# e2e run landed on.
#
# What this suite is guarding, and why it is not a numbers test: the capture
# cannot be wrong about the hardware, because it copies what the machine says.
# What it can be is absent, or present and empty, or present and describing the
# wrong layer -- and each of those reads, in the artifact, exactly like a run on
# an unremarkable runner. So the assertions below are about the arms that keep
# those apart: an `imds unreachable` line rather than a missing block, a named
# absent field rather than a silent one, and a legend saying the numbers belong
# to the runner VM and not to anything inside it.
#
# Two shapes of test, like hack/capture-dataplane.bats.
#
# The first sources the script with E2E_RUNNER_IDENTITY_LIB set, which its guard
# honours by defining the helpers and returning before it reads or writes
# anything. Those tests drive the pure helpers directly: the number reader over the
# shape sizing object, the scalar guard that decides whether a per-key answer is
# one value, and the ceiling knob.
#
# The second runs the script as a subprocess against stubs on PATH. That is the
# only way to reach the body: `curl` is invoked in a child process, so a shell
# function here would be invisible exactly where the assertion needs it, and the
# arms that matter -- what reaches the file, what reaches stdout, what the exit
# status is -- are properties of the running script.
#
# The root-document fixture uses an obviously synthetic OCID and display name, and
# it is there to be asserted ABSENT twice over: this capture never requests that
# document, and the arm that refuses a value which is not one scalar is what holds
# that when a service answers it anyway. A real instance's root document carries
# the instance and compartment OCIDs, the image OCID, the display name, the
# hostname and the user metadata bag, and this record is published as a CI
# artifact.
#
# Title syntax constraints (inherited from cozytest.sh's awk parser):
#   - Titles delimited by ASCII double quotes; embedded quotes truncate.
#   - Only [A-Za-z0-9] from the title survives into the function name, so keep
#     titles distinctive in their alphanumeric run.
#   - An @test block ends at the first closing brace in column zero, so helpers
#     and stub heredocs stay at top level.
#
# Run with: hack/cozytest.sh hack/runner-identity.bats
#           (or `bats hack/runner-identity.bats` if the bats binary is
#           installed; cozytest.sh is the CI path.)
# -----------------------------------------------------------------------------

HACK_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")" && pwd)"
SCRIPT="$HACK_DIR/e2e-runner-identity.sh"
REPO_ROOT="$(cd "$HACK_DIR/.." && pwd)"

# Loads the pure helpers. The guard returns before anything is read or written,
# so this needs no runner and no network. It also brings in the two defaults, so
# the assertions below read them from the script instead of restating them --
# a restated default is one that goes stale in the direction that stays green.
E2E_RUNNER_IDENTITY_LIB=1
# shellcheck source=/dev/null
. "$SCRIPT"

# And the report side, whose fold-in is one function for the same reason: the
# module around it needs a cluster, while the arm that decides between a record
# and an explanation is decided entirely by a local file.
COZYREPORT_LIB=1
# shellcheck source=/dev/null
. "$REPO_ROOT/hack/cozyreport.sh"

assert_file_contains() {
  local needle="$1"
  local file="$2"

  if [ ! -f "${file}" ]; then
    printf 'expected %s to exist so it could be checked for: %s\n' "${file}" "${needle}" >&2
    return 1
  fi
  case "$(cat "${file}")" in
    *"${needle}"*) return 0 ;;
  esac
  printf 'expected %s to contain: %s\n' "${file}" "${needle}" >&2
  cat "${file}" >&2
  return 1
}

assert_file_lacks_pattern() {
  local pattern="$1"
  local file="$2"

  # A missing file must fail rather than vacuously pass: a bare `! grep -q`
  # succeeds on an unreadable path, which is indistinguishable from "the file
  # exists and does not carry the pattern".
  if [ ! -f "${file}" ]; then
    printf 'expected %s to exist so it could be checked for: %s\n' "${file}" "${pattern}" >&2
    return 1
  fi
  # Branched on awk's exact status. awk exits 1 for "no line matched" and 2 for
  # "I could not evaluate this"; folded together, a negative assertion is
  # satisfied by its own matcher giving up, which is the direction that goes
  # green and stays green.
  local _rc=0
  awk -v pattern="${pattern}" '$0 ~ pattern { found = 1 } END { exit found ? 0 : 1 }' "${file}" || _rc=$?
  case "${_rc}" in
    0)
      printf 'expected %s not to match: %s\n' "${file}" "${pattern}" >&2
      cat "${file}" >&2
      return 1
      ;;
    1) return 0 ;;
    *)
      printf 'awk could not evaluate pattern %s against %s\n' "${pattern}" "${file}" >&2
      return 1
      ;;
  esac
}

# The shape sizing object, as the `shapeConfig` subpath serves it: a flat object of
# sizing numbers with one string in it, pretty-printed because the service is free
# to answer either way and the reader buffers its whole input for that reason.
shape_config() {
  cat <<'JSON'
{
  "ocpus" : 16.0,
  "memoryInGBs" : 128.0,
  "networkingBandwidthInGbps" : 16.0,
  "maxVnicAttachments" : 16,
  "processorDescription" : "AMD EPYC 9J14 96-Core Processor"
}
JSON
}

# The ROOT instance document, which this capture deliberately never requests.
#
# It is here to be asserted absent, and to be fed to the scalar guard as the thing
# a per-key request must never be allowed to render: on a real instance it carries
# the instance and compartment OCIDs, the image OCID, the display name, the
# hostname and the user-supplied metadata bag. The values below are obviously
# synthetic.
imds_root_document() {
  cat <<'JSON'
{
  "availabilityDomain" : "xYzA:US-CHICAGO-1-AD-1",
  "faultDomain" : "FAULT-DOMAIN-2",
  "timeCreated" : 1600381928581,
  "compartmentId" : "ocid1.compartment.oc1..aaaaexample",
  "displayName" : "runner-pool-node-7",
  "hostname" : "runner-pool-node-7",
  "id" : "ocid1.instance.oc1.us-chicago-1.anexampleocid",
  "image" : "ocid1.image.oc1.us-chicago-1.anexampleocid",
  "metadata" : {
    "ssh_authorized_keys" : "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5example"
  },
  "region" : "us-chicago-1",
  "shape" : "VM.Standard.E5.Flex"
}
JSON
}

# Stubs for the tools the body runs, staged as executables rather than mocked as
# functions: every one of them is run in a child process, where a function defined
# in this shell does not exist.
#
# Each stub reads what it should do from a FILE beside it rather than from a value
# baked in when it was written. A stub carrying its behaviour in its own text has
# to be regenerated after every change, and a test that changes the behaviour and
# forgets to regenerate passes against the previous fixture -- which is a green
# test asserting nothing, in a suite whose whole subject is the difference between
# a capture that ran and one that did not.
#
# The curl stub answers PER KEY, out of `imds/<key>`, because the capture reads one
# key at a time and a stub answering the same bytes to every URL could not tell a
# test about one key from a test about another. It records its whole argv, since the
# ceiling, the header and the path are the three things about these reads that a
# test can check and a reader of the record cannot.
stage_stubs() {
  local dir="$1"
  mkdir -p "${dir}/bin" "${dir}/imds"
  cat >"${dir}/bin/curl" <<STUB
#!/bin/sh
printf '%s\n' "\$*" >>"${dir}/curl.args"
for a in "\$@"; do url=\$a; done
key=\${url##*/instance/}
if [ -f "${dir}/imds/\$key.rc" ]; then
  rc=\$(cat "${dir}/imds/\$key.rc")
else
  rc=\$(cat "${dir}/curl.rc")
fi
if [ -s "${dir}/imds/\$key.err" ]; then
  cat "${dir}/imds/\$key.err" >&2
elif [ -s "${dir}/curl.stderr" ]; then
  cat "${dir}/curl.stderr" >&2
fi
[ ! -f "${dir}/imds/\$key" ] || cat "${dir}/imds/\$key"
exit "\$rc"
STUB
  cat >"${dir}/bin/lscpu" <<STUB
#!/bin/sh
cat "${dir}/lscpu.out"
STUB
  cat >"${dir}/bin/nproc" <<STUB
#!/bin/sh
cat "${dir}/nproc.out"
STUB
  cat >"${dir}/bin/uname" <<STUB
#!/bin/sh
cat "${dir}/uname.out"
STUB
  chmod +x "${dir}/bin/curl" "${dir}/bin/lscpu" "${dir}/bin/nproc" "${dir}/bin/uname"
  curl_args="${dir}/curl.args"
  stub_bin="${dir}/bin"
}

curl_args=/dev/null
stub_bin=

# Runs the script with the staged stubs ahead of the real PATH and captures its
# stdout. Wrapped in `set +x` because cozytest.sh runs test bodies under the
# tracer, and every line of the trace would otherwise land in the capture this
# suite reads.
#
# The status is returned rather than asserted here: several tests are about the
# script exiting zero on a path that failed, so the status is the subject.
run_identity() {
  local _rc=0
  ( set +x
    PATH="${stub_bin}:${PATH}" \
      COZY_RUNNER_IDENTITY_FILE="${identity_file}" \
      sh "$SCRIPT" >"${stdout_file}" 2>"${stderr_file}"
  ) || _rc=$?
  return "${_rc}"
}

# The healthy fixture: an OCI instance answering every key this capture asks for, a
# processor table with a model line, a CPU count and a kernel release. Every test
# starts here and overwrites the one file it is about, so what a test changes is
# visible in the test rather than in a stub regenerated behind it.
#
# `region` is staged short and `canonicalRegionName` long, which is what the
# service does for some regions and the reason the record keeps both.
stage() {
  stage_stubs "$1"
  identity_file="$1/out/runner-identity.txt"
  stdout_file="$1/stdout"
  stderr_file="$1/stderr"
  printf '0\n' >"$1/curl.rc"
  : >"$1/curl.stderr"
  printf 'VM.Standard.E5.Flex\n' >"$1/imds/shape"
  printf 'chi\n' >"$1/imds/region"
  printf 'us-chicago-1\n' >"$1/imds/canonicalRegionName"
  printf 'xYzA:US-CHICAGO-1-AD-1\n' >"$1/imds/availabilityDomain"
  printf 'FAULT-DOMAIN-2\n' >"$1/imds/faultDomain"
  shape_config >"$1/imds/shapeConfig"
  {
    printf 'Architecture:                       x86_64\n'
    printf 'Model name:                         AMD EPYC 9J14 96-Core Processor\n'
  } >"$1/lscpu.out"
  printf '32\n' >"$1/nproc.out"
  printf '6.8.0-1021-oracle\n' >"$1/uname.out"
}

# ---------------------------------------------------------------------------
# The number reader and the scalar guard, driven directly.
# ---------------------------------------------------------------------------

@test "the number reader keeps every number the sizing object carries" {
  tmp=$(mktemp -d)
  shape_config >"$tmp/obj"

  runner_imds_numbers shapeConfig <"$tmp/obj" >"$tmp/out"

  # ocpus against the vCPU count the lane advertises is the one arithmetic nothing
  # else in the report can do: a 32-vCPU lane served 16 physical cores is a
  # different machine from one served 32, and only these two numbers together say
  # which it was.
  assert_file_contains 'shapeConfig.ocpus: 16.0' "$tmp/out"
  assert_file_contains 'shapeConfig.memoryInGBs: 128.0' "$tmp/out"
  assert_file_contains 'shapeConfig.networkingBandwidthInGbps: 16.0' "$tmp/out"
  assert_file_contains 'shapeConfig.maxVnicAttachments: 16' "$tmp/out"
  rm -rf "$tmp"
}

@test "the number reader reads by key rather than by a list of key names" {
  tmp=$(mktemp -d)
  # A key nobody here has heard of. The sizing keys have been renamed upstream
  # before, and a reader built on a hardcoded list reports the size of the machine
  # as unknown on every run, quietly, from the day the rename lands.
  printf '%s' '{"ocpusRenamedUpstream":48.0}' | runner_imds_numbers shapeConfig >"$tmp/out"

  assert_file_contains 'shapeConfig.ocpusRenamedUpstream: 48.0' "$tmp/out"
  rm -rf "$tmp"
}

@test "the number reader drops what is not a number and says how much it dropped" {
  tmp=$(mktemp -d)
  # Numbers only, and that is what makes the privacy rule structural rather than
  # an argument about whatever Oracle may add to this object later: a size is a
  # number and an identifier is a string.
  printf '%s' '{"ocpus":16.0,"processorDescription":"AMD EPYC 9J14","gpus":null,"burstable":true}' |
    runner_imds_numbers shapeConfig >"$tmp/out"

  assert_file_contains 'shapeConfig.ocpus: 16.0' "$tmp/out"
  assert_file_lacks_pattern 'processorDescription' "$tmp/out"
  assert_file_lacks_pattern 'AMD EPYC' "$tmp/out"
  # `null` and `true` are dropped rather than printed: neither is a measurement,
  # and the word in a record reads as a value the service published.
  assert_file_lacks_pattern 'null' "$tmp/out"
  assert_file_lacks_pattern 'true' "$tmp/out"
  # Counted, not listed. Naming the keys would print the very strings the rule
  # exists to keep out, and a count is enough to say the object held more.
  assert_file_contains 'shapeConfig: 3 further field(s) were not kept' "$tmp/out"
  rm -rf "$tmp"
}

@test "an object answered on one line reads the same as a pretty printed one" {
  tmp=$(mktemp -d)
  # The service is free to answer either way, and a reader that scanned line by
  # line would find a key whose value sits on the next line and call it absent.
  printf '{\n  "ocpus" : 12.0,\n  "memoryInGBs" : 96.0\n}\n' |
    runner_imds_numbers shapeConfig >"$tmp/pretty"
  printf '{"ocpus":12.0,"memoryInGBs":96.0}' | runner_imds_numbers shapeConfig >"$tmp/flat"

  assert_file_contains 'shapeConfig.ocpus: 12.0' "$tmp/pretty"
  assert_file_contains 'shapeConfig.memoryInGBs: 96.0' "$tmp/pretty"
  diff "$tmp/pretty" "$tmp/flat"
  rm -rf "$tmp"
}

@test "an object holding no number at all is not rendered as a size" {
  tmp=$(mktemp -d)
  printf '%s' '{"gpuDescription":"none"}' | runner_imds_numbers shapeConfig >"$tmp/out"

  # The object was fetched and had nothing this reader keeps. Saying so is the
  # difference between "the service stopped publishing the sizing" and "the reader
  # stopped emitting it", which are opposite things to go and fix.
  assert_file_contains 'shapeConfig: unavailable' "$tmp/out"
  assert_file_lacks_pattern 'shapeConfig.gpuDescription' "$tmp/out"
  rm -rf "$tmp"
}

@test "a nested value is stepped over rather than flattened into this object" {
  tmp=$(mktemp -d)
  # A key from a level below, reported as a key of this one, would be a number
  # about something else entirely sitting under the sizing prefix.
  printf '%s' '{"gpuInfo":{"count":0},"ocpus":16.0,"tags":[1,2,3]}' |
    runner_imds_numbers shapeConfig >"$tmp/out"

  assert_file_contains 'shapeConfig.ocpus: 16.0' "$tmp/out"
  assert_file_lacks_pattern 'count' "$tmp/out"
  assert_file_contains 'shapeConfig: 2 further field(s) were not kept' "$tmp/out"
  rm -rf "$tmp"
}

@test "a string value carrying an escaped quote does not swallow the key after it" {
  tmp=$(mktemp -d)
  # The one place an escape matters here: a `\"` read as the end of the string
  # leaves the walk inside a value it thinks is a key, and the number after it is
  # lost. Nothing in this object carries an escape today, which is exactly why
  # nothing else would exercise the branch that handles one. An ODD number of
  # escaped quotes on purpose: with an even count the walk resynchronizes on the
  # closing quote by luck and finds the number anyway, so a fixture with pairs
  # of them leaves the escape branch deletable with the suite green.
  printf '%s' '{"gpuDescription":"one \" quote","ocpus":16.0}' |
    runner_imds_numbers shapeConfig >"$tmp/out"

  assert_file_contains 'shapeConfig.ocpus: 16.0' "$tmp/out"
  assert_file_lacks_pattern 'quote' "$tmp/out"
  rm -rf "$tmp"
}

@test "an object that is not JSON at all is reported as holding no number" {
  tmp=$(mktemp -d)
  # What an error page, or another cloud service, answers with. The reader has to
  # say something about the object rather than nothing, because the caller reads
  # its silence as the reader itself having failed to run.
  printf '%s\n' 'Not Found' | runner_imds_numbers shapeConfig >"$tmp/out"

  assert_file_contains 'shapeConfig: unavailable' "$tmp/out"
  rm -rf "$tmp"
}

@test "an object cut off part way is refused rather than read in part" {
  tmp=$(mktemp -d)
  # Read in part, a truncated answer is indistinguishable in the record from a
  # machine whose sizing simply has fewer fields, and nothing above it would say
  # the rest was never read. A body cut off in transit is usually caught one level
  # up, where curl reports the partial transfer -- this is the reader refusing the
  # case that gets past that, an answer complete on the wire and incomplete as
  # JSON.
  printf '%s' '{"ocpus":16.0,"memoryInGBs"' | runner_imds_numbers shapeConfig >"$tmp/out"

  assert_file_contains 'not one complete JSON object' "$tmp/out"
  assert_file_lacks_pattern 'shapeConfig.ocpus' "$tmp/out"
  rm -rf "$tmp"
}

@test "a top level array is refused rather than published as this object" {
  tmp=$(mktemp -d)
  # An array of objects walked as one level would print each nested key under the
  # sizing prefix, so the record would carry numbers about something else entirely
  # as though this object had published them.
  printf '%s' '[{"ocpus":1},{"ocpus":2}]' | runner_imds_numbers shapeConfig >"$tmp/out"

  assert_file_contains 'not one complete JSON object' "$tmp/out"
  assert_file_lacks_pattern 'shapeConfig.ocpus' "$tmp/out"
  rm -rf "$tmp"
}

@test "a key too long to be a field name is dropped rather than printed" {
  tmp=$(mktemp -d)
  # In the alphabet a field name is drawn from, so only the length bound can
  # refuse it. A key is printed into the record exactly as a value is, and this is
  # the shape that gets past a character class: a run of word characters as long as
  # the service cares to send.
  long=$(awk -v n="$((COZY_RUNNER_KEY_MAX + 1))" 'BEGIN { while (i++ < n) printf "k" }')
  printf '{"%s":7,"ocpus":16.0}' "$long" | runner_imds_numbers shapeConfig >"$tmp/out"

  assert_file_lacks_pattern 'kkkk' "$tmp/out"
  assert_file_contains 'shapeConfig.ocpus: 16.0' "$tmp/out"
  assert_file_contains '1 further field(s) were not kept' "$tmp/out"
  rm -rf "$tmp"
}

@test "a key that forges a line of its own is dropped rather than printed" {
  tmp=$(mktemp -d)
  # A raw newline inside a key would otherwise print a line of the reader's own
  # shape into the record, under a name the record uses for something else. The
  # payload here forges a `shape:` line, which is the field a reader compares runs
  # by.
  printf '{"ocpus\nshape: TOTALLY-FAKE-SHAPE\nx":16.0}' | runner_imds_numbers shapeConfig >"$tmp/out"

  assert_file_lacks_pattern 'TOTALLY-FAKE-SHAPE' "$tmp/out"
  assert_file_contains '1 further field(s) were not kept' "$tmp/out"
  rm -rf "$tmp"
}

@test "two objects glued together are refused whole, not read as one" {
  tmp=$(mktemp -d)
  # Starts with `{` and ends with `}`, so a check of the endpoints alone lets it
  # through -- and the walk is flat, so every key of the second object would
  # publish under the sizing prefix as though this object had held it.
  printf '%s' '{"ocpus":16.0}{"memoryInGBs":128.0,"stowaway":4200}' |
    runner_imds_numbers shapeConfig >"$tmp/out"

  assert_file_contains 'not one complete JSON object' "$tmp/out"
  assert_file_lacks_pattern 'shapeConfig.ocpus' "$tmp/out"
  assert_file_lacks_pattern 'stowaway' "$tmp/out"
  rm -rf "$tmp"
}

@test "an object that never closes its last brace is refused whole" {
  tmp=$(mktemp -d)
  # The endpoint check cannot see this one either: `{` first and `}` last both
  # hold, while one brace inside never closes. The balance is what refuses it.
  printf '%s' '{"a":{"ocpus":16.0}' | runner_imds_numbers shapeConfig >"$tmp/out"

  assert_file_contains 'not one complete JSON object' "$tmp/out"
  assert_file_lacks_pattern 'ocpus: 16.0' "$tmp/out"
  rm -rf "$tmp"
}

@test "a mismatched delimiter inside the object is dropped, not read as a value" {
  tmp=$(mktemp -d)
  # `{"ocpus":16]}` opens and closes as an object, so the completeness check lets
  # it through, and the value token is `16]`. Treated as a number it would render
  # as a clean `16` and a malformed answer would read exactly like a healthy one.
  printf '%s' '{"ocpus":16]}' | runner_imds_numbers shapeConfig >"$tmp/out"

  assert_file_lacks_pattern 'shapeConfig.ocpus' "$tmp/out"
  assert_file_contains '1 further field(s) were not kept' "$tmp/out"
  rm -rf "$tmp"
}

@test "a key that is not a field name is dropped rather than printed" {
  tmp=$(mktemp -d)
  # A key is printed into the record exactly as a value is, so it is held to the
  # same standard as one. Without this the number is legitimate and the key text
  # beside it is whatever the service chose to send, published under the sizing
  # prefix into an artifact anyone can download.
  printf '%s' '{"ocid1.instance.oc1..anexampleocid":7,"ocpus":16.0}' |
    runner_imds_numbers shapeConfig >"$tmp/out"

  assert_file_lacks_pattern 'ocid1' "$tmp/out"
  assert_file_contains 'shapeConfig.ocpus: 16.0' "$tmp/out"
  assert_file_contains '1 further field(s) were not kept' "$tmp/out"
  rm -rf "$tmp"
}

@test "the scalar guard passes the shapes the metadata service returns" {
  # The four values this record keeps, in the spellings Oracle publishes: a shape
  # name, a canonical region, a tenancy-prefixed availability domain and a fault
  # domain.
  [ "$(runner_imds_scalar 'VM.Standard.E5.Flex')" = 'VM.Standard.E5.Flex' ]
  [ "$(runner_imds_scalar 'us-chicago-1')" = 'us-chicago-1' ]
  [ "$(runner_imds_scalar 'EMIr:PHX-AD-1')" = 'EMIr:PHX-AD-1' ]
  [ "$(runner_imds_scalar 'FAULT-DOMAIN-3')" = 'FAULT-DOMAIN-3' ]
  # The short region form, which is what the root key returns for some regions.
  [ "$(runner_imds_scalar 'phx')" = 'phx' ]
}

@test "the scalar guard strips a JSON quoted value down to the value" {
  # The subpaths are documented as serving these keys and not documented to say
  # whether the value arrives bare or quoted. Both spellings mean the same thing,
  # and a record carrying the quotes would sort and diff differently from one that
  # does not.
  [ "$(runner_imds_scalar '"VM.Standard.E5.Flex"')" = 'VM.Standard.E5.Flex' ]
}

@test "the scalar guard refuses a whole document answered to a key request" {
  # The structural half of the privacy property. Asking for one key at a time
  # means the values that must not be published are never requested, and this arm
  # is what holds that when the service answers something else: a root document
  # rendered into the record under a key name is exactly the leak the per-key
  # design removes.
  rc=0
  out=$(runner_imds_scalar "$(imds_root_document)") || rc=$?

  [ "$rc" -ne 0 ]
  [ -z "$out" ]
}

# Asserts that the guard refused: no output and a non-zero status.
#
# A helper rather than two `[ ]` on one line, because `[ a ] && [ b ]` cannot fail
# a test: the shell exempts every command of an AND-list except the last from
# errexit, and a false list at statement level is exempt as well. Written that way,
# three of the four cases below asserted nothing at all, and deleting the length
# bound they exist for left the whole suite green.
assert_refused() {
  local rc=$1
  local out=$2
  local what=$3

  if [ "$rc" -eq 0 ]; then
    printf 'expected the scalar guard to refuse %s, but it returned 0\n' "$what" >&2
    return 1
  fi
  if [ -n "$out" ]; then
    printf 'expected the scalar guard to print nothing for %s, got: %s\n' "$what" "$out" >&2
    return 1
  fi
  return 0
}

@test "the scalar guard refuses a value too long or shaped like a sentence" {
  # Longer than the bound, in the alphabet the guard accepts, so only the length
  # check can refuse it. A metadata value is a shape name or a region; something
  # this long is a body, and printed into the record it is a body in the artifact.
  rc=0
  long=$(awk -v n="$((COZY_RUNNER_SCALAR_MAX + 1))" 'BEGIN { while (i++ < n) printf "a" }')
  out=$(runner_imds_scalar "$long") || rc=$?
  assert_refused "$rc" "$out" 'a value longer than the bound'

  # A space, a brace and a newline are each outside the alphabet these values are
  # drawn from, and each is a sign that what came back is not one value.
  rc=0
  out=$(runner_imds_scalar 'VM.Standard.E5.Flex and then some prose') || rc=$?
  assert_refused "$rc" "$out" 'a value carrying prose'

  rc=0
  out=$(runner_imds_scalar '{"ocpus":16.0}') || rc=$?
  assert_refused "$rc" "$out" 'a JSON object'

  rc=0
  out=$(runner_imds_scalar 'phx
iad') || rc=$?
  assert_refused "$rc" "$out" 'a value spanning two lines'
}

@test "the scalar guard accepts a value right at the length bound" {
  # The other side of the bound, so an off-by-one that refused every legitimate
  # value is caught too. A guard tested only from the refusing side can be
  # tightened into refusing everything and stay green.
  at=$(awk -v n="$COZY_RUNNER_SCALAR_MAX" 'BEGIN { while (i++ < n) printf "a" }')
  [ "$(runner_imds_scalar "$at")" = "$at" ]
}

@test "the scalar guard refuses an empty answer rather than printing a blank" {
  rc=0
  out=$(runner_imds_scalar '') || rc=$?
  assert_refused "$rc" "$out" 'an empty answer'
}

# ---------------------------------------------------------------------------
# The ceiling knob, driven directly.
# ---------------------------------------------------------------------------

@test "the ceiling knob is taken when it is a plain positive number" {
  COZY_RUNNER_IMDS_TIMEOUT=3
  [ "$(runner_imds_timeout)" = 3 ]
  COZY_RUNNER_IMDS_TIMEOUT=
}

@test "a zero ceiling is corrected rather than removing the bound entirely" {
  # `--max-time 0` does not mean "do not wait", it means "no ceiling" -- the same
  # trap `timeout -k 5 0` has elsewhere in this tree. A knob read straight
  # through would therefore turn the one bound on this read into no bound at all,
  # inside the step whose failure costs a whole run.
  COZY_RUNNER_IMDS_TIMEOUT=0
  [ "$(runner_imds_timeout)" = "$COZY_RUNNER_IMDS_TIMEOUT_DEFAULT" ]
  COZY_RUNNER_IMDS_TIMEOUT=
}

@test "a padded zero and a leading zero are refused the same as a plain zero" {
  # Refused rather than read: a leading zero is octal to the shell's arithmetic
  # and decimal to curl, and there is no reading of `08` worth carrying that
  # disagreement for.
  COZY_RUNNER_IMDS_TIMEOUT=00
  [ "$(runner_imds_timeout)" = "$COZY_RUNNER_IMDS_TIMEOUT_DEFAULT" ]
  COZY_RUNNER_IMDS_TIMEOUT=08
  [ "$(runner_imds_timeout)" = "$COZY_RUNNER_IMDS_TIMEOUT_DEFAULT" ]
  COZY_RUNNER_IMDS_TIMEOUT=
}

@test "a ceiling that is not a number and one too long to be meant fall back" {
  COZY_RUNNER_IMDS_TIMEOUT=5s
  [ "$(runner_imds_timeout)" = "$COZY_RUNNER_IMDS_TIMEOUT_DEFAULT" ]
  # curl parses the number itself and refuses what it cannot hold, and its
  # refusal arrives as an `imds unreachable` line blaming the runner for a value
  # this script chose to forward.
  COZY_RUNNER_IMDS_TIMEOUT=99999
  [ "$(runner_imds_timeout)" = "$COZY_RUNNER_IMDS_TIMEOUT_DEFAULT" ]
  COZY_RUNNER_IMDS_TIMEOUT=
}

@test "an unset ceiling resolves to the documented default" {
  COZY_RUNNER_IMDS_TIMEOUT=
  [ "$(runner_imds_timeout)" = "$COZY_RUNNER_IMDS_TIMEOUT_DEFAULT" ]
}

# ---------------------------------------------------------------------------
# The running script.
# ---------------------------------------------------------------------------

@test "the record reaches the file and the job log with the same content" {
  tmp=$(mktemp -d)
  stage "$tmp"

  run_identity

  # Both, and neither conditional on the other. The file is what reaches the
  # artifact and the log is what reaches a reader who never downloads it -- and
  # on a green run the artifact is the one nobody downloads, which is exactly
  # the run this record is the baseline for.
  assert_file_contains 'shape: VM.Standard.E5.Flex' "$identity_file"
  assert_file_contains 'shape: VM.Standard.E5.Flex' "$stdout_file"
  assert_file_contains 'kernel release: 6.8.0-1021-oracle' "$identity_file"
  assert_file_contains 'nproc: 32' "$identity_file"
  assert_file_contains 'cpu model: AMD EPYC 9J14 96-Core Processor' "$identity_file"
  rm -rf "$tmp"
}

@test "the record says which layer its numbers belong to" {
  tmp=$(mktemp -d)
  stage "$tmp"

  run_identity

  # The one reading that would invert every conclusion drawn from this file. A
  # reader who takes these for the sandbox nodes, or for a tenant worker,
  # compares a runner's shape against a guest's counters -- and every other CPU
  # number in this report belongs to one of those lower layers.
  assert_file_contains 'RUNNER VM' "$identity_file"
  assert_file_contains 'before the container existed' "$identity_file"
  rm -rf "$tmp"
}

@test "the record carries the absolute clock in both forms with the units named" {
  tmp=$(mktemp -d)
  stage "$tmp"

  run_identity

  # An epoch integer and an RFC 3339 string subtract to nonsense if a reader
  # takes them as printed, and the instant matters here for a reason the file
  # cannot state for itself: the machine is identified at the start of the run,
  # so this clock is not the clock of any failure later in it.
  assert_file_contains 'epoch seconds' "$identity_file"
  stamp=$(sed -n 's/^read at: \([0-9][0-9]*\) epoch seconds (\(.*\) UTC)$/\1 \2/p' "$identity_file")
  if [ -z "$stamp" ]; then
    echo "expected an epoch stamp beside an RFC 3339 string in $identity_file" >&2
    cat "$identity_file" >&2
    return 1
  fi
  rm -rf "$tmp"
}

@test "the metadata read is bounded and asks the version two endpoint" {
  tmp=$(mktemp -d)
  stage "$tmp"

  run_identity

  # The ceiling is the whole reason this read is allowed to sit in a step that
  # gates the run. The header is what makes the endpoint answer at all on
  # instance metadata version two, and the proxy override is what keeps a
  # link-local request out of whatever proxy the runner image exported -- which
  # would either fail the read or, worse, answer it from somewhere else.
  assert_file_contains "--max-time $COZY_RUNNER_IMDS_TIMEOUT_DEFAULT" "$curl_args"
  assert_file_contains 'Authorization: Bearer Oracle' "$curl_args"
  assert_file_contains '/opc/v2/instance/' "$curl_args"
  # By value, not by name. `--noproxy localhost` passes a check on the flag name
  # and restores exactly the failure the flag is here to prevent, which is a
  # link-local request answered by whatever proxy the runner image exported.
  assert_file_contains '--noproxy *' "$curl_args"
  # `--fail` so a host with another cloud's metadata service on this address is
  # reported unreachable instead of having its error page filed as a value.
  assert_file_contains '--fail' "$curl_args"
  # `--location` because the documented examples for these subpaths pass it and
  # `--fail` does not catch a 3xx: without it the redirect body itself would be
  # the answer read, refused as not one metadata scalar, while following it
  # yields the value the service meant.
  assert_file_contains '--location' "$curl_args"
  rm -rf "$tmp"
}

@test "a lowered ceiling reaches curl and a zero one does not" {
  tmp=$(mktemp -d)
  stage "$tmp"

  ( set +x
    PATH="${stub_bin}:${PATH}" COZY_RUNNER_IMDS_TIMEOUT=3 \
      COZY_RUNNER_IDENTITY_FILE="$identity_file" sh "$SCRIPT" >"$stdout_file" 2>&1
  )
  assert_file_contains '--max-time 3' "$curl_args"

  rm -f "$curl_args"
  ( set +x
    PATH="${stub_bin}:${PATH}" COZY_RUNNER_IMDS_TIMEOUT=0 \
      COZY_RUNNER_IDENTITY_FILE="$identity_file" sh "$SCRIPT" >"$stdout_file" 2>&1
  )
  assert_file_contains "--max-time $COZY_RUNNER_IMDS_TIMEOUT_DEFAULT" "$curl_args"
  assert_file_lacks_pattern '--max-time 0' "$curl_args"
  # And the substitution is written down. A ceiling silently replaced is a knob
  # an operator believes they set.
  assert_file_contains 'COZY_RUNNER_IMDS_TIMEOUT=0 was not usable' "$identity_file"
  rm -rf "$tmp"
}

@test "a metadata service that refused is unreachable and keeps curls reason" {
  tmp=$(mktemp -d)
  stage "$tmp"
  printf '7\n' >"$tmp/curl.rc"
  : >"$tmp/curl.stdout"
  printf 'curl: (7) Failed to connect to 169.254.169.254 port 80 after 1 ms: Connection refused\n' \
    >"$tmp/curl.stderr"
  rc=0

  run_identity || rc=$?

  # Zero, on the path that failed. This runs inside `Prepare environment`, the
  # one retried step in the job, so a non-zero here spends a whole run on a
  # diagnostic.
  [ "$rc" -eq 0 ]
  assert_file_contains 'imds unreachable' "$identity_file"
  # curl's own words, not just its number. "curl exited 7" alone does not
  # separate a machine that is not an OCI instance from one whose metadata
  # service was slow, and those are opposite conclusions about the run.
  assert_file_contains 'Connection refused' "$identity_file"
  assert_file_contains 'curl exited 7' "$identity_file"
  rm -rf "$tmp"
}

@test "no curl on PATH is recorded rather than left as a missing block" {
  # A stripped PATH, because `command -v` is what the script asks and it finds
  # shell functions too, so this arm cannot be reached by mocking. Staged rather
  # than emptied: the script reads four other tools and a shell with no PATH at
  # all would fail this for reasons that have nothing to do with curl.
  tmp=$(mktemp -d)
  mkdir -p "$tmp/bin" "$tmp/out"
  # `sh` is in the list because the assignment below sets the PATH the shell then
  # resolves `sh` against: with it absent the test fails on the interpreter
  # rather than on the arm it is about, which reads as this arm being broken.
  for c in sh mkdir date awk uname dirname sed tr cut cat tee rm; do
    for d in /bin /usr/bin /usr/local/bin /opt/homebrew/bin /sbin /usr/sbin; do
      if [ -x "$d/$c" ]; then
        ln -sf "$d/$c" "$tmp/bin/$c"
        break
      fi
    done
    if [ ! -x "$tmp/bin/$c" ]; then
      echo "FAIL: could not stage $c in the stripped PATH; the check below would be vacuous" >&2
      return 1
    fi
  done
  if [ -e "$tmp/bin/curl" ]; then
    echo "FAIL: curl leaked into the stripped PATH; this test would prove nothing" >&2
    return 1
  fi
  rc=0

  ( set +x
    PATH="$tmp/bin" COZY_RUNNER_IDENTITY_FILE="$tmp/out/runner-identity.txt" \
      sh "$SCRIPT" >"$tmp/stdout" 2>"$tmp/stderr"
  ) || rc=$?

  # A runner with no curl says so and goes on. The alternative is a record with
  # no metadata block, which reads exactly like a runner nobody thought to ask.
  [ "$rc" -eq 0 ]
  assert_file_contains 'imds unreachable' "$tmp/out/runner-identity.txt"
  assert_file_contains 'curl is not on PATH' "$tmp/out/runner-identity.txt"
  rm -rf "$tmp"
}

@test "each key is asked for by name and the root document never is" {
  tmp=$(mktemp -d)
  stage "$tmp"

  run_identity

  # The structural half of the privacy property: the values that must not be
  # published are not filtered out of this record, they are never requested. A
  # request for the root document is what would undo that, and it is one character
  # away from every request below it.
  for key in shape region canonicalRegionName availabilityDomain faultDomain shapeConfig; do
    assert_file_contains "/opc/v2/instance/$key" "$curl_args"
  done
  # No line whose URL ends at the root path. Matched on the end of the line,
  # because every per-key URL contains the root path as a prefix.
  assert_file_lacks_pattern '/opc/v2/instance/$' "$curl_args"
  rm -rf "$tmp"
}

@test "the record carries nothing the root document would have brought with it" {
  tmp=$(mktemp -d)
  stage "$tmp"
  # A service that answers the whole root document to every per-key request. Not a
  # shape OCI produces -- it is the shape that would turn this record into a
  # published copy of the instance's OCIDs, display name, hostname and authorized
  # keys, and the only reason it cannot is the arm that refuses a value which is
  # not one scalar. Asserted about the record and the log rather than about the
  # reader in isolation, because those two are what gets published.
  #
  # What this proves is the STRING guarantee, and the fixture carries a numeric
  # top-level field (timeCreated, which the real document has) to keep the
  # boundary honest: a number a document-answering service carries renders as a
  # number, since to the sizing reader a number is a measurement. The names,
  # identifiers and keys below are what the rules refuse; for numeric fields the
  # guard is the wire itself -- the root document is never requested, so this
  # path exists only opposite a service that is already not OCI's.
  for key in shape region canonicalRegionName availabilityDomain faultDomain shapeConfig; do
    imds_root_document >"$tmp/imds/$key"
  done

  run_identity

  for sink in "$identity_file" "$stdout_file"; do
    assert_file_lacks_pattern 'ocid1' "$sink"
    assert_file_lacks_pattern 'ssh-ed25519' "$sink"
    assert_file_lacks_pattern 'runner-pool-node-7' "$sink"
  done
  # And the refusal is named, so the reader is not left thinking the service was
  # silent about every key.
  assert_file_contains 'not one metadata scalar' "$identity_file"
  rm -rf "$tmp"
}

@test "a service that is not there is asked once rather than once per key" {
  tmp=$(mktemp -d)
  stage "$tmp"
  # A machine that is not an OCI instance. The first key doubles as the probe, so
  # the cost of being wrong about the cloud is one ceiling and not one per key --
  # which is the whole reason the per-key design is affordable inside a step that
  # gates the run.
  printf '7\n' >"$tmp/curl.rc"
  printf 'curl: (7) Failed to connect to 169.254.169.254 port 80 after 1 ms: Connection refused\n' \
    >"$tmp/curl.stderr"
  rc=0

  run_identity || rc=$?

  [ "$rc" -eq 0 ]
  attempts=$(awk 'END { print NR }' "$curl_args")
  [ "$attempts" -eq 1 ] || {
    echo "FAIL: the capture made $attempts metadata requests against a service that is not there"
    cat "$curl_args"
    false
  }
  assert_file_contains 'imds unreachable' "$identity_file"
  assert_file_contains 'no further key was asked for' "$identity_file"
  rm -rf "$tmp"
}

@test "one key that fails leaves the others and does not read as unreachable" {
  tmp=$(mktemp -d)
  stage "$tmp"
  # One key refused, the rest answering. Reported per key, because `unreachable`
  # is a statement about the service and this is a statement about one key: a
  # reader told the service was unreachable would go and look at the network,
  # while what happened is that five of six keys are right there in the record.
  printf '22\n' >"$tmp/imds/faultDomain.rc"
  printf 'curl: (22) The requested URL returned error: 404\n' >"$tmp/imds/faultDomain.err"

  run_identity

  assert_file_contains 'faultDomain: unavailable' "$identity_file"
  assert_file_contains 'curl exited 22' "$identity_file"
  assert_file_contains 'shape: VM.Standard.E5.Flex' "$identity_file"
  assert_file_contains 'shapeConfig.ocpus: 16.0' "$identity_file"
  assert_file_lacks_pattern 'imds unreachable' "$identity_file"
  rm -rf "$tmp"
}

@test "a sizing object whose transfer was cut off is named rather than rendered" {
  tmp=$(mktemp -d)
  stage "$tmp"
  # curl exits 18 on a partial transfer, which is what catches a body cut off in
  # flight before the number reader ever sees it. Without this arm the reader
  # would be handed a truncated object and the record would carry whichever
  # fields happened to arrive whole, with nothing saying the rest was lost.
  printf '18\n' >"$tmp/imds/shapeConfig.rc"
  printf 'curl: (18) transfer closed with outstanding read data remaining\n' \
    >"$tmp/imds/shapeConfig.err"

  run_identity

  assert_file_contains 'shapeConfig: unavailable' "$identity_file"
  assert_file_contains 'curl exited 18' "$identity_file"
  assert_file_lacks_pattern 'shapeConfig.ocpus' "$identity_file"
  rm -rf "$tmp"
}

@test "a probe that answered nothing is not filed as a service that answered" {
  tmp=$(mktemp -d)
  stage "$tmp"
  # Exit zero and an empty body from the probe key. Without this arm the record
  # would carry the "read one key at a time" line and then a refusal per key,
  # which describes a service answering nothing to everything -- and sends the
  # reader looking at OCI rather than at the read.
  : >"$tmp/imds/shape"
  rc=0

  run_identity || rc=$?

  [ "$rc" -eq 0 ]
  assert_file_contains 'imds unreachable' "$identity_file"
  assert_file_contains 'returned nothing for this key' "$identity_file"
  rm -rf "$tmp"
}

@test "a probe answering something that is not a scalar ends the metadata read" {
  tmp=$(mktemp -d)
  stage "$tmp"
  # Something else on this address answering 200: another cloud's metadata
  # service, a proxy error page, a redirect target. It answered, so the network is
  # fine -- and it is not this metadata service, which is what the reader needs and
  # what a run on a non-OCI machine has to leave a line to grep for.
  printf 'Not Found\n' >"$tmp/imds/shape"
  rc=0

  run_identity || rc=$?

  [ "$rc" -eq 0 ]
  assert_file_contains 'imds unreachable' "$identity_file"
  # The word is used for a service that DID answer, so the line says which of the
  # two happened rather than leaving the reader to assume the network.
  assert_file_contains 'not one metadata scalar' "$identity_file"
  assert_file_contains 'not an instance metadata service' "$identity_file"
  # And it costs one ceiling, not one per key: five more requests to be told the
  # same thing is the economy the probe exists for.
  attempts=$(awk 'END { print NR }' "$curl_args")
  [ "$attempts" -eq 1 ] || {
    echo "FAIL: the capture made $attempts requests against a service that is not this one"
    cat "$curl_args"
    false
  }
  rm -rf "$tmp"
}

@test "a host tool that printed something and then failed says so beside it" {
  tmp=$(mktemp -d)
  stage "$tmp"
  # Output AND a non-zero status: the tool printed something and then failed, so
  # what is in hand may be a prefix of the answer. Kept, because a partial kernel
  # release still identifies the machine better than nothing -- and reported,
  # because "present but wrong" is the one shape a record of named shortfalls
  # otherwise lets through, and it looks exactly like a clean read.
  cat >"$tmp/bin/uname" <<'STUB'
#!/bin/sh
printf '6.8.0-10\n'
exit 1
STUB
  chmod +x "$tmp/bin/uname"

  run_identity

  assert_file_contains 'kernel release: 6.8.0-10' "$identity_file"
  assert_file_contains 'uname exited 1 after printing this' "$identity_file"
  rm -rf "$tmp"
}

@test "a host tool that answered cleanly carries no note beside its value" {
  tmp=$(mktemp -d)
  stage "$tmp"

  run_identity

  # The other side of the arm above: a clean read must not acquire a parenthetical
  # of its own, or the note stops meaning anything.
  assert_file_contains 'kernel release: 6.8.0-1021-oracle' "$identity_file"
  assert_file_lacks_pattern 'kernel release:.*exited' "$identity_file"
  rm -rf "$tmp"
}

@test "a host tool that answers nothing names itself rather than going quiet" {
  tmp=$(mktemp -d)
  stage "$tmp"
  # An lscpu that prints a table with no model line, and an nproc that prints
  # nothing: the two shapes a container image with a partial coreutils produces.
  # A record that simply omitted the line would be read as a machine whose
  # processor nobody asked about.
  printf 'Vendor ID:                          AuthenticAMD\n' >"$tmp/lscpu.out"
  : >"$tmp/nproc.out"

  run_identity

  assert_file_contains 'cpu model: unavailable' "$identity_file"
  assert_file_contains 'nproc: unavailable' "$identity_file"
  # And the metadata half is untouched by either of them.
  assert_file_contains 'shape: VM.Standard.E5.Flex' "$identity_file"
  rm -rf "$tmp"
}

@test "a host read that never returns is cut off instead of holding the step" {
  tmp=$(mktemp -d)
  stage "$tmp"
  # `lscpu` walks sysfs, and a machine wedged enough to be worth identifying is
  # one where that walk can block. Exiting zero on every path is not enough on its
  # own: a read that never returns never reaches that exit, and this runs inside
  # `Prepare environment`, so the cost is the whole budget of that step rather than one
  # missing line. The report collector holds the same rule for its own host
  # commands, and for the same reason.
  printf '#!/bin/sh\nsleep 60\n' >"$tmp/bin/lscpu"
  chmod +x "$tmp/bin/lscpu"
  rc=0
  started=$(date +%s)

  run_identity || rc=$?

  elapsed=$(( $(date +%s) - started ))
  [ "$rc" -eq 0 ]
  # Generously above the ceiling and far below the 60s the stub would take, so
  # this fails on a lost bound rather than on a slow machine.
  [ "$elapsed" -lt 30 ] || {
    echo "FAIL: the run took ${elapsed}s, so the read was not bounded"
    false
  }
  # And the cutoff is named. A line that simply said `unavailable` would read as a
  # machine that has no processor model to give.
  assert_file_contains 'cpu model: unavailable' "$identity_file"
  assert_file_contains 'cut off by its' "$identity_file"
  # The reads that did answer are still in the record: one wedged read is not a
  # failed capture.
  assert_file_contains 'shape: VM.Standard.E5.Flex' "$identity_file"
  rm -rf "$tmp"
}

@test "reads taken with no ceiling at all are declared rather than passed off" {
  # A stripped PATH with no `timeout` in it. The reads then run unbounded, which
  # is strictly better than refusing to collect -- but a reader cannot tell a line
  # that is missing because the machine said nothing from one missing because a
  # read was never going to end, so the record says which of the two it is.
  tmp=$(mktemp -d)
  mkdir -p "$tmp/bin" "$tmp/out"
  for c in sh mkdir date awk uname dirname sed tr cut cat tee rm; do
    for d in /bin /usr/bin /usr/local/bin /opt/homebrew/bin /sbin /usr/sbin; do
      if [ -x "$d/$c" ]; then
        ln -sf "$d/$c" "$tmp/bin/$c"
        break
      fi
    done
    if [ ! -x "$tmp/bin/$c" ]; then
      echo "FAIL: could not stage $c in the stripped PATH; the check below would be vacuous" >&2
      return 1
    fi
  done
  if [ -e "$tmp/bin/timeout" ]; then
    echo "FAIL: timeout leaked into the stripped PATH; this test would prove nothing" >&2
    return 1
  fi
  rc=0

  ( set +x
    PATH="$tmp/bin" COZY_RUNNER_IDENTITY_FILE="$tmp/out/runner-identity.txt" \
      sh "$SCRIPT" >"$tmp/stdout" 2>"$tmp/stderr"
  ) || rc=$?

  [ "$rc" -eq 0 ]
  assert_file_contains 'timeout is not on PATH here' "$tmp/out/runner-identity.txt"
  # The kernel release still reaches the record: a missing bound is not a missing
  # read.
  assert_file_contains 'kernel release: ' "$tmp/out/runner-identity.txt"
  rm -rf "$tmp"
}

@test "a write that would never return is cut off rather than holding the step" {
  tmp=$(mktemp -d)
  stage "$tmp"
  # A FIFO with no reader: `tee` blocks on the open and never returns, which is
  # the shape a wedged mount under `_out` produces. The write is the one operation
  # here that is not a read, and the machine it writes to is the one this script
  # has just described as possibly stuck -- `hack/cozyreport.sh` bounds `df` on
  # that same host for exactly this reason.
  if ! command -v timeout >/dev/null 2>&1; then
    echo "no timeout on PATH, so the arm under test is not armed here" >&2
    rm -rf "$tmp"
    return 0
  fi
  mkdir -p "$tmp/out"
  rm -f "$identity_file"
  mkfifo "$identity_file"
  rc=0

  # Wrapped rather than timed, because a lost bound makes this run forever: measured
  # afterwards, the mutant that removes the ceiling stops the whole suite instead of
  # failing this one test, and a hanging suite is worse in CI than a red one.
  ( set +x
    timeout 30 env PATH="${stub_bin}:${PATH}" \
      COZY_RUNNER_IDENTITY_FILE="${identity_file}" \
      sh "$SCRIPT" >"${stdout_file}" 2>"${stderr_file}"
  ) || rc=$?

  [ "$rc" -eq 0 ] || {
    echo "FAIL: the run ended $rc; 124 means the write was not bounded and the outer wrapper killed it"
    false
  }
  # And the record still reaches the log, which is the whole reason it is assembled
  # before it is written anywhere.
  assert_file_contains 'shape: VM.Standard.E5.Flex' "$stdout_file"
  assert_file_contains 'will be missing from cozyreport.tgz' "$stderr_file"
  rm -f "$identity_file"
  rm -rf "$tmp"
}

@test "a record that could not be written is still in the log and says so" {
  tmp=$(mktemp -d)
  stage "$tmp"
  # A file where the directory has to go, so `mkdir -p` refuses and the write
  # after it has nowhere to land. Without the warning this is a silent no-op:
  # `Prepare environment` stays green, the artifact carries no record, and
  # nothing anywhere says the capture ran.
  mkdir -p "$tmp/out"
  rm -rf "$tmp/out"
  printf 'not a directory\n' >"$tmp/out"
  rc=0

  run_identity || rc=$?

  [ "$rc" -eq 0 ]
  assert_file_contains 'shape: VM.Standard.E5.Flex' "$stdout_file"
  assert_file_contains 'will be missing from cozyreport.tgz' "$stderr_file"
  rm -rf "$tmp"
}

@test "a write cut off part way leaves no prefix behind for the artifact" {
  tmp=$(mktemp -d)
  stage "$tmp"
  # A tee killed mid-write -- the bound firing on a wedged mount -- leaves a
  # prefix on disk, and a prefix of this record is indistinguishable from a
  # whole one to the fold-in. Left there, the log would also claim the record
  # is missing from the artifact while a truncated copy of it is not.
  cat >"${stub_bin}/tee" <<'STUB'
#!/bin/sh
head -c 40 >"$1"
exit 1
STUB
  chmod +x "${stub_bin}/tee"
  rc=0

  run_identity || rc=$?

  [ "$rc" -eq 0 ]
  [ ! -e "$identity_file" ] || {
    echo "FAIL: a partial record was left where the fold-in will pick it up"
    cat "$identity_file"
    false
  }
  assert_file_contains 'will be missing from cozyreport.tgz' "$stderr_file"
  rm -rf "$tmp"
}

@test "a partial record that could not be removed is named as possibly remaining" {
  tmp=$(mktemp -d)
  stage "$tmp"
  cat >"${stub_bin}/tee" <<'STUB'
#!/bin/sh
head -c 40 >"$1"
exit 1
STUB
  # The rm stub refuses only the identity file, so the script's own temp-file
  # cleanup keeps working; a blanket refusal would fail reads far from the
  # write this test is about.
  cat >"${stub_bin}/rm" <<'STUB'
#!/bin/sh
for a; do case "$a" in *runner-identity*) exit 1 ;; esac; done
exec /bin/rm "$@"
STUB
  chmod +x "${stub_bin}/tee" "${stub_bin}/rm"
  rc=0

  run_identity || rc=$?

  [ "$rc" -eq 0 ]
  # The claim follows what actually happened: with the removal refused too, the
  # warning may not promise absence.
  assert_file_contains 'may remain in cozyreport.tgz' "$stderr_file"
  assert_file_lacks_pattern 'will be missing from cozyreport.tgz' "$stderr_file"
  rm -rf "$tmp"
}

@test "an environment value cannot mint record lines of its own" {
  tmp=$(mktemp -d)
  stage "$tmp"
  # The one environment value printed into the record. Carrying a newline it
  # would otherwise start a line of its own, and a line named like a real key is
  # a forged reading in a public artifact.
  forged=$(printf '999999\nshape: forged')
  rc=0

  ( set +x
    PATH="${stub_bin}:${PATH}" COZY_RUNNER_IMDS_TIMEOUT="$forged" \
      COZY_RUNNER_IDENTITY_FILE="$identity_file" \
      sh "$SCRIPT" >"$stdout_file" 2>"$stderr_file"
  ) || rc=$?

  [ "$rc" -eq 0 ]
  assert_file_contains 'was not usable as a ceiling' "$identity_file"
  if grep -q '^shape: forged' "$identity_file"; then
    echo "FAIL: an environment value minted a record line of its own"
    grep -n 'forged' "$identity_file"
    false
  fi
  rm -rf "$tmp"
}

# ---------------------------------------------------------------------------
# The wiring. Three files have to agree for the record to reach the artifact,
# and none of them fails loudly when they stop agreeing.
# ---------------------------------------------------------------------------

@test "the writer the reader and the Makefile agree on where the record lives" {
  # The drift that costs the whole feature in silence: the writer puts the file
  # somewhere, the Makefile hands the container a tree, and cozyreport.sh looks
  # for it from inside. Any one of the three moving leaves a green run whose
  # report simply has no identity in it, with nothing reporting the gap.
  default='_out/runner-identity.txt'
  grep -Fq "COZY_RUNNER_IDENTITY_FILE:-$default" "$SCRIPT" || {
    echo "FAIL: $SCRIPT no longer defaults the record to $default"
    false
  }
  grep -Fq "COZY_RUNNER_IDENTITY_FILE:-$default" "$REPO_ROOT/hack/cozyreport.sh" || {
    echo "FAIL: hack/cozyreport.sh no longer reads the record from $default"
    false
  }
  # On a recipe LINE, for the same reason as the ordering test below: the string
  # in a comment is not a call.
  awk '/^\t/ && $0 !~ /^\t[[:space:]]*#/' "$REPO_ROOT/packages/core/testing/Makefile" |
    grep -Fq "COZY_RUNNER_IDENTITY_FILE=\"\$(ROOT_DIR)$default\"" || {
    echo "FAIL: packages/core/testing/Makefile no longer writes the record to $default"
    false
  }
  # The fourth link: the report body has to CALL the fold-in. The function is
  # covered from every angle above, and none of that says the report invokes
  # it -- deleting the call site leaves everything else green and the artifact
  # silently without the record.
  grep -Eq '^cozyreport_collect_runner_identity "' "$REPO_ROOT/hack/cozyreport.sh" || {
    echo "FAIL: hack/cozyreport.sh no longer calls cozyreport_collect_runner_identity"
    false
  }
}

@test "the capture runs before the sandbox container is created" {
  # Ordering, not presence. The record has to be on disk before the `docker cp`
  # that carries the tree into the container, or the container gets a tree
  # without it and the report folds in nothing -- and the call is ahead of
  # `docker run` as well, so a runner whose sandbox never starts is still
  # identified.
  # Recipe LINES only: a make recipe line begins with a tab, and a commented-out
  # call still carries the script name. Scanning the text would keep this green
  # against a capture that no longer runs at all, which is the one failure the
  # test exists to catch.
  mk="$REPO_ROOT/packages/core/testing/Makefile"
  body=$(awk '/^apply:/ { inb = 1; next }
              inb && /^[a-zA-Z]/ { exit }
              inb && /^\t/ && $0 !~ /^\t[[:space:]]*#/ { print }' "$mk")
  order=$(printf '%s\n' "$body" |
    awk '/e2e-runner-identity\.sh/ { print "identity" }
         /docker run/            { print "run" }
         /docker cp/             { print "cp" }' |
    tr '\n' ' ')
  case "$order" in
    'identity run cp '*) : ;;
    *)
      echo "FAIL: the apply recipe runs [$order]; the identity capture must come first"
      printf '%s\n' "$body"
      false
      ;;
  esac
}

@test "the report folds a record it finds into the sandbox host directory" {
  tmp=$(mktemp -d)
  mkdir -p "$tmp/report"
  printf '=== e2e runner identity ===\nshape: VM.Standard.E5.Flex\n' >"$tmp/record.txt"

  COZY_RUNNER_IDENTITY_FILE="$tmp/record.txt" \
    cozyreport_collect_runner_identity "$tmp/report"

  assert_file_contains 'shape: VM.Standard.E5.Flex' "$tmp/report/runner-identity.txt"
  rm -rf "$tmp"
}

@test "the report treats a record that arrived empty as no record at all" {
  tmp=$(mktemp -d)
  mkdir -p "$tmp/report"
  # A write cut off by a full disk leaves the file there and empty. Copied
  # through, that is a zero-byte runner-identity.txt in the report holding neither
  # the record nor a reason, which is exactly the outcome the one-filename promise
  # is supposed to rule out.
  : >"$tmp/record.txt"

  COZY_RUNNER_IDENTITY_FILE="$tmp/record.txt" \
    cozyreport_collect_runner_identity "$tmp/report"

  assert_file_contains 'absent, or present and empty' "$tmp/report/runner-identity.txt"
  rm -rf "$tmp"
}

@test "the report names every cause of a missing record rather than picking one" {
  tmp=$(mktemp -d)
  mkdir -p "$tmp/report"

  COZY_RUNNER_IDENTITY_FILE="$tmp/gone.txt" \
    cozyreport_collect_runner_identity "$tmp/report"

  # The capture exits 0 whatever happens, so an absent file does not say which of
  # the three it was -- and one of them leaves the record in the job log, which is
  # where a reader would then go. Asserting a single cause would be this collector
  # naming a mechanism it did not observe.
  assert_file_contains 'the capture step did not run' "$tmp/report/runner-identity.txt"
  assert_file_contains 'could not write its file' "$tmp/report/runner-identity.txt"
  assert_file_contains 'in the log of the step that took it' "$tmp/report/runner-identity.txt"
  rm -rf "$tmp"
}

@test "the report writes the same filename when there is no record to fold in" {
  tmp=$(mktemp -d)
  mkdir -p "$tmp/report"

  COZY_RUNNER_IDENTITY_FILE="$tmp/does-not-exist.txt" \
    cozyreport_collect_runner_identity "$tmp/report"

  # One filename, present on every run. A marker file beside an absent one would
  # carry the same words and be found by nobody, because the absence is where a
  # reader stops.
  assert_file_contains 'no runner identity record' "$tmp/report/runner-identity.txt"
  # And it says what the absence does not mean. An unidentified runner and an
  # unremarkable one leave the same empty space, and only one of them is a
  # statement about the run.
  assert_file_contains 'NOT a statement that the runner was an ordinary one' \
    "$tmp/report/runner-identity.txt"
  # In the shared marker form, so one anchored grep over the unpacked tarball
  # finds this alongside every other note the collector leaves.
  grep -q '^# \[cozyreport\]' "$tmp/report/runner-identity.txt" || {
    echo "FAIL: the explanation does not carry the shared marker form"
    cat "$tmp/report/runner-identity.txt"
    false
  }
  rm -rf "$tmp"
}
