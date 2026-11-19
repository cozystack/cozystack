#!/bin/sh
#
# e2e-runner-identity.sh -- record which machine an e2e run landed on.
#
# WHY THE RECORD IS NEEDED AT ALL
#
# The e2e lanes ask for a runner class, not for a machine. A flex shape is
# filled from whatever capacity the pool has at the time, so the same lane can
# be served by two processor generations; the region is a preference with a
# fallback, so it can be served out of a second one. None of that reached the
# report, which means a red run and a green one could not be compared by the
# hardware they ran on -- the first question anyone asks about a flake that some
# runs have and others do not. That comparison is the whole of what this file
# exists to make possible.
#
# WHY IT RUNS A LAYER ABOVE EVERYTHING ELSE IN THE REPORT
#
# The metadata service answers on a link-local address. The sandbox container is
# not guaranteed to have a route to it, and a capture that answered "unreachable"
# from in there would be saying nothing about the machine while looking exactly
# like a capture that had tried. So the read is taken on the runner VM, before
# the container exists, and the record is written into the repository tree that
# `packages/core/testing/Makefile`'s `apply` target then copies into the
# container. hack/cozyreport.sh folds that file into the artifact from inside,
# without going near the endpoint itself.
#
# That placement is also what makes the record unconditional. Every workflow that
# runs the suite reaches the sandbox through `make prepare-env`, so a capture
# wired into `apply` is taken once per run in each of them, on the green path and
# the red one alike, with no per-workflow step to keep in step. Written without a
# count of those workflows on purpose: the count is what goes stale, and the
# funnel is the property worth stating.
#
# WHY ONE READ PER KEY RATHER THAN ONE READ OF THE DOCUMENT
#
# The root instance document is the obvious thing to fetch and the wrong thing to
# fetch. It carries the instance and compartment OCIDs, the display name, the
# hostname, the image OCID and the whole user-supplied metadata bag, which on a
# real instance holds authorized keys -- and this record is published as a CI
# artifact. Fetching it and then filtering makes the privacy property something a
# reader has to trust a filter for; fetching the individual keys makes it
# something the wire settles, because the values that must not be published are
# never asked for.
#
# It also removes a guess. Oracle documents the shape sizing under its own
# subpath, `shapeConfig`, and the root document it documents does not carry that
# object at all -- so a reader hunting `shapeConfig` inside the root document
# would have reported the size of every machine as unknown, forever, and blamed
# the service for it.
#
# The cost is one round trip per key instead of one for all of them, over a
# link-local address, and it is bounded twice: each read carries its own ceiling,
# and the first read decides whether there is a service to talk to at all. On a
# machine that is not an OCI instance nothing after that first read is attempted.
#
# NEVER FATAL, AND NEVER SLOW, ON ANY PATH
#
# This runs inside `Prepare environment`, which is the one retried step in the
# job. A diagnostic that can fail that step would cost a whole run to say
# something about the hardware, so every read here is allowed to fail and each
# failure is written down in the record instead: a runner that is not an OCI
# instance, or one whose metadata service does not answer, produces an `imds
# unreachable` line and an exit status of 0. The script therefore sets no
# errexit; `set -u` stays, since an unset variable here is a bug in this file
# rather than a fact about the runner.
#
# Exiting zero is not enough on its own, because a read that never returns costs
# the step its whole budget without ever reaching that exit. So every read here is
# taken through a wall-clock ceiling, which is the same rule hack/cozyreport.sh
# holds for the host commands it runs, and for the same reason: `lscpu` walks
# sysfs, and a machine wedged enough to be worth identifying is one where that walk
# can block. The metadata reads carry curl's own ceiling; the local ones are
# wrapped in `timeout`.
#
# With one stated exception, taken from the same collector: where `timeout` is not
# on PATH the local reads run unbounded rather than not at all, because a machine
# with no `timeout` is a missing dependency here and not a fact about the runner,
# and refusing to collect would trade the whole record for it. The record says so
# in a `[bounds]` line, so a reader is never left to infer which of the two a
# missing value was.
#
# Environment:
#   COZY_RUNNER_IDENTITY_FILE   where the record is written (default
#                               `_out/runner-identity.txt`, relative to the
#                               caller's cwd). hack/cozyreport.sh reads the same
#                               default from inside the sandbox container, where
#                               the repository root is /workspace; changing it
#                               here only means the report folds in nothing. On the
#                               CI path packages/core/testing/Makefile SETS this
#                               rather than deferring to an inherited value,
#                               because the record has to land inside the tree that
#                               is copied into the container: a path outside it
#                               would be written and then never carried in. The
#                               knob is for the report side and for tests.
#   COZY_RUNNER_IMDS_TIMEOUT    curl's own ceiling on each metadata read, in
#                               seconds (default 5). A malformed value falls back
#                               to the default and the record says which value it
#                               used. Zero is malformed for this knob rather than
#                               meaning "no wait": `--max-time 0` removes the
#                               ceiling outright, the same way a zero passed to
#                               `timeout` disables it rather than firing at once.
#   E2E_RUNNER_IDENTITY_LIB     set to define the helpers and return without
#                               reading anything or writing anything, so the unit
#                               suite can exercise them directly.

set -u

# Every label this script matches on is an English one, and `lscpu` translates
# its own. Set once for the whole script rather than in front of the one read
# that needs it, so a reader does not have to check which of them are locale
# dependent.
LC_ALL=C
export LC_ALL

COZY_RUNNER_IDENTITY_FILE=${COZY_RUNNER_IDENTITY_FILE:-_out/runner-identity.txt}
COZY_RUNNER_IMDS_TIMEOUT_DEFAULT=5

# The OCI instance metadata endpoint, v2. Held as a literal address rather than a
# name, which is what makes curl's own `--max-time` a hard ceiling here: the one
# phase that option does not cover is a blocking name lookup in a curl built
# without an asynchronous resolver, and there is no name to look up.
COZY_RUNNER_IMDS_BASE='http://169.254.169.254/opc/v2/instance/'

# The keys this record keeps, in the order they are read and printed. `shape`
# comes first because it doubles as the probe: if it does not answer, there is no
# metadata service here and nothing below it is attempted.
#
# Both region keys, because they are not the same string: the root key is
# shortened for some regions (`phx`) while the canonical one is always the full
# identifier (`us-phoenix-1`), and a record carrying only the short form makes a
# region fallback harder to read, not easier.
COZY_RUNNER_IMDS_KEYS='shape region canonicalRegionName availabilityDomain faultDomain'

# How long a metadata scalar may be, and which characters it may hold.
#
# What keeps the values that must not be published out of this record is that they
# are never requested. This guard is narrower and worth describing as what it is: a
# shape check that refuses an answer which is not ONE value. A service answering the
# root document, an error page, a redirect target's page or a multi-line body to a
# per-key request would otherwise have that printed into the record under a key
# name, and every value this record does keep is drawn from a small alphabet
# (`VM.Standard.E5.Flex`, `us-chicago-1`, `EMIr:PHX-AD-1`, `FAULT-DOMAIN-3`).
#
# What it does NOT do, said here so nobody reads more into it: an identifier or a
# hostname is inside that alphabet and inside that length, so a service answering
# one of those to a per-key request would pass this check. The reason that is not
# a hole in the design is that no key returning one is ever asked for.
COZY_RUNNER_SCALAR_MAX=128
COZY_RUNNER_SCALAR_CLASS='A-Za-z0-9:._-'

# The same bound on the key side of the sizing object. A key is printed into the
# record exactly as a value is, so it is held to a shape and a length too; the
# longest key this object has published is `networkingBandwidthInGbps`.
COZY_RUNNER_KEY_MAX=64

# The ceiling on each read of local machine state, and the wrapper that applies
# it, resolved once so that every read gets the same answer.
#
# Empty when `timeout` is absent: the reads then run unbounded and the record says
# so, rather than every one of them exiting 127 and this script reporting a
# machine with no kernel, no CPUs and no processor. Five seconds because what goes
# through here is two clock reads, a uname, a CPU count and one sysfs walk;
# anything slower than that is the finding rather than the measurement.
COZY_RUNNER_READ_TIMEOUT=5
if command -v timeout >/dev/null 2>&1; then
  COZY_RUNNER_BOUND="timeout -k 2 $COZY_RUNNER_READ_TIMEOUT"
else
  COZY_RUNNER_BOUND=""
fi

# runner_read <command> [args...]: one bounded read of local machine state.
#
# Leaves its output in RUNNER_READ_OUT and, when there is none, the reason in
# RUNNER_READ_WHY; returns 0 only when something was read. Two globals rather
# than stdout, because a caller taking this through `$( )` would run it in a
# subshell and the reason would die there -- and the reason is the whole point:
# a record that simply omits a line reads as a machine nobody asked about.
runner_read() {
  RUNNER_READ_OUT=''
  RUNNER_READ_WHY=''
  _rr_rc=0
  # shellcheck disable=SC2086  # an empty COZY_RUNNER_BOUND must vanish, not become ""
  RUNNER_READ_OUT=$($COZY_RUNNER_BOUND "$@" 2>/dev/null) || _rr_rc=$?
  if [ -n "$RUNNER_READ_OUT" ]; then
    # Output AND a non-zero status: the tool printed something and then failed, so
    # what is in hand may be a prefix of the answer. Kept, because a partial
    # kernel release still identifies the machine better than nothing, and
    # reported, because "present but wrong" is the one shape this file's premise
    # -- every shortfall named -- otherwise lets through.
    [ "$_rr_rc" -eq 0 ] || RUNNER_READ_WHY="$1 exited $_rr_rc after printing this, so it may be a partial answer"
    return 0
  fi
  case "$_rr_rc" in
    124 | 137)
      # 124 is the deadline expiring and 137 is 128+SIGKILL, which our own `-k`
      # grace produces and so does anything else that kills the read. With no
      # bound in place neither can be ours, and naming a ceiling that was never
      # applied would be a mechanism this script did not observe.
      if [ -z "$COZY_RUNNER_BOUND" ]; then
        RUNNER_READ_WHY="$1 was killed from outside this script, which ran its reads with no ceiling"
      else
        RUNNER_READ_WHY="$1 was cut off by its ${COZY_RUNNER_READ_TIMEOUT}s ceiling (exit $_rr_rc)"
      fi
      ;;
    127) RUNNER_READ_WHY="$1 is not on PATH" ;;
    0) RUNNER_READ_WHY="$1 answered with nothing" ;;
    *) RUNNER_READ_WHY="$1 exited $_rr_rc" ;;
  esac
  return 1
}

# runner_imds_timeout: the number to hand curl, and nothing else.
#
# Refused rather than passed through: a value that is not a plain run of digits,
# a zero, a leading zero, or something long enough to be a mistake. Zero is the
# consequential one -- `--max-time 0` means "no ceiling", the same way a zero
# handed to `timeout` disables it rather than firing at once, so the malformed case
# would silently remove the bound this whole function exists to place.
#
# The four-digit cap is this script's own judgement and not curl's: curl accepts
# `--max-time 99999` without complaint and refuses only absurd magnitudes, so
# nothing downstream would catch a mistyped ceiling. Anything past four digits is
# more than two hours of waiting inside a step measured in minutes, which is a
# typo rather than a setting.
runner_imds_timeout() {
  case ${COZY_RUNNER_IMDS_TIMEOUT-} in
    '' | *[!0-9]* | 0* | ?????*) printf '%s' "$COZY_RUNNER_IMDS_TIMEOUT_DEFAULT" ;;
    *) printf '%s' "$COZY_RUNNER_IMDS_TIMEOUT" ;;
  esac
}

# runner_imds_scalar <value>: the value as this record will print it, or nothing
# when what came back is not a scalar.
#
# Surrounding quotes are stripped first, because the service is documented to
# serve these keys as subpaths and not documented to say whether the value
# arrives bare or JSON-quoted. Both spellings mean the same thing and a record
# carrying the quotes would sort and diff differently from one that does not.
runner_imds_scalar() {
  _ris_v=$1
  case $_ris_v in
    '"'*'"') _ris_v=${_ris_v#\"}; _ris_v=${_ris_v%\"} ;;
  esac
  [ -n "$_ris_v" ] || return 1
  [ "${#_ris_v}" -le "$COZY_RUNNER_SCALAR_MAX" ] || return 1
  case $_ris_v in
    *[!$COZY_RUNNER_SCALAR_CLASS]*) return 1 ;;
  esac
  printf '%s' "$_ris_v"
}

# runner_imds_numbers <prefix>: stdin is one flat JSON object, stdout is one
# `<prefix>.<key>: <value>` line per NUMERIC field in it, plus its own notes under
# `<prefix>:`. The prefix is a parameter rather than something the caller sticks
# on afterwards, so that the notes and the values are formatted by whatever knows
# which of the two each line is.
#
# Numbers only, and that is the rule rather than a shortlist of key names. A
# shortlist is the obvious way to write this and has a failure mode worth
# avoiding: the sizing keys have been renamed upstream before, and a list that
# stops matching reports the size of the machine as unknown on every run, quietly.
# Reading whatever the object holds keeps that visible, and keeping only its
# numbers is what stops the rule from being an argument about whatever Oracle may
# add to this object later -- a size is a number, an identifier is a string.
#
# Read with awk rather than with jq, because jq is not part of the runner image
# contract and a missing dependency here would report a machine of unknown size
# while the service was answering fine.
#
# Walked character by character rather than matched by regex, at one object level.
# A regex is shorter and gets two things wrong that a test in this suite pins: a
# number inside a string value is not a number this object published, and a key one
# level down is not a key of this object -- it would arrive in the record under the
# sizing prefix as a measurement of something else.
#
# What the walk requires of its input, and what it does with the rest, because
# neither is obvious from the code and both decide what a reader may believe:
#
#   * The answer must be ONE complete JSON object: `{` first, `}` last, and the
#     braces balancing back to zero only at the last character. Anything else is
#     refused whole rather than read in part, which covers a body cut off in
#     transit, a top-level array, and two objects glued together -- in each case
#     keys this object never published would otherwise arrive under the sizing
#     prefix as though it had.
#   * A KEY is kept only if it is shaped like a field name AND is short enough to
#     be one. This object's keys are `ocpus`, `memoryInGBs` and their like; a
#     service answering something else has its key text refused for the same reason
#     a per-key scalar answer is, since a key is printed into the record just as a
#     value is -- an identifier, a newline that would forge a line of its own, or a
#     twenty-thousand-character run all arrive on the key side.
#   * A repeated key prints once per occurrence. Nothing here dedupes, and the
#     record showing a key twice is the service having sent it twice.
runner_imds_numbers() {
  # shellcheck disable=SC2086  # an empty COZY_RUNNER_BOUND must vanish, not become ""
  # shellcheck disable=SC2016  # the single quotes are the point: awk owns these $
  $COZY_RUNNER_BOUND awk -v prefix="${1:-object}" -v keymax="$COZY_RUNNER_KEY_MAX" '
    { doc = doc $0 "\n" }

    function jspace(c) {
      return (c == " " || c == "\t" || c == "\r" || c == "\n")
    }

    # One sentence, three refusal sites: the endpoint check, a brace count that
    # returns to zero early, and one that never returns at all. A second copy of
    # the sentence would be a scheduled divergence.
    function refuse_doc(prefix) {
      printf "%s: unavailable (the answer was not one complete JSON object, so nothing was read from it)\n", prefix
    }

    # Walks a JSON string, leaving its contents in JSTR and returning the index
    # just past its closing quote. Used for keys, for the string values that are
    # then discarded, and inside a nested value.
    #
    # The escape branch is what stops a `\"` inside a value from being read as the
    # end of that value, which would leave the walk treating the rest of the value
    # as a key and lose the number after it. Nothing in this object carries an
    # escape today, which is exactly why nothing else here would exercise it.
    function jstring(s, i, n,   c, out) {
      out = ""
      i++
      while (i <= n) {
        c = substr(s, i, 1)
        if (c == "\\") { out = out substr(s, i, 2); i += 2; continue }
        if (c == "\"") { JSTR = out; return i + 1 }
        out = out c
        i++
      }
      JSTR = out
      return i
    }

    # Steps over a nested object or array, returning the index just past its
    # close, or 0 when it never closes. Zero rather than "the rest of the input",
    # so that a document cut off mid-value cannot be read as a whole one.
    function jskipnested(s, i, n,   c, depth) {
      depth = 0
      while (i <= n) {
        c = substr(s, i, 1)
        if (c == "\"") { i = jstring(s, i, n); continue }
        if (c == "{" || c == "[") depth++
        else if (c == "}" || c == "]") {
          depth--
          if (depth == 0) return i + 1
        }
        i++
      }
      return 0
    }

    END {
      # One complete object, or nothing. Read in part, a truncated answer is
      # indistinguishable in the record from a machine whose sizing simply has
      # fewer fields -- and a top-level array would publish its nested keys under
      # this prefix.
      body = doc
      sub(/^[ \t\r\n]+/, "", body)
      sub(/[ \t\r\n]+$/, "", body)
      if (substr(body, 1, 1) != "{" || substr(body, length(body), 1) != "}") {
        refuse_doc(prefix)
        exit
      }
      doc = body
      n = length(doc)
      # The endpoints are not enough: two objects glued together start with `{`
      # and end with `}`, and the walk below is flat, so the keys of the second
      # object would publish under this prefix as though the sizing object held
      # them. Complete means the braces balance and first return to zero at the
      # very last character, with strings walked so a brace inside one does not
      # count.
      depth = 0
      j = 1
      while (j <= n) {
        cj = substr(doc, j, 1)
        if (cj == "\"") { j = jstring(doc, j, n); continue }
        if (cj == "{") depth++
        if (cj == "}") {
          depth--
          if (depth == 0 && j < n) { refuse_doc(prefix); exit }
        }
        j++
      }
      if (depth != 0) { refuse_doc(prefix); exit }
      i = 1
      kept = 0
      dropped = 0
      while (i <= n) {
        c = substr(doc, i, 1)
        if (c != "\"") { i++; continue }
        i = jstring(doc, i, n)
        key = JSTR
        while (i <= n && substr(doc, i, 1) != ":") {
          if (substr(doc, i, 1) == "\"") break
          i++
        }
        if (i > n || substr(doc, i, 1) != ":") continue
        i++
        while (i <= n && jspace(substr(doc, i, 1))) i++
        if (i > n) break
        c = substr(doc, i, 1)
        if (c == "\"") {
          i = jstring(doc, i, n)
          dropped++
        } else if (c == "{" || c == "[") {
          e = jskipnested(doc, i, n)
          if (e == 0) break
          i = e
          dropped++
        } else {
          val = ""
          while (i <= n) {
            c = substr(doc, i, 1)
            # `]` is deliberately NOT a terminator here: inside an object it is
            # a mismatched delimiter, so `{"ocpus":16]}` yields the token `16]`,
            # fails the number test, and is counted as a field that was not kept.
            # Treated as a terminator it would render as a clean `16` and the
            # malformed answer would read like a healthy one.
            if (c == "," || c == "}" || jspace(c)) break
            val = val c
            i++
          }
          # The key is printed into the record exactly as the value is, so it is
          # held to the same standard: a field name, or nothing.
          if (val ~ /^-?[0-9]+([.][0-9]+)?$/ &&
              key ~ /^[A-Za-z][A-Za-z0-9_]*$/ && length(key) <= keymax) {
            printf "%s.%s: %s\n", prefix, key, val
            kept++
          } else {
            # `null`, `true` and `false` land here, and so does a number under a
            # key that is not shaped like a field name. None of the first three is
            # a measurement, and printing the word would read as a value the
            # service published; the fourth is a service answering something this
            # record has no business publishing under a key of its own.
            dropped++
          }
        }
      }
      # Counted rather than listed: naming the keys would print the very strings
      # the numeric rule exists to keep out, and a count is enough to say the
      # object held more than this.
      if (dropped > 0) {
        printf "%s: %d further field(s) were not kept -- not a number, or under a key that is not a field name\n", prefix, dropped
      }
      if (kept == 0) {
        printf "%s: unavailable (the object was fetched and held no numeric field)\n", prefix
      }
    }
  '
}

# runner_cpu_model: the processor string, out of a processor table already read.
#
# It answers a different question from the shape: the shape says which class was
# asked for, this says what the hypervisor is presenting as the model, and the two
# disagree exactly when a lane is served by a generation it did not expect.
#
# Takes the table on stdin rather than running `lscpu` itself, so the bounded read
# stays at the one call site that owns the ceiling and this stays a transform. The
# value is taken from the whole line rather than by field, since a model string may
# itself contain a colon.
runner_cpu_model() {
  sed -n 's/^Model name:[[:space:]]*//p' | sed -n '1p'
}

# runner_svm_features: the AMD SVM sub-features this VM's virtual CPU was given.
#
# Same shape as the model reader above and for the same reason: it transforms an
# `lscpu` table handed to it on stdin, so the one bounded read stays at the one
# call site that owns the ceiling.
#
# WHY THIS LINE IS WORTH A PLACE IN THE RECORD
#
# `svm` says a guest may run a hypervisor. The sub-features say what that costs.
# The one that has mattered here is `vgif`: without it, a guest that is itself
# running a hypervisor traps to the layer below on every CLGI and STGI, and those
# bracket every world switch it performs. A guest that runs no hypervisor never
# executes either instruction, so this is invisible until something nests two
# deep -- which is exactly this suite, where the sandbox Talos nodes host the
# tenant KubeVirt VMs. It explains a shape of failure no resource counter can:
# the cycles are spent handling exits rather than running the guest, so the
# runner reads mostly idle with near-zero steal while its nested guests freeze.
#
# Measured across the archived reports, no run whose kernel did not report
# Virtual GIF has ever passed a tenant-Kubernetes suite, and the two hosts
# compared directly differed in this one flag and nothing else -- same shape,
# same processor model, same kernel. On a flex shape filled from shared capacity
# that is not a property of the lane, so it belongs beside the shape and the
# processor rather than in a reader's memory of a run that has since been
# recycled.
#
# The whole SVM list is kept rather than that one flag, because which features a
# host exposes has turned out to vary between hosts of the same shape, and a
# record that answered only the question already asked would have to be extended
# by whoever asks the next one.
#
# Names are the kernel's own from this leaf, matched whole so `svm` cannot be
# taken from `svm_lock`; `-E` and `-x` are both POSIX. `grep` finding nothing is
# a status this reads as an answer, so no branch here -- an Intel host, or a
# guest given no sub-features, prints an empty list and the caller says so.
runner_svm_features() {
  _rsf_flags=$(sed -n 's/^Flags:[[:space:]]*//p' | sed -n '1p')
  # Gated on `svm` itself, and not only for tidiness: `vnmi` is a flag name on
  # Intel too, where it belongs to VMX, so an ungated list prints one SVM
  # sub-feature on a machine that has no SVM at all. Sub-features of an absent
  # feature are not a shorter answer, they are a wrong one. Padded case rather
  # than a grep so `svm` cannot be taken from `svm_lock`.
  case " $_rsf_flags " in
    *' svm '*) ;;
    *) return 0 ;;
  esac
  printf '%s\n' "$_rsf_flags" | tr ' ' '\n' |
    grep -xE 'svm|svm_lock|npt|lbrv|nrip_save|tsc_scale|vmcb_clean|flushbyasid|decodeassists|pausefilter|pfthreshold|avic|x2avic|v_vmsave_vmload|vgif|vnmi|v_spec_ctrl|svme_addr_chk' |
    tr '\n' ' ' | sed 's/[[:space:]]*$//'
}

if [ -n "${E2E_RUNNER_IDENTITY_LIB:-}" ]; then
  return 0 2>/dev/null
fi

record='=== e2e runner identity ==='

# Appends one line. The record is assembled in a variable rather than written as
# it goes, because it has to reach two places -- the file and this job's log --
# and a record half-written to a file that then could not be opened is the one
# outcome that would leave neither.
add() {
  record="$record
$1"
}

add '[layer] Every line below describes the RUNNER VM: the machine the e2e sandbox container is created on, read on that machine before the container existed. Everything else in cozyreport is read from inside that container, from the Talos guests under it, or from the tenant guests under those, so this is the only record of what hardware the run landed on.'
add '[scope] The metadata keys below are fetched one at a time, by name. The root instance document is never requested, so the instance and compartment OCIDs, the image OCID, the display name, the hostname and the user-supplied metadata bag are not filtered out of this record -- they are never asked for. What a key IS asked for is printed as that key value, whatever the service answers, provided it is one short scalar; so this record is a statement about which keys were requested, not a promise about what a misbehaving service could put in one of them. From the shape sizing object only its numbers are kept, under keys shaped and sized like field names.'
if [ -z "$COZY_RUNNER_BOUND" ]; then
  # Said in the record rather than only in the log, because the reads still ran
  # and a reader cannot otherwise tell a line that is missing because the machine
  # said nothing from one missing because a read was never going to end.
  add "[bounds] timeout is not on PATH here, so the local commands below ran with no ceiling; the metadata reads carried curl's own."
fi

# The absolute clock, in both forms and with the units named on each, because an
# epoch integer and an RFC 3339 string subtract to nonsense if a reader takes
# them as printed. It is the instant the machine was identified, which is the
# start of the run rather than the moment of any failure in it.
now_epoch=''
now_iso=''
clock_why=''
if runner_read date +%s; then now_epoch=$RUNNER_READ_OUT; else clock_why=$RUNNER_READ_WHY; fi
# `-u` rather than `--utc`: the long form is GNU-only, and while this script only
# ever runs on a Linux runner, its unit suite runs it on developer machines too.
if runner_read date -u +%Y-%m-%dT%H:%M:%SZ; then now_iso=$RUNNER_READ_OUT; else clock_why=$RUNNER_READ_WHY; fi
if [ -n "$now_epoch" ] && [ -n "$now_iso" ]; then
  add "read at: $now_epoch epoch seconds ($now_iso UTC)"
else
  add "read at: unavailable ($clock_why, so this record carries no clock)"
fi

imds_timeout=$(runner_imds_timeout)
imds_timeout_note=''
if [ -n "${COZY_RUNNER_IMDS_TIMEOUT-}" ] && [ "${COZY_RUNNER_IMDS_TIMEOUT-}" != "$imds_timeout" ]; then
  # Folded and cut like the curl reason below: this is the one place an
  # environment value is printed into the record, and a value carrying a
  # newline would otherwise mint record lines of its own.
  _it_shown=$(printf '%s' "${COZY_RUNNER_IMDS_TIMEOUT}" | tr '\n\r' '  ' | cut -c1-200)
  imds_timeout_note=" (COZY_RUNNER_IMDS_TIMEOUT=${_it_shown} was not usable as a ceiling, so the default was applied instead)"
fi

# imds_read <key>: one bounded metadata read, leaving the body in IMDS_OUT and the
# reason there is none in IMDS_WHY.
#
# `--noproxy '*'` because a runner image may export http_proxy, and a link-local
# metadata request sent to a proxy is answered by the proxy: the read would fail,
# or worse succeed against something else, and the record would describe neither
# this machine nor the failure honestly.
#
# `--fail` so that a host with something else on this address -- another cloud's
# metadata service, which refuses a request carrying no header of its own -- is
# reported as unreachable rather than having its error page filed as a value.
#
# `--location` because Oracle's own examples for these subpaths pass it, and
# `--fail` does not catch a 3xx: without it a redirect's own body would be the
# answer read -- refused as not one metadata scalar -- while following it is what
# yields the value the service meant.
imds_read() {
  IMDS_OUT=''
  IMDS_WHY=''
  _ir_err=$(mktemp "${TMPDIR:-/tmp}/runner-identity.XXXXXX" 2>/dev/null) || _ir_err=''
  _ir_rc=0
  IMDS_OUT=$(curl --silent --show-error --fail --location --noproxy '*' \
    --max-time "$imds_timeout" \
    --header 'Authorization: Bearer Oracle' \
    "${COZY_RUNNER_IMDS_BASE}$1" 2>"${_ir_err:-/dev/null}") || _ir_rc=$?
  _ir_reason=''
  if [ -n "$_ir_err" ] && [ -s "$_ir_err" ]; then
    # Folded to one line, because this becomes one `key: value` line and curl
    # writes its reason with a trailing newline of its own.
    _ir_reason=$(tr '\n' ' ' <"$_ir_err" | sed 's/[[:space:]]*$//' | cut -c1-200)
  fi
  [ -z "$_ir_err" ] || rm -f "$_ir_err"
  if [ "$_ir_rc" -ne 0 ]; then
    # curl's own words are kept. Without them the line is "curl exited 28",
    # which does not separate a machine that is not an OCI instance from one
    # whose metadata service was slow -- and those are opposite conclusions
    # about the run.
    IMDS_WHY="curl exited ${_ir_rc} under a ${imds_timeout}s ceiling${_ir_reason:+: $_ir_reason}"
    return 1
  fi
  if [ -z "$IMDS_OUT" ]; then
    IMDS_WHY="the metadata service exited 0 and returned nothing for this key"
    return 1
  fi
  return 0
}

if ! command -v curl >/dev/null 2>&1; then
  add 'imds unreachable: curl is not on PATH, so the metadata service was never asked'
elif ! imds_read shape; then
  # The first key doubles as the probe. One line rather than one per key, and
  # nothing after it attempted: a machine that is not an OCI instance would
  # otherwise pay the ceiling once per key to say the same thing six times.
  add "imds unreachable: the probe read of ${COZY_RUNNER_IMDS_BASE}shape did not answer, so no further key was asked for${imds_timeout_note} -- ${IMDS_WHY}"
elif ! runner_imds_scalar "$IMDS_OUT" >/dev/null; then
  # Answered, and not like this service answers. A shape name is one short scalar;
  # something else on this address -- another cloud's metadata service, a proxy
  # error page, a redirect target -- answers a document, a sentence or a page.
  #
  # Filed as unreachable rather than as five unavailable keys, and this is the one
  # place that word is used for a service that did answer. The reason is what the
  # reader needs: THIS metadata service is not here. Reporting it per key instead
  # would spend the ceiling five more times to say the same thing, and would leave
  # a run on a non-OCI machine with no line to grep for.
  add "imds unreachable: ${COZY_RUNNER_IMDS_BASE}shape answered ${#IMDS_OUT} bytes that are not one metadata scalar, so this is not an instance metadata service and no further key was asked for${imds_timeout_note}"
else
  add "imds: read one key at a time from ${COZY_RUNNER_IMDS_BASE} under a ${imds_timeout}s curl ceiling each${imds_timeout_note}"
  # The probe already holds `shape`, so it is rendered from that read rather than
  # asked for twice.
  imds_first=1
  for imds_key in $COZY_RUNNER_IMDS_KEYS; do
    if [ "$imds_first" -eq 1 ]; then
      imds_first=0
    elif ! imds_read "$imds_key"; then
      add "$imds_key: unavailable ($IMDS_WHY)"
      continue
    fi
    if imds_value=$(runner_imds_scalar "$IMDS_OUT"); then
      add "$imds_key: $imds_value"
    else
      # Reached, and answered with something that is not one metadata scalar.
      # Printed as a shape rather than as a value, because the thing this arm
      # exists for is a service answering a whole document to a per-key request,
      # and rendering that under a key name is the leak the per-key design
      # removes.
      add "$imds_key: unavailable (the service answered ${#IMDS_OUT} bytes that are not one metadata scalar, so the value was not kept)"
    fi
  done
  if imds_read shapeConfig; then
    # ocpus against the vCPU count the lane advertises is the one arithmetic
    # nothing else in the report can do.
    imds_sizes=$(printf '%s\n' "$IMDS_OUT" | runner_imds_numbers shapeConfig 2>/dev/null) || imds_sizes=''
    if [ -n "$imds_sizes" ]; then
      add "$imds_sizes"
    else
      # The reader prints everything from its END block, so nothing at all means
      # the reader itself did not run to the end: no awk on PATH, or the read
      # ending early. Whether a ceiling could have ended it is decided by
      # whether one was in place -- the same fork runner_read words, and without
      # it this line would contradict the [bounds] line above on a machine with
      # no `timeout`.
      if [ -n "$COZY_RUNNER_BOUND" ]; then
        add 'shapeConfig: unavailable (the object was fetched and the number reader emitted no line at all -- awk is not on PATH here, or it was cut off by its ceiling)'
      else
        add 'shapeConfig: unavailable (the object was fetched and the number reader emitted no line at all -- awk is not on PATH here, or it was killed from outside this script, which ran this read with no ceiling)'
      fi
    fi
  else
    add "shapeConfig: unavailable ($IMDS_WHY)"
  fi
fi

if runner_read uname -r; then
  add "kernel release: $RUNNER_READ_OUT${RUNNER_READ_WHY:+ ($RUNNER_READ_WHY)}"
else
  add "kernel release: unavailable ($RUNNER_READ_WHY)"
fi

if runner_read nproc; then
  # Named `nproc` after the tool, because it is the count of online logical CPUs
  # and reads against shapeConfig.ocpus rather than replacing it: the pair is
  # what shows the lane's advertised vCPU count to be half that many cores.
  add "nproc: $RUNNER_READ_OUT${RUNNER_READ_WHY:+ ($RUNNER_READ_WHY)}"
else
  add "nproc: unavailable ($RUNNER_READ_WHY)"
fi

if runner_read lscpu; then
  cpu_model=$(printf '%s\n' "$RUNNER_READ_OUT" | runner_cpu_model)
  if [ -n "$cpu_model" ]; then
    add "cpu model: $cpu_model${RUNNER_READ_WHY:+ ($RUNNER_READ_WHY)}"
  else
    add 'cpu model: unavailable (lscpu answered, and no Model name line was taken from its table)'
  fi
  # Out of the same read as the model, so the two lines describe one reading of
  # one machine rather than two that a slow first read could have separated.
  svm_features=$(printf '%s\n' "$RUNNER_READ_OUT" | runner_svm_features)
  if [ -n "$svm_features" ]; then
    add "svm features: $svm_features${RUNNER_READ_WHY:+ ($RUNNER_READ_WHY)}"
  else
    # Distinguished from a missing line on purpose: an empty list is a fact about
    # the machine -- no SVM sub-feature was exposed to it -- and a reader who
    # cannot tell that from a read that never happened learns nothing from either.
    add 'svm features: none (lscpu answered, and its flag list named no SVM sub-feature; an Intel host, or a guest given none)'
  fi
else
  add "cpu model: unavailable ($RUNNER_READ_WHY)"
  add "svm features: unavailable ($RUNNER_READ_WHY)"
fi

# The file first, the log second, and neither conditional on the other. The file
# is what reaches the artifact and the log is what reaches a reader who never
# downloads it, so a write that fails must still leave the record somewhere and
# say that the artifact copy is missing.
#
# Bounded like the reads, and for the same reason one directory up: `_out` is a
# path on a machine this script has just described as possibly wedged, and
# hack/cozyreport.sh bounds `df` on that same host because a stale mount blocks
# indefinitely. Through `tee` rather than a redirection, because a redirection is
# the shell's and cannot be wrapped.
identity_dir=$(dirname "$COZY_RUNNER_IDENTITY_FILE")
# shellcheck disable=SC2086  # an empty COZY_RUNNER_BOUND must vanish, not become ""
if $COZY_RUNNER_BOUND mkdir -p "$identity_dir" 2>/dev/null &&
  printf '%s\n' "$record" | $COZY_RUNNER_BOUND tee "$COZY_RUNNER_IDENTITY_FILE" >/dev/null 2>&1; then
  :
else
  # A bound that killed tee mid-write leaves a prefix on disk, which the
  # fold-in cannot tell from a whole record; removing it is what makes the
  # "missing from the artifact" claim below true rather than hopeful. Which
  # warning to print is then decided by looking at the disk, not by rm's exit
  # status: rm also fails where nothing was ever written -- a parent that is
  # not a directory -- and "may remain" about a file that does not exist is a
  # forged doubt. Both commands bounded like the write they follow.
  # shellcheck disable=SC2086  # an empty COZY_RUNNER_BOUND must vanish, not become ""
  $COZY_RUNNER_BOUND rm -f "$COZY_RUNNER_IDENTITY_FILE" 2>/dev/null || :
  # shellcheck disable=SC2086
  if $COZY_RUNNER_BOUND test -e "$COZY_RUNNER_IDENTITY_FILE" 2>/dev/null; then
    echo "WARNING: could not write the runner identity record to $COZY_RUNNER_IDENTITY_FILE; it is in this log, and a partial copy of it may remain in cozyreport.tgz" >&2
  else
    echo "WARNING: could not write the runner identity record to $COZY_RUNNER_IDENTITY_FILE; it is in this log and will be missing from cozyreport.tgz" >&2
  fi
fi

printf '%s\n' "$record"

# Always. The contract of this script is that it describes the machine or says
# why it could not, and a non-zero exit here fails `Prepare environment`, which
# would trade a whole run for a diagnostic.
exit 0
