#!/bin/sh
#
# Environment:
#   COZY_REPORT_PODS_MAX     max not-Ready pods to collect per-pod evidence for
#                            (default 40). The overflow is named in
#                            COLLECTION-TRUNCATED.txt, never dropped in silence.
#   COZY_REPORT_PODS_BUDGET  wall-clock seconds for that walk (default 600). A
#                            deadline for starting a read, not for finishing the
#                            walk, and rechecked between containers, so the ceiling
#                            is this plus one pod's object reads plus one
#                            container's current AND previous log reads -- a fixed
#                            overshoot, not one that grows with the container
#                            count. The two log reads are consecutive, with no
#                            deadline check between them.
#   COZY_REPORT_POD_TAIL     --tail for the per-pod container logs (default 2000;
#                            -1 for kubectl's own default of the complete log).
#   COZY_REPORT_DIR          where the snapshot tree this report folds in lives
#                            (default `_out/cozyreport`). Shared with
#                            hack/cozytest.sh and hack/e2e-chainsaw/.chainsaw.yaml,
#                            which write the per-test snapshots this script then
#                            picks up; changing it in one place only means the
#                            report collects an empty snapshots directory.
#   COZY_REPORT_PREFER_NS    namespace prefix collected first, so the pod cap is
#                            not spent on unrelated platform namespaces (default
#                            `tenant-`; empty disables the ordering).
#   COZY_RUNNER_IDENTITY_FILE  the record hack/e2e-runner-identity.sh left on the
#                            runner VM, carried into this container with the tree
#                            (default `_out/runner-identity.txt`, which is where
#                            packages/core/testing/Makefile writes it and, inside
#                            the container, sits under /workspace). Copied into
#                            the report rather than re-read here: it describes the
#                            machine a layer above this container, whose metadata
#                            service this container has no guaranteed route to.
# A malformed value in any numeric knob falls back to its default and says so in
# the report.
#
# EVERY read below carries a wall-clock ceiling, and a ceiling that fires is
# recorded in the artifact rather than in the job log alone. A test derives that
# from the code, so this sentence is checked rather than maintained -- which the
# list it replaced was not: it named four sections while the file had ten.
#
# Which reader a new read belongs to:
#   cozyreport_read_object     its output IS a file; the marker goes in.
#   cozyreport_select_objects  it feeds a loop, and dash pipelines lose the status.
#   cozyreport_probe           it is a gate deciding whether a module runs.
#   cozyreport_read_exec       it runs a command INSIDE a pod, so its 124 is
#                              ambiguous and its marker is placed by output format.
# The bare wrapper is for reads whose output is not the reader's to write -- a
# variable, or a pipeline that transforms it before anything lands -- so there is
# no point at which a marker could attach. Each says so where it sits.
#
# A ceiling per read is NOT a ceiling on the report. The pod, VM, VMI and services
# walks carry a count cap and a time budget; the other loops carry neither, so
# their cost grows with the cluster. A per-read bound removes the single read that
# never returns; what bounds the rest is the step's own ceiling, below.
# The pod cap and collection budget apply to the pod section only. The fixed
# --tail=2000 reads elsewhere in this file do not name their bound.
#
# THE TOTAL, which no individual section states and which is the number that
# decides whether the tarball survives at all. Each bounded section names its own
# ceiling; nothing sums them, and a reader checking one section reasonably
# concludes the report is bounded by that figure.
#
# Written as the COMPOSITION rather than as one figure, because the figure is what
# goes stale: every term below is a number this file sets, so an editor who changes
# one recomputes instead of trusting a total that silently stopped matching. A
# single figure here is easy to get wrong in the quiet direction: omit the kill
# grace and one class of overshoot and it reads seven minutes short, with nothing
# in the file disagreeing.
#
#   ONE READ            = COZYREPORT_READ_TIMEOUT + the `-k` grace = 30 + 5 = 35s.
#                         The grace counts: `timeout -k 5 30` sends TERM at 30s and
#                         KILLs at 35s, so a read that ignores TERM costs 35s, not
#                         30. hack/e2e-capture-previous-logs.sh states the same rule
#                         for its own pair and budgets 18 + 2 = 20 against it.
#   BUDGETS             = 600s (pods) + 3 x 240s (VM, VMI, services)      = 1320s
#   OVERSHOOT, pods     = a budget gates when a read may START, so the last pod
#                         admitted still runs 4 reads: pod.yaml, describe.txt, and
#                         both log reads of one container.        4 x 35 =  140s
#   OVERSHOOT, objects  = same rule, and the deadline is checked once per iteration
#                         with two reads after it, in each of three walks.
#                                                             3 x 2 x 35 =  210s
#   UNBUDGETED LISTINGS = pods.txt, vms.txt, vmis.txt, services.txt,
#                         applicationdefinitions.txt, tenants.txt. Each is bounded
#                         per read but sits outside every budget window -- the three
#                         object deadlines are set AFTER their own listing read.
#                                                                 6 x 35 =  210s
#   LINSTOR EXEC READS  = the three listings, the error-report index, the
#                         controller bundle, and ONE BUNDLE PER SATELLITE. Bounded
#                         per read, in no budget window, and the only reads here
#                         whose duration is a workload's rather than the
#                         apiserver's. Written with its n rather than with a
#                         cluster's worth folded in, because this is the term that
#                         grows with the storage nodes.
#                                                          (5 + n) x 35 = 175 + 35n
#   EVERY OTHER READ    = literally the rest of the file -- the listings, the
#                         gates, the sandbox-host commands, and every per-object walk
#                         EXCEPT the four capped ones above. Written as "the rest"
#                         so that a read added later is inside this term without
#                         anyone having to notice. Each
#                         read is bounded, none of those walks sits inside a cap or
#                         a budget, and they take two to four reads per selected
#                         object, so this term is the cluster's to set and not this
#                         file's. Named as "the ones outside the caps" rather than
#                         listed: a list of them is exactly what this file stopped
#                         keeping, because it drifts and reads as complete while it
#                         does so. A failed install with a hundred not-Ready
#                         HelmReleases takes four reads per release and spends
#                         400 x 35 = 14000s in that one walk.
#                                                          not a constant: see above
#                                                                          ------
#                                              2055s + 35n + whatever the walks cost
#
# No total is stated, and no partial sum either. Three attempts at a summarising
# figure here were each wrong on arrival -- the terms are arithmetic over five
# constants this file sets elsewhere, nothing binds a figure to them, and a
# sentence that adds some of them up is a fourth thing to keep in step. The
# relation that matters is stated once, below, where the step's own ceiling is:
# it sits UNDER this composition deliberately. Add the terms yourself if you need
# the number; do not expect to find it written here.
#
# TWO ceilings sit outside it, and they fail differently. The job is capped at
# `timeout-minutes: 180`, and reaching that cancels every step still to come --
# the artifact upload among them. The step running this script is capped at 30 of
# its own, and reaching THAT ends only this step: the uploads after it still run,
# and `continue-on-error` on that step keeps the overrun out of the job's result,
# so a passing run whose collection was slow is still reported as passing.
# Neither ceiling saves this report: the tarball is written on the last line of
# this file, so an overrun of either loses it rather than truncating it. What the
# step ceiling buys is the rest of the job.
#
# The step's 30 minutes is deliberately UNDER the sum above rather than over it.
# The sum is what a run costs when essentially every read hits its ceiling, and
# the measured collection takes well under a minute; sizing the step for the
# pathological case would spend a sixth of the job budget waiting for a report
# that is already worthless. Anyone raising a budget here should check this sum
# against that 30 minutes, not against the section they are editing.
#
# Default pod cap, shared with hack/cozyreport-summary.sh so the two start from
# the same number. Their resulting counts can still differ: the collector also
# stops on its time budget and says so in COLLECTION-TRUNCATED.txt.
COZYREPORT_PODS_DEFAULT=40

# The same two bounds for the object loops that read one YAML and one describe per
# selected object: VMs, VMIs and pending-address services. Constants rather than
# knobs, because nobody has asked to tune them and a setting added on spec cannot
# be withdrawn once somebody sets it.
#
# They exist for the same reason the pod bounds do, and became necessary for a
# sharper one: the services selector read a column that never carries the value it
# filtered for, so that loop had never executed. Correcting it is what makes a
# large selection possible, and the platform ships LoadBalancer services in
# quantity, so an install with no LB provider selects all of them at once. At two
# reads per object and a 30s ceiling each, an unbounded loop clears the step's own
# 30-minute ceiling long before it finishes; the tarball is written on this file's
# last line, so the overrun does not truncate it, it loses it.
COZYREPORT_OBJECTS_DEFAULT=25
COZYREPORT_OBJECTS_BUDGET_DEFAULT=240

# The wall-clock wrapper for the bounded reads, resolved once so a caller driving
# cozyreport_collect_pod directly gets the same answer. Empty when `timeout` is
# absent: the reads then run unbounded and the section says so, rather than every
# read exiting 127 and blaming the cluster for a missing local binary.
COZYREPORT_READ_TIMEOUT=30
if command -v timeout >/dev/null 2>&1; then
  COZYREPORT_BOUND="timeout -k 5 $COZYREPORT_READ_TIMEOUT"
else
  COZYREPORT_BOUND=""
fi

# cozyreport_cutoff_desc <exit-status>: how a 124/137 should be described, derived
# from the bound at the moment of writing rather than fixed alongside it.
#
# Two variables set in one if/else agree until something sets one of them on its
# own -- and the collector already keys on an empty COZYREPORT_BOUND, so "was there
# a bound" has to be asked of the bound. Otherwise a read with no ceiling reports a
# kill by runner teardown or the OOM killer as this script's own timeout, which is a
# mechanism that was not in play.
#
# The status matters for the same reason, one level down. 124 is the deadline
# expiring. It is not literally unique -- `timeout` passes a command's own exit
# status through, so a command that itself exits 124 is indistinguishable -- but
# every command handed to this wrapper is a kubectl `get`, `logs` or `describe`, a
# talosctl `dmesg` or `logs`, or one of the host commands the sandbox-host
# section reads, and none of those returns 124. Described rather than counted,
# because a number here is a second thing to keep in step with a list that
# grows. talosctl and the host commands are
# named because this file only started routing them through the bounded reader; a
# cobra CLI exits 0 or 1 and `df`/`free`/`ps` exit 0, 1 or 2, so the conclusion is
# unchanged -- but an enumeration that stops listing what reaches it stops being
# something a reader can check. The one construct that would break that is
# `kubectl exec`, which propagates the remote command's status. Six bounded reads
# ARE execs, so the invariant is narrower than "no bounded read is an exec": none
# of them is described by THIS helper. They go through cozyreport_read_exec, which
# states the ambiguity rather than resolving it and never calls in here, and a test
# holds both halves. 137 is 128+SIGKILL,
# which our own `-k` grace produces and so does anything else that SIGKILLs the
# read -- the OOM killer on a loaded runner, a teardown that signals the process
# group. Both belong in the cut-off branch, since either way the file ends before
# the object did; but naming our timeout for a 137 states a cause the script never
# observed, and a read killed at second two would be reported as one that ran the
# full 30.
cozyreport_cutoff_desc() {
  if [ -z "$COZYREPORT_BOUND" ]; then
    printf '%s' "a signal from outside this script, which ran its reads unbounded"
  elif [ "${1:-}" = "137" ]; then
    printf '%s' "a SIGKILL -- the kill grace of its own ${COZYREPORT_READ_TIMEOUT}s timeout, or something else killing the read, which 137 does not tell apart"
  else
    printf '%s' "its own ${COZYREPORT_READ_TIMEOUT}s timeout"
  fi
}

# cozyreport_admit_objects <section>: stdin = selected rows, stdout = at most
# COZYREPORT_OBJECTS_DEFAULT of them, with the overflow named rather than dropped.
#
# The count bound. The time bound is COZYREPORT_OBJECTS_DEADLINE, set beside the
# call and checked between objects by the loops themselves, so a slow apiserver
# stops the walk instead of the job's outer limit stopping the whole report.
cozyreport_admit_objects() {
  _cao_rows=$(cat)
  _cao_total=$(printf '%s\n' "$_cao_rows" | grep -c . || true)
  printf '%s\n' "$_cao_rows" | awk 'NF' | head -n "$COZYREPORT_OBJECTS_DEFAULT"
  if [ "$_cao_total" -gt "$COZYREPORT_OBJECTS_DEFAULT" ]; then
    # "admitted", not "collected": this runs BEFORE the loop reads anything, so
    # the number is how many the cap let through, not how many ended up in the
    # tree. The two differ whenever the time budget stops the walk early, and on
    # an already-expired budget they differ by all of them -- the note would
    # otherwise claim twenty-five directories that do not exist.
    printf '%s\n' "[cozyreport] $1: $_cao_total selected, $COZYREPORT_OBJECTS_DEFAULT admitted by the cap; the rest are listed in the section's own .txt above and were not read individually. Fewer may have been collected than admitted if the object budget also elapsed, which is recorded separately" \
      >> "$REPORT_DIR/kubernetes/COLLECTION-TRUNCATED.txt" || true
  fi
}

# cozyreport_objects_deadline_passed: true once the object walk has spent its
# budget. Checked between objects, never inside one, so every file present is a
# file that was read whole.
cozyreport_objects_deadline_passed() {
  [ -n "${COZYREPORT_OBJECTS_DEADLINE:-}" ] &&
    [ "$(date +%s)" -ge "$COZYREPORT_OBJECTS_DEADLINE" ]
}

# cozyreport_select_objects <section> <kubectl-args...>: the selector read, with
# its exit status kept.
#
# A pipeline in dash has no pipefail, so `timeout kubectl get ... | filter | while`
# returns the status of `while` and a read killed at 124 is indistinguishable from
# a cluster that genuinely has nothing to select: no rows, no error, no marker. The
# pod list is read this way for the same reason; these three sections were still
# piping the read straight in.
cozyreport_select_objects() {
  _cso_section=$1
  shift
  # Where this walk's notes belong. Defaults to the kubernetes directory, which is
  # right for the twelve callers that read a Kubernetes listing; the LINSTOR
  # satellite walk sets it, because its bundles land under linstor/ and a note
  # about a pod list that came back short is no use in a directory the reader has
  # no reason to open.
  _cso_dir=${COZYREPORT_SELECT_NOTE_DIR:-$REPORT_DIR/kubernetes}
  # shellcheck disable=SC2086  # empty COZYREPORT_BOUND must vanish, not become ""
  # kubectl's reason is kept, like every other read in this file. Discarding it
  # leaves "the selection read did not return (exit 1)", which is a symptom with
  # no cause: on an install without KubeVirt `kubectl get vm -A` exits 1, and the
  # report cannot then say whether the CRD is absent, RBAC refused, or the
  # apiserver never answered. This was the one reader added here that threw it away.
  _cso_err=$(mktemp "${TMPDIR:-/tmp}/cozyreport-select.XXXXXX" 2>/dev/null) || _cso_err=""
  # Status taken in an explicit `else`. A compound `if` whose condition fails and
  # which has no else leaves `$?` at 0, so reading it after `fi` reports a clean
  # exit for every failure -- the same trap this file documents two functions up.
  # It is easy to reintroduce here, because the `if` reads as though the status
  # survives it.
  # shellcheck disable=SC2086  # empty COZYREPORT_BOUND must vanish, not become ""
  if _cso_rows=$($COZYREPORT_BOUND "$@" 2>"${_cso_err:-/dev/null}"); then
    [ -z "$_cso_err" ] || rm -f "$_cso_err"
    # An empty selection emits NOTHING, not a blank line. `printf '%s\n' ""`
    # writes one, and a blank line is a row to every caller downstream: the
    # filters that select on a named column -- `$2 != "Ready"`, `$3 != "Bound"`
    # -- all pass it, because an absent field is not the string it is compared
    # against, and the loop then runs once with an empty name. That reads
    # `kubectl get node ""`, whose refusal lands in `nodes/node.yaml` and looks
    # exactly like a failed read of a real node.
    #
    # Guarded here rather than in each loop. The three callers that predate this
    # were immune only because their filters happen to start with `NF &&`, which
    # is a property of what they select on and not a decision anyone made about
    # empty selections; the next loop added would need to know that too.
    if [ -n "$_cso_rows" ]; then
      printf '%s\n' "$_cso_rows"
    fi
    return 0
  else
    _cso_rc=$?
  fi

  # Every note below is appended into $REPORT_DIR/kubernetes, and this function is
  # no longer called only from sections that run after that directory is created.
  # A redirect into a directory that does not exist fails, the `|| true` swallows
  # it, and the marker is lost -- in the one function whose entire purpose is that
  # a failed selection cannot pass for an empty cluster. Created here rather than
  # at each append, and only on the failure path, so a healthy run still leaves no
  # empty directory behind.
  mkdir -p "$_cso_dir" 2>/dev/null || true

  # Rows arrived before the failure: that is a partial list, and the rows are the
  # only ones the read named. Emitted, and the note says partial rather than
  # empty -- the same rule the pod walk and the summary already follow.
  if [ -n "$_cso_rows" ]; then
    printf '%s\n' "$_cso_rows"
    printf '%s\n' "[cozyreport] $_cso_section: the selection read did not return (exit $_cso_rc); the objects below are the ones it named before it stopped, and there may be others it never reached" \
      >> "$_cso_dir/COLLECTION-FAILED.txt" || true
    [ -z "$_cso_err" ] || rm -f "$_cso_err"
    return 0
  fi

  # A kind this cluster does not serve is not a failure to collect -- it is the
  # cluster, correctly described. `kubectl get vm -A` exits 1 on every install
  # without KubeVirt, and filing that under COLLECTION-FAILED.txt puts an alarming
  # filename in the tree of a report where nothing went wrong. The body did carry
  # kubectl's reason, so the note was never misleading once opened; the problem is
  # that the filename is what a triager scans for, and a file that cries wolf on
  # ordinary installs is one a reader learns to skip -- which costs the runs where
  # it names something real.
  #
  # Checked before the timeout branch, not after: this is decided by what kubectl
  # SAID, and the branches below are ordered by how the read ended. A read that
  # was cut off has no message at all, so the two cannot both match.
  #
  # CONSTRAINT ON CALLERS, not a general truth. `the server could not find the
  # requested resource` is also what an aggregated APIService that is DOWN
  # produces, and that is a failed read wearing the words of an absent one. Safe
  # at every call site today because each of them reads a core kind or a CRD --
  # `packages` and `packagesources` are real CRDs, installed from
  # internal/crdinstall/manifests/ -- and neither can be served by an aggregated
  # API. Stated as the PROPERTY every caller has to have rather than as a count of
  # the callers, because the count is what goes stale: it read "today's three call
  # sites" while there were thirteen, and a reader checking the constraint against
  # an audited set of three has no way to know the other ten were never audited.
  # Route an aggregated kind
  # through this selector -- `tenants.apps.cozystack.io` is one, and this file
  # already reads it, through cozyreport_read_object rather than here -- and an
  # apiserver outage on exactly the failed install this tool exists for gets filed
  # as "nothing to collect", with COLLECTION-FAILED.txt suppressed.
  if [ -n "$_cso_err" ] && [ -s "$_cso_err" ] &&
     grep -qE "doesn't have a resource type|the server could not find the requested resource|could not find the requested resource" "$_cso_err"; then
    # Its own filename, not COLLECTION-NOTES.txt. That name is already taken, one
    # directory down, by "a knob value the collector could not use" -- a different
    # statement with a different subject, and the doc bullet describes only that
    # one. Two meanings behind one name means whichever the reader has met before
    # is the one they assume, and the marker guard cannot catch the collision: it
    # matches basenames, so the doc's mention of the other file satisfies it.
    printf '%s\n' "[cozyreport] $_cso_section: this cluster does not serve that resource type, so there is nothing to collect. This is a statement about the cluster, not a failed read: $(tail -n 1 "$_cso_err" | cut -c1-200)" \
      >> "$_cso_dir/KIND-NOT-SERVED.txt" || true
    [ -z "$_cso_err" ] || rm -f "$_cso_err"
    return 0
  fi

  # Timeout first, matching every other classification site here: a read killed
  # before kubectl wrote anything has an empty stderr by construction, so leading
  # with "no message" would name the accident instead of the cause.
  if cozyreport_timed_out "$_cso_rc"; then
    _cso_why="the read was cut off by $(cozyreport_cutoff_desc "$_cso_rc")"
  elif [ -z "$_cso_err" ]; then
    _cso_why="kubectl exited $_cso_rc; its message could not be captured (no writable temp dir on the machine producing this report), so whether it said anything is unknown"
  elif [ -s "$_cso_err" ]; then
    _cso_why="kubectl said: $(tail -n 1 "$_cso_err" | cut -c1-200)"
  else
    _cso_why="kubectl exited $_cso_rc without a message"
  fi
  [ -z "$_cso_err" ] || rm -f "$_cso_err"

  # printf, not echo: kubectl's text is untrusted and dash's echo expands
  # backslash escapes, so a message carrying one could forge a line in a file a
  # triager reads as fact.
  printf '%s\n' "[cozyreport] $_cso_section: the selection read did not return (exit $_cso_rc), so this section is empty because nothing could be listed, NOT because nothing matched; $_cso_why" \
    >> "$_cso_dir/COLLECTION-FAILED.txt" || true
  return 0
}

# cozyreport_probe <section> <kubectl-args...>: the bounded form of the `if
# kubectl get ... >/dev/null` gates that decide whether a module runs at all.
#
# A gate is a read, and an unbounded one costs more than any read it guards: the
# module below it never starts. Bounded, its non-zero status is indistinguishable
# from "this cluster does not run that component", so the module is skipped and
# leaves the same empty tree.
#
# So the cut-off is recorded and the status is still returned: the caller keeps
# its `if`, and the report carries the difference between a component that is not
# installed and a probe that TIMED OUT. Only that one. A probe refused for another
# reason -- RBAC, an apiserver 503 -- still skips its module and still looks like
# a component that was never installed, because telling those apart means reading
# kubectl's message, and filing every non-zero probe would put a note in every
# report on every install that legitimately lacks a component. Named here rather
# than implied away: this function narrows the silence, it does not end it.
cozyreport_probe() {
  _cpr_section=$1
  shift
  # Status taken in an explicit `else`, like every other reader here. A compound
  # `if` whose condition fails and which has no else leaves `$?` at 0, so reading
  # it after `fi` reports a clean probe for every failure -- and a probe is the
  # one place where that turns straight into "this module is present", which then
  # runs the whole section against a cluster that never answered.
  # shellcheck disable=SC2086  # empty COZYREPORT_BOUND must vanish, not become ""
  if $COZYREPORT_BOUND "$@" >/dev/null 2>&1; then
    return 0
  else
    _cpr_rc=$?
  fi
  cozyreport_timed_out "$_cpr_rc" || return "$_cpr_rc"
  mkdir -p "$REPORT_DIR/kubernetes" 2>/dev/null || true
  printf '%s\n' "[cozyreport] $_cpr_section: the probe that decides whether this section is collected was cut off by $(cozyreport_cutoff_desc "$_cpr_rc"), so the section was skipped. That is NOT a statement that the component is absent -- nothing was ever looked at, and a cluster running it produces the same empty tree here" \
    >> "$REPORT_DIR/kubernetes/COLLECTION-FAILED.txt" || true
  return "$_cpr_rc"
}

# cozyreport_objects_budget_note <section>: record that the walk stopped on time
# rather than on count. Written once per section, because the loop breaks once.
#
# Without it the objects still in the selection simply vanish: a reader sees three
# service directories where the listing beside them names nine, with nothing
# saying which bound removed the other six -- or, worse, sees the cap's note and
# reads every omission as the cap when the clock is what stopped the walk.
cozyreport_objects_budget_note() {
  printf '%s\n' "[cozyreport] $1: the ${COZYREPORT_OBJECTS_BUDGET_DEFAULT}s object budget elapsed before the walk finished; the objects not collected here are listed in the section's own .txt above and were not read individually" \
    >> "$REPORT_DIR/kubernetes/COLLECTION-TRUNCATED.txt" || true
}

# cozyreport_collect_talos_node <talosconfig> <node> <output-dir>
#
# The e2e talosconfig intentionally carries no endpoints, so both -e and -n are
# required. Each node's InternalIP is itself a Talos API endpoint. `dmesg
# --tail` is a boolean follow-mode flag (unlike `logs --tail`, which is an
# integer), so request the complete kernel ring buffer and retain bounded tails
# only for kubelet/containerd service logs.
cozyreport_collect_talos_node() {
  _crt_config=$1
  _crt_node=$2
  _crt_dir=$3

  # Bounded like every other read here, and for a sharper reason: these do not
  # go to the apiserver at all. A node whose Talos API is up but wedged, or one
  # answering slowly enough that three reads each outlast the step, is the same
  # lost tarball with none of the kubectl-side bounds in the path.
  cozyreport_read_object "$_crt_dir/talos-$_crt_node-dmesg.txt" \
    talosctl --talosconfig "$_crt_config" -e "$_crt_node" -n "$_crt_node" dmesg
  cozyreport_read_object "$_crt_dir/talos-$_crt_node-kubelet.log" \
    talosctl --talosconfig "$_crt_config" -e "$_crt_node" -n "$_crt_node" \
    logs kubelet --tail=500
  cozyreport_read_object "$_crt_dir/talos-$_crt_node-containerd.log" \
    talosctl --talosconfig "$_crt_config" -e "$_crt_node" -n "$_crt_node" \
    logs containerd --tail=500
}

# cozyreport_pods_not_ready: stdin = `kubectl get pod -A --no-headers` rows
# (NAMESPACE NAME READY STATUS RESTARTS AGE), stdout = the subset worth collecting.
#
# Compares the two halves of the READY column rather than filtering on STATUS: a
# pod whose container runs while its readiness probe never passes reports
# STATUS=Running READY=0/1, and a STATUS-only filter drops exactly that. Terminal
# phases are excluded first, since Succeeded/Completed legitimately end at 0/1.
#
# READY counts ready CONTAINERS, which is not the pod's Ready condition: a pod
# held back by an unsatisfied `spec.readinessGates` prints 1/1 while its Ready
# condition is False, and this drops it. Reading the condition instead means a
# structured read per pod where this is one cluster-wide list, on the report of a
# cluster that is already failing -- so the column stays, and the gap is pinned by
# a test rather than left for a reader to discover from an empty directory.
cozyreport_pods_not_ready() {
  awk '
    !NF                            { next }
    $4 ~ /^(Succeeded|Completed)$/ { next }
    $4 != "Running"                { print; next }
    { if (split($3, ready, "/") == 2 && ready[1] != ready[2]) print }
  '
}

# cozyreport_kubevirt_not_ready: stdin = `kubectl get vm -A --no-headers` or
# `kubectl get vmi -A --no-headers` rows, stdout = the ones worth collecting.
#
# READY is the LAST column in both layouts, and its INDEX differs between the two
# kinds, so it is read as $NF rather than by number:
#   vmi   NAMESPACE NAME AGE PHASE IP NODENAME READY
#   vm    NAMESPACE NAME AGE STATUS READY
# One helper serves both, and $NF is what lets it, without either index appearing
# anywhere.
#
# Anything whose last field is not literally "True" is selected, so a missing
# READY column over-collects rather than skipping. `NF` drops the blank line an
# empty selection arrives as.
cozyreport_kubevirt_not_ready() {
  awk 'NF && $NF != "True"'
}

# cozyreport_svc_pending: stdin = `kubectl get svc -A --no-headers` rows, stdout =
# the LoadBalancers still waiting for an address.
#
#   NAMESPACE NAME TYPE CLUSTER-IP EXTERNAL-IP PORT(S) AGE
#
# EXTERNAL-IP is $5, and it is the only column that ever prints `<pending>`. The
# selection read $4 -- CLUSTER-IP, which holds an address, `None`, or `<none>` and
# never `<pending>` -- so it matched nothing on any cluster and the section has
# been empty since it was written, which reads as "no LoadBalancer is stuck". Not
# $NF and not an unanchored match: unlike the vm/vmi layouts this one has a fixed
# column count, and a service NAMED `<pending>` is not a row about a pending
# address.
cozyreport_svc_pending() {
  awk 'NF && $5 == "<pending>"'
}

# cozyreport_pods_prioritize: stdin = selected rows, stdout = the same rows with
# the COZY_REPORT_PREFER_NS namespaces first, each group keeping its input order.
# `kubectl get pod -A` emits namespace-alphabetical rows, so without this the cap
# is spent on cozy-* before the tenant pod the run died on. Empty prefix is a
# passthrough.
#
# awk with index() rather than grep: the prefix is user-supplied and would
# otherwise be a basic regular expression. Field-scoped, not line-anchored.
#
# Narrowed, not closed: `awk -v` expands backslash escapes in the value it assigns,
# so a prefix containing `\t` arrives as a real tab. COZY_REPORT_PREFER_NS holds a
# namespace prefix and namespaces are DNS labels, so it cannot bite -- said here
# because the sentence above otherwise reads as a guarantee. Same note sits on the
# sibling helper in hack/e2e-capture-previous-logs.sh.
cozyreport_pods_prioritize() {
  _cpp_ns=${COZY_REPORT_PREFER_NS-tenant-}
  # Explicit rather than relying on the awk below to fall through, which it does
  # only because `index(s, "")` returns 0 rather than 1. That is the behaviour of
  # every awk this runs on today and it is not something the documented disable
  # switch should rest on. Deleting this branch does not change any test, which is
  # the point: the contract is the passthrough, not the arithmetic underneath it.
  _cpp_rows=$(cat)
  # The disable switch turns off the NAMESPACE preference only. The evidence-class
  # ordering below is not a preference and is not optional: it is what keeps the
  # count cap from dropping a pod that the previous, uncapped collector always
  # kept. Letting `COZY_REPORT_PREFER_NS=` collapse both would mean one documented
  # env value silently removes a guarantee about which evidence reaches the
  # tarball, which is not what "disables the ordering" leads anyone to expect.
  if [ -z "$_cpp_ns" ]; then
    printf '%s\n' "$_cpp_rows" | awk 'NF && $4 != "Running"' || true
    printf '%s\n' "$_cpp_rows" | awk 'NF && $4 == "Running"' || true
    return 0
  fi
  # Emit four times: stdin is consumable only once and every pass needs the same
  # rows, which is why they are buffered above.
  #
  # FOUR groups, and the outer split is by EVIDENCE CLASS rather than by namespace.
  # Two selections exist here. The narrow one -- not `Running` at all: crash loops,
  # `Pending`, `Error` -- is collected unconditionally by design. The wide one adds
  # pods that are `Running` with an unready container, which a readiness probe that
  # never passes produces and which are easy to have many of. Since the walk is
  # capped by count, ordering the narrow class ahead of the wide one is what stops a
  # cluster full of unready tenant pods from filling the cap and leaving a crash
  # loop in a platform namespace with no describe and no container logs.
  #
  # Ranking by namespace first and evidence class second does NOT achieve that:
  # a cap's worth of Running-unready pods inside the preferred namespace still
  # pushes every crash loop outside it past the cap.
  #
  # What this guarantees, stated exactly: the WIDE class can never displace the
  # narrow one. It does not promise the narrow class always fits -- more crash
  # loops than COZY_REPORT_PODS_MAX still lose the excess, and that is the declared
  # cap doing its job, named in COLLECTION-TRUNCATED.txt.
  #
  # The cost is that COZY_REPORT_PREFER_NS orders WITHIN each class instead of
  # across the whole selection, so a platform crash loop precedes a tenant pod that
  # is merely unready. That is the right way round: the namespace preference is a
  # heuristic about where the interesting pod usually lives, while collecting the
  # pod that is crashing is the thing this section exists to do.
  #
  # Terminal phases never reach here -- cozyreport_pods_not_ready drops
  # Succeeded/Completed -- so `$4 != "Running"` IS the narrow class, exactly.
  printf '%s\n' "$_cpp_rows" | awk -v p="$_cpp_ns" 'NF && $4 != "Running" && index($1, p) == 1' || true
  printf '%s\n' "$_cpp_rows" | awk -v p="$_cpp_ns" 'NF && $4 != "Running" && index($1, p) != 1' || true
  printf '%s\n' "$_cpp_rows" | awk -v p="$_cpp_ns" 'NF && $4 == "Running" && index($1, p) == 1' || true
  printf '%s\n' "$_cpp_rows" | awk -v p="$_cpp_ns" 'NF && $4 == "Running" && index($1, p) != 1' || true
}

# cozyreport_timed_out <exit-status>: true when that status means "our own read
# timeout fired", false otherwise.
#
# Two statuses, not one. `timeout` reports 124 when the command is still running
# when the clock runs out, but with `-k` a command that ignores SIGTERM is killed
# afterwards and the status becomes 137. Checking only 124 puts the more stubborn
# of the two cases -- the one that had to be killed -- into the branch that
# describes a clean exit, so a partial log would pass as whole and an empty one
# would be blamed on the container.
cozyreport_timed_out() {
  case "$1" in
    124 | 137) return 0 ;;
    *) return 1 ;;
  esac
}

# cozyreport_append_note <file> <text>
#
# Append a `[cozyreport]` line, starting a new line first when the file does not
# already end in one. A read killed mid-stream leaves a partial final line, so
# appending blind would corrupt the decisive last line and hide the marker from a
# start-of-line grep.
cozyreport_append_note() {
  _can_file=$1
  _can_text=$2
  if [ -s "$_can_file" ] && [ -n "$(tail -c 1 "$_can_file" 2>/dev/null)" ]; then
    printf '\n' >> "$_can_file" || true
  fi
  printf '%s\n' "$_can_text" >> "$_can_file" || true
}

# cozyreport_read_object <output-file> <kubectl-args...>
#
# One bounded read of a whole object, with a marker appended when the read was cut
# off rather than finished. kubectl's refusal is the finding and belongs next to
# the evidence it replaces -- but only when it IS the evidence: `pod.yaml`,
# `vm.yaml` and `vmi.yaml` each hold one YAML document and nothing else, and a
# `Warning:` line on a read that otherwise succeeded turns a whole object into a
# file no parser will load. So stderr
# goes to a scratch file and is copied in only when kubectl wrote no object,
# which is the same rule cozyreport_read_container_log and summary_read follow.
#
# For the same reason the markers below are `#`-prefixed, unlike the ones in the
# log files: a bare marker line after a mapping is a second top-level node, so
# appending it to an object that kubectl finished writing before failing turns a
# parseable file into an unparseable one -- the exact defect diverting stderr was
# meant to remove, reintroduced by the fix for it. As a YAML comment the marker is
# still there for a person and invisible to the parser. describe.txt is not YAML
# and gets the same prefix, because one reader, one rule.
#
# One exception, and it is the format's rather than the reader's. This function
# now also writes the controller LOGS, which it never did before, and a log is
# neither YAML nor prose: the platform's controllers log JSON per line, `#` is not
# a comment in that, and "the object ended" is not a sentence about a log. So the
# marker for a `.log` target drops the prefix and speaks about output rather than
# about an object -- which is exactly the marker cozyreport_read_container_log
# already writes into a partial log. That reader appends into the log too, so the
# note stays IN the file rather than moving beside it: two readers in one tarball
# answering "where does a truncation marker for a log go" two different ways is
# worse than either answer.
cozyreport_read_object() {
  _cro_file=$1
  shift
  # Chosen from the target, once, so the branches below cannot disagree.
  #
  # _cro_prose says whether kubectl's own line may be written INTO this file. Only
  # YAML says no, and for the reason the block above gives: a bare `Warning:` or
  # `No resources found` line is a second top-level node and costs a parser the
  # whole document. Everything else here is read by a person.
  #
  # It matters because `kubectl get` writes `No resources found` to STDERR and
  # exits 0 (kubernetes/kubectl#1667): sent beside the file, an empty listing is
  # zero bytes and a READ-WARNINGS.txt lands on every healthy run.
  case "$_cro_file" in
    *.yaml) _cro_mark="# " ; _cro_ends="the object ended" ; _cro_prose=0 ;;
    *.log)  _cro_mark=""   ; _cro_ends="the output ended" ; _cro_prose=1 ;;
    *)      _cro_mark="# " ; _cro_ends="the object ended" ; _cro_prose=1 ;;
  esac
  # And the TOOL, from the argv rather than from what this reader used to serve.
  # It read only kubectl once; it now also runs talosctl and the four host
  # commands, and a marker saying "kubectl exited 1" on a `df` that hit a stale
  # mount is a false statement in an artifact whose whole premise is that its
  # notes can be believed. Same for the noun: `df.txt` did not stop because an
  # object ended.
  _cro_tool=$(basename "$1" 2>/dev/null) || _cro_tool=$1
  case "$_cro_file" in
    *.yaml) ;;
    *) [ "$_cro_tool" = "kubectl" ] || _cro_ends="the output ended" ;;
  esac
  _cro_err=$(mktemp "${TMPDIR:-/tmp}/cozyreport-obj.XXXXXX" 2>/dev/null) || _cro_err=""
  # Status taken in an explicit `else`, not after the `if`: a compound `if` whose
  # condition fails and which has no else leaves `$?` at 0. `|| true` on the read
  # itself would discard it entirely, which is what it used to do.
  # shellcheck disable=SC2086  # empty COZYREPORT_BOUND must vanish, not become ""
  if $COZYREPORT_BOUND "$@" > "$_cro_file" 2>"${_cro_err:-/dev/null}"; then
    _cro_rc=0
  else
    _cro_rc=$?
  fi
  # Recorded before anything else is written into the file, for the same reason
  # the log reader does it: the refusal is copied into that same file below, after
  # which `[ -s ]` answers "did kubectl say anything at all" while still reading
  # as "did kubectl return an object".
  _cro_had_stdout=0
  [ -s "$_cro_file" ] && _cro_had_stdout=1
  _cro_reason=""
  if [ -n "$_cro_err" ] && [ -s "$_cro_err" ]; then
    _cro_reason=$(tail -n 1 "$_cro_err" | cut -c1-200)
    if [ "$_cro_had_stdout" -eq 1 ]; then
      # kubectl wrote the object AND said something -- a deprecation notice, a
      # partial-result warning. Keeping it out of the file is the point of this
      # function; dropping it from the report is not. `2>&1` would put it in the
      # object, which costs the parser; leaving it nowhere costs the evidence, and
      # this file's whole claim is that nothing is lost in silence.
      #
      # Copied WHOLE, not reduced to its last line. `tail -n 1` is right where a
      # failing read buries its cause under klog retry preambles and one line has
      # to be chosen; a successful read has no preamble, so every line is a
      # warning in its own right and picking one drops the others. Two warnings
      # on one read is the ordinary case, not the exotic one.
      {
        printf '%s\n' "[cozyreport] $(basename "$_cro_file"): $_cro_tool wrote this and also said:"
        cat "$_cro_err"
      } >> "$(dirname "$_cro_file")/READ-WARNINGS.txt" || true
    elif [ "$_cro_rc" -ne 0 ]; then
      cat "$_cro_err" > "$_cro_file" 2>/dev/null || true
    elif [ "$_cro_prose" -eq 0 ]; then
      # Exited 0, wrote no object, and still said something, into a file a parser
      # reads. Copying it in would put an unprefixed kubectl line where an object
      # belongs -- a `Warning:` no parser can take and no marker explains, since
      # the emptiness check below sees a non-empty file and stays quiet. Beside it,
      # like every other warning on a successful read.
      {
        printf '%s\n' "[cozyreport] $(basename "$_cro_file"): $_cro_tool returned no object, exited 0, and said:"
        cat "$_cro_err"
      } >> "$(dirname "$_cro_file")/READ-WARNINGS.txt" || true
    else
      # The same case into a file a person reads, and here the message IS the
      # file. `No resources found` is what an empty listing has to say for itself;
      # sent beside it, the listing is zero bytes and says nothing at all -- and
      # that is the ordinary outcome on any install without an ACME issuer, so the
      # warnings file would then arrive on every healthy run. Prefixed, because
      # this is the script speaking about the read and not kubectl's own output.
      {
        printf '%s' "${_cro_mark}[cozyreport] $_cro_tool returned no rows and exited 0, saying: "
        cat "$_cro_err"
      } > "$_cro_file" || true
    fi
  fi
  [ -z "$_cro_err" ] || rm -f "$_cro_err"

  if cozyreport_timed_out "$_cro_rc"; then
    cozyreport_append_note "$_cro_file" \
      "${_cro_mark}[cozyreport] TRUNCATED: this file ends here because the read was cut off by $(cozyreport_cutoff_desc "$_cro_rc"), not because $_cro_ends -- anything absent below may exist on the cluster and simply was not read"
  elif [ "$_cro_rc" -ne 0 ] && [ "$_cro_had_stdout" -eq 1 ]; then
    # Failed part way through. Without this the file is a valid-looking prefix of
    # an object and nothing says the rest was never read -- the ambiguity the
    # TRUNCATED marker exists to remove, arrived at by kubectl's own exit rather
    # than by our timeout. The reason is not left in the file body, because the
    # body is what yq parses.
    cozyreport_append_note "$_cro_file" \
      "${_cro_mark}[cozyreport] TRUNCATED: this file ends here because $_cro_tool exited $_cro_rc part way through, not because $_cro_ends${_cro_reason:+: $_cro_reason}"
  elif [ "$_cro_rc" -ne 0 ] && [ ! -s "$_cro_file" ]; then
    # Failed with neither an object nor a message. Two ways to get here: a signal
    # that is not 124 or 137 -- SIGTERM from a teardown, say -- and a `mktemp`
    # that failed, which sent stderr to /dev/null. Without this branch the file is
    # zero bytes and says nothing, in a directory whose emptiness the doc reads as
    # a statement about the pod. Every other classification site in these scripts
    # has this branch; this was the one that did not.
    if [ -z "$_cro_err" ]; then
      cozyreport_append_note "$_cro_file" \
        "${_cro_mark}[cozyreport] $_cro_tool exited $_cro_rc and this file is empty; its message could not be captured (no writable temp dir on the machine producing this report), so whether it said anything is unknown"
    else
      cozyreport_append_note "$_cro_file" \
        "${_cro_mark}[cozyreport] $_cro_tool exited $_cro_rc without a message and wrote nothing"
    fi
  fi
  return 0
}

# cozyreport_read_exec <output-file> <kubectl-exec-args...>
#
# One bounded read whose command runs INSIDE a pod. Everything above reads the
# apiserver; these read a workload, so their duration is set by whatever is
# happening in the container rather than by how fast the apiserver answers, and a
# wedged pod holds the stream open with nothing on either end to close it.
#
# Kept apart from cozyreport_read_object for one reason, and it is the reason the
# comment on cozyreport_cutoff_desc already gives: `kubectl exec` passes the
# REMOTE command's exit status through. A 124 here is either this script's
# deadline expiring or a command that itself exited 124, and the status does not
# tell them apart -- so describing it as "its own 30s timeout", which is what
# that helper does and is correct for every `get`, `logs` and `describe`, would
# state a mechanism about the machine writing the report for something that may
# have happened inside a pod. This path states the ambiguity instead of resolving
# it, and never reaches that helper; a test holds both halves.
#
# The note goes BESIDE the output, never into it, and kubectl's own message goes
# with it. Two of the calls here stream a gzipped tarball, and a line of prose
# appended to one is an archive no reader can open -- one rule for every exec is
# worth more than a rule that changes with the output format.
#
# A cut-off bundle is EITHER kept and truncated (the call site's check asks for a
# first member, which a truncated gzip still lists) OR removed as zero bytes when
# the stream never started. This path runs before that check, so it names both.
#
# The four TEXT reads keep the remote command's message in the .txt itself, whole.
# `tail -n 1` is right where a failing read buries its cause under klog preambles;
# on an exec it is backwards, because `kubectl exec` appends `command terminated
# with exit code N` last.
cozyreport_read_exec() {
  _cre_file=$1
  shift
  # Before the redirect below, not after it. The same guard the selector and the
  # probe carry, and it protects the OUTPUT as well as the note -- placed under the
  # read it is meant to precede, it could not fire for either. Both call sites
  # create the directory today, so this is defence; defence that runs too late is
  # not defence.
  mkdir -p "$(dirname "$_cre_file")" 2>/dev/null || true
  _cre_err=$(mktemp "${TMPDIR:-/tmp}/cozyreport-exec.XXXXXX" 2>/dev/null) || _cre_err=""
  # Status taken in an explicit `else`, for the reason the readers above spell
  # out: a compound `if` whose condition fails and which has no else leaves `$?`
  # at 0, so reading it after `fi` reports a clean exit for every failure.
  # shellcheck disable=SC2086  # empty COZYREPORT_BOUND must vanish, not become ""
  if $COZYREPORT_BOUND "$@" > "$_cre_file" 2>"${_cre_err:-/dev/null}"; then
    _cre_rc=0
  else
    _cre_rc=$?
  fi
  _cre_said=0
  [ -n "$_cre_err" ] && [ -s "$_cre_err" ] && _cre_said=1
  _cre_had_stdout=0
  [ -s "$_cre_file" ] && _cre_had_stdout=1
  # Whether this output can carry the message at all. Only the two archive
  # streams cannot; the rest are tables a person reads, and a zero-byte one is
  # the "did it return nothing, or never run" question this collector exists to
  # answer.
  case "$_cre_file" in
    *.tgz) _cre_prose=0 ;;
    *)     _cre_prose=1 ;;
  esac
  if [ "$_cre_prose" -eq 1 ] && [ "$_cre_had_stdout" -eq 0 ] && [ "$_cre_rc" -ne 0 ]; then
    # A table that got nothing must not ship as zero bytes: `linstor/nodes.txt` at
    # zero length reads as a cluster with no nodes, which is the one reading this
    # collector exists to prevent. Three ways to get here and each says which.
    if [ "$_cre_said" -eq 1 ]; then
      # The refusal IS the finding, and it belongs where the table would have been.
      cat "$_cre_err" > "$_cre_file" 2>/dev/null || true
    elif cozyreport_timed_out "$_cre_rc"; then
      # The wedged pod of the ticket: killed before the command wrote a byte, so
      # there is no stderr either. Without this the sharpest failure this reader
      # was written for is the one that leaves nothing behind.
      printf '%s\n' "# [cozyreport] the read was killed at exit $_cre_rc before the command inside the pod wrote anything, so this file is empty for that reason and not because the command had nothing to say; COLLECTION-FAILED.txt beside it says what that status does and does not establish" \
        > "$_cre_file" || true
    elif [ -z "$_cre_err" ]; then
      # No writable temp dir, so stderr went to /dev/null. "Said nothing" and "we
      # could not hear it" are different facts and only the first is about the
      # cluster -- the same branch the other four readers here carry.
      printf '%s\n' "# [cozyreport] the read exited $_cre_rc and this file is empty; its message could not be captured (no writable temp dir on the machine producing this report), so whether it said anything is unknown" \
        > "$_cre_file" || true
    else
      printf '%s\n' "# [cozyreport] the read exited $_cre_rc without a message and wrote nothing" \
        > "$_cre_file" || true
    fi
  fi
  # printf, not echo: the remote command's text is untrusted and dash's echo
  # expands backslash escapes, so a message carrying one could forge a line in a
  # file a triager reads as fact.
  if cozyreport_timed_out "$_cre_rc"; then
    # The four tables get the marker IN the file as well, like every other .txt in
    # the tarball. A `nodes.txt` cut at the ceiling is a short node list that reads
    # as a small cluster, and "the note is one directory up" is exactly the walk a
    # triager does not make. Only the two archives cannot take it -- a line of
    # prose inside a gzip stream is not a file anyone can open -- which is what
    # _cre_prose already decides for the two branches above.
    if [ "$_cre_prose" -eq 1 ] && [ "$_cre_had_stdout" -eq 1 ]; then
      cozyreport_append_note "$_cre_file" \
        "# [cozyreport] TRUNCATED: this output ends here because the read was killed at exit $_cre_rc, not because the command finished -- see COLLECTION-FAILED.txt beside this file for what that status does and does not establish"
    fi
    {
      printf '%s\n' "[cozyreport] $(basename "$_cre_file"): this read runs a command inside a pod and ended at exit $_cre_rc, so whatever it produced stops where the stream stopped and not where the command's output did. What $_cre_rc means here is genuinely open: kubectl exec passes the remote command's own status through, so it is either the ${COZYREPORT_READ_TIMEOUT}s bound on this read firing or the command itself exiting $_cre_rc, and nothing in the status separates the two."
      # Only for the two reads whose output is an archive. Printing it beside a
      # cut-off .txt would describe a mechanism that read never went through,
      # which is the same fault as naming a timeout that did not fire.
      if [ "$_cre_prose" -eq 0 ]; then
        printf '%s\n' "The file this note names is EITHER beside it and truncated -- it lists and extracts the members that crossed before the cut -- OR gone, because a read killed before the remote command wrote its first byte leaves zero bytes and the emptiness check at the call site removes it. This note cannot tell you which; the directory can."
      fi
      if [ "$_cre_said" -eq 1 ]; then
        printf '%s\n' "It also said:"
        cat "$_cre_err"
      fi
    } >> "$(dirname "$_cre_file")/COLLECTION-FAILED.txt" || true
  elif [ "$_cre_rc" -ne 0 ]; then
    # `# ` like every other in-file marker on a .txt: the object reader has given
    # this prefix to every non-YAML target since before this reader existed, and
    # two readers marking the same file type two ways means a triager grepping
    # `^# [cozyreport]` across the tarball gets half of it and cannot tell.
    # A partial table that died on the command's own terms, not on the clock,
    # gets the same in-file marker: a `linstor n l` that streams three rows and
    # exits 1 is a three-node listing to anyone who opens it. The timeout branch
    # above marks its own case and the object reader marks this one; leaving it
    # out here had the two readers disagree about the same shape.
    if [ "$_cre_prose" -eq 1 ] && [ "$_cre_had_stdout" -eq 1 ]; then
      cozyreport_append_note "$_cre_file" \
        "# [cozyreport] TRUNCATED: this output ends here because the read exited $_cre_rc part way through, not because the command finished"
    fi
    {
      printf '%s\n' "[cozyreport] $(basename "$_cre_file"): the read exited $_cre_rc, so its output holds only what was streamed before that."
      if [ "$_cre_said" -eq 1 ]; then
        # WHOLE, not the last line. `kubectl exec` appends `command terminated
        # with exit code N` after whatever the remote command said, so the last
        # line here is the one line that never carries the cause.
        printf '%s\n' "It said:"
        cat "$_cre_err"
      fi
    } >> "$(dirname "$_cre_file")/COLLECTION-FAILED.txt" || true
  elif [ "$_cre_said" -eq 1 ]; then
    # Succeeded and still wrote to stderr. Beside the output rather than merged
    # into it, the same rule the object reader follows: one of these outputs is a
    # tarball and the others are tables a person reads, and a stray line is noise
    # in the first case and a corrupt file in the second.
    {
      printf '%s\n' "[cozyreport] $(basename "$_cre_file"): the read succeeded and also said:"
      cat "$_cre_err"
    } >> "$(dirname "$_cre_file")/READ-WARNINGS.txt" || true
  fi
  [ -z "$_cre_err" ] || rm -f "$_cre_err"
  return 0
}

# cozyreport_read_container_log <output-file> <instance> <ns> <pod> <container> [args...]
#
# One bounded container log read. Three outcomes, distinguished because they mean
# different things: our timeout fired, kubectl failed part way through on its own
# terms, or the read completed. The exit status is kept rather than swallowed --
# a killed read, a refusal, and a container that genuinely logged nothing all leave
# a file that looks the same from outside, and only one is about the cluster.
#
# A partial read is kept and marked, never discarded.
cozyreport_read_container_log() {
  _crl_file=$1
  _crl_instance=$2
  _crl_ns=$3
  _crl_pod=$4
  _crl_c=$5
  shift 5

  _crl_tail=$(cozyreport_pod_tail)
  # -k: plain `timeout` sends TERM and waits forever on a process that ignores it.
  # `-c NAME` is the documented spelling; the bare second positional also works but
  # is undocumented.
  #
  # stderr goes to a scratch file, not `2>&1`: merged, "wrote part of a log then
  # failed" and "only refused" are byte-indistinguishable, and the three-way
  # outcome below depends on telling them apart.
  _crl_err=$(mktemp "${TMPDIR:-/tmp}/cozyreport-log.XXXXXX" 2>/dev/null) || _crl_err=""
  # shellcheck disable=SC2086  # empty COZYREPORT_BOUND must vanish, not become ""
  if $COZYREPORT_BOUND kubectl logs -n "$_crl_ns" "$_crl_pod" -c "$_crl_c" \
      --tail="$_crl_tail" "$@" > "$_crl_file" 2>"${_crl_err:-/dev/null}"; then
    _crl_rc=0
  else
    _crl_rc=$?
  fi
  # Whether kubectl produced any LOG is recorded here, before anything is written
  # into the file, and every later branch keys off this flag rather than off the
  # file's size. Asking `[ -s "$_crl_file" ]` afterwards answers a different
  # question than it appears to: the refusal is copied into that same file below,
  # so the check silently becomes "did kubectl say anything at all" and every
  # refusal is then classified as a partial log. That is not hypothetical -- it
  # made the commonest file in the tree, the `--previous` read of a container that
  # never restarted, carry a fabricated "cut off part way through" line.
  _crl_had_stdout=0
  [ -s "$_crl_file" ] && _crl_had_stdout=1
  # The refusal belongs in the log file, as before: it is the finding when there is
  # no log, and it is the reason when the log stops early.
  _crl_reason=""
  # Recorded before the scratch file is removed below: the classification branches
  # still need to know whether kubectl spoke, and by then there is nothing to ask.
  # Testing `-s "$_crl_err"` after the `rm` silently answers "no" for every read.
  _crl_said=""
  if [ -n "$_crl_err" ] && [ -s "$_crl_err" ]; then _crl_said=1; fi
  if [ -n "$_crl_err" ] && [ -s "$_crl_err" ]; then
    _crl_reason=$(tail -n 1 "$_crl_err" | cut -c1-200)
    if [ "$_crl_had_stdout" -eq 0 ] && [ "$_crl_rc" -ne 0 ]; then
      # A REFUSAL with no log: the message IS the finding, so it goes in the file.
      # Gated on the status as well as on the emptiness, because a read that exited
      # 0 with no output and a warning is not a refusal -- copying that in writes an
      # unprefixed kubectl line into a file read as container output, and the
      # placeholder below is then suppressed by its own `[ ! -s ]` guard, so nothing
      # says where the line came from.
      cat "$_crl_err" > "$_crl_file" 2>/dev/null || true
    else
      # kubectl wrote log lines AND said something. It does not belong in the log,
      # which is machine-read, but it does belong in the report: dropping it is a
      # silent loss in a tree whose claim is that there are none. Copied whole for
      # the same reason as the object reader above: nothing here needs picking one
      # line out of a preamble, so picking one only loses the rest.
      #
      # Whatever the exit status, not only on a clean one. A PARTIAL read is where
      # kubectl has the most to say -- the reset, the refusal that stopped it mid
      # stream -- and gating this on `rc == 0` kept only `tail -n 1` of it in the
      # marker line and dropped every other line on the floor. That is the one case
      # this file is written against, losing evidence exactly where there is most
      # of it, and it is what the sibling capture in e2e-capture-previous-logs.sh
      # already does correctly: it copies stderr whole regardless of how the read
      # ended. Two collectors in one tarball answering the same question two ways
      # is worse than either answer.
      # THREE cases, not two. A clean read that returned nothing is not "returned
      # this log": the placeholder written into the log file for that same read says
      # kubectl wrote no output, and the two files would then contradict each other
      # about one read -- with this one read first, because it is the one that names
      # the file. Both siblings already spell the third case out.
      {
        if cozyreport_timed_out "$_crl_rc"; then
          # The cutoff, not the raw status. The TRUNCATED marker inside the log
          # names this read's own timeout; rendering the same read as "kubectl
          # exited 124" here would have the two files disagree about what happened,
          # and 124 is this script's clock rather than anything kubectl decided.
          printf '%s\n' "[cozyreport] $(basename "$_crl_file"): the read was cut off by $(cozyreport_cutoff_desc "$_crl_rc") part way through this log, and kubectl also said:"
        elif [ "$_crl_rc" -ne 0 ]; then
          printf '%s\n' "[cozyreport] $(basename "$_crl_file"): kubectl exited $_crl_rc part way through this log and said:"
        elif [ "$_crl_had_stdout" -eq 0 ]; then
          printf '%s\n' "[cozyreport] $(basename "$_crl_file"): kubectl returned no log, exited 0, and said:"
        else
          printf '%s\n' "[cozyreport] $(basename "$_crl_file"): kubectl returned this log and also said:"
        fi
        cat "$_crl_err"
      } >> "$(dirname "$_crl_file")/READ-WARNINGS.txt" || true
    fi
  fi
  [ -z "$_crl_err" ] || rm -f "$_crl_err"

  # printf, not echo: kubectl's message is untrusted text and dash's echo expands
  # backslash escapes, so a message carrying one could forge a line in a file whose
  # whole job is to be believed.
  #
  # Keyed on the flag, not on the file: by this point the file may hold kubectl's
  # refusal, copied in above, and testing its size here would answer "did kubectl
  # say anything" while reading as "was there a log".
  if [ "$_crl_had_stdout" -eq 0 ]; then
    if cozyreport_timed_out "$_crl_rc"; then
      cozyreport_append_note "$_crl_file" \
        "[cozyreport] the read of the $_crl_instance instance of $_crl_c was cut off by $(cozyreport_cutoff_desc "$_crl_rc") before kubectl wrote anything: this says the apiserver did not answer in time, and says nothing about whether the container logged"
    elif [ "$_crl_rc" -ne 0 ]; then
      # Only when stderr was empty too. When it was not, the refusal is already in
      # the file and IS the finding; overwriting it with "without a message" would
      # destroy the one thing worth reading.
      #
      # "Said nothing" and "we could not capture what it said" are different facts,
      # and only the first is about the cluster. With no writable temp dir the
      # stderr file was never created, so claiming silence turns a local filesystem
      # condition into a statement about kubectl.
      #
      # cozyreport-summary.sh avoids the false claim by printing no quote at all,
      # which is weaker than this: it is silent about which of the two happened.
      # Naming it is better where there is a file to name it in, and that is the
      # difference between the two scripts rather than a rule one of them already
      # follows.
      if [ ! -s "$_crl_file" ]; then
        if [ -z "$_crl_err" ]; then
          printf '%s\n' "kubectl exited $_crl_rc for the $_crl_instance instance of $_crl_c; its message could not be captured (no writable temp dir on the machine producing this report), so whether it said anything is unknown" \
            > "$_crl_file" || true
        else
          printf '%s\n' "kubectl exited $_crl_rc without a message for the $_crl_instance instance of $_crl_c" \
            > "$_crl_file" || true
        fi
      fi
    else
      # A clean exit with no bytes is genuinely ambiguous, and the two readings
      # differ per instance: a current instance that never started leaves this,
      # and so does one that started and stayed quiet; for a previous instance the
      # first reading is that there was never a previous one at all.
      case "$_crl_instance" in
        previous) _crl_why="it never restarted, or the previous instance logged nothing" ;;
        *)        _crl_why="it never started, or it started and logged nothing" ;;
      esac
      if [ ! -s "$_crl_file" ]; then
        # "without error" only when there was none. A clean exit that still wrote a
        # deprecation or partial-result notice is a different fact, and the notice
        # itself is beside the log in READ-WARNINGS.txt rather than in here.
        if [ -n "$_crl_said" ]; then
          printf '%s\n' "kubectl exited 0 and wrote no output for the $_crl_instance instance of $_crl_c: $_crl_why. It did write to stderr; that message is in READ-WARNINGS.txt beside this file" \
            > "$_crl_file" || true
        else
          printf '%s\n' "kubectl exited without output or error for the $_crl_instance instance of $_crl_c: $_crl_why" \
            > "$_crl_file" || true
        fi
      fi
    fi
    return 0
  fi
  if cozyreport_timed_out "$_crl_rc"; then
    cozyreport_append_note "$_crl_file" \
      "[cozyreport] TRUNCATED: this log ends here because the read was cut off by $(cozyreport_cutoff_desc "$_crl_rc"), not because the container stopped writing"
  elif [ "$_crl_rc" -ne 0 ]; then
    # kubectl wrote log lines and then failed on its own terms -- a connection
    # reset to the kubelet mid-stream, not our clock. Reaching here at all means
    # stdout was non-empty, so this is a partial log and not a bare refusal, and
    # without a marker it reads as a complete one. The timeout is not named,
    # because no timeout fired.
    cozyreport_append_note "$_crl_file" \
      "[cozyreport] TRUNCATED: this log ends here because kubectl exited $_crl_rc part way through, not because the container stopped writing${_crl_reason:+: $_crl_reason}"
  fi

  if [ "$_crl_tail" != "-1" ]; then
    # Recorded whatever the read's outcome was, not only when it succeeded. A log
    # that was cut short at its END still lost its OLDEST lines to `--tail`, and
    # naming only the end leaves the reader believing the file starts where the
    # container did. Two bounds applied; both have to be named, or the one that is
    # named implies the other did not fire.
    #
    # The bound still has to be recorded -- `--tail` drops the OLDEST lines and
    # nothing in the output says so, and for a pod that never came up the startup
    # lines are the decisive part -- but it goes BESIDE the log, not into it.
    #
    # A log that was read successfully is a complete artifact of its own, and the
    # platform's controllers log JSON per line, so a trailing prose line is the
    # same defect this file avoids for pod.yaml one function up: a parser that can
    # read the file without the line stops reading it with one. YAML could take a `#`;
    # a log format cannot, so the note moves out instead.
    #
    # One file per pod directory rather than one per log: the bound is the same
    # for every container of the pod, and a reader who opens the directory finds
    # it once instead of at the foot of each file.
    printf '%s\n' \
      "[cozyreport] every log in this directory holds at most the last $_crl_tail lines; anything earlier was never requested. That is the bound that was APPLIED, which is not always the one COZY_REPORT_POD_TAIL was set to -- a value this script could not use is reported in the collection notes" \
      > "$(dirname "$_crl_file")/CONTAINER-LOGS-BOUND.txt" || true
  fi
  return 0
}

# cozyreport_is_count <value>: true for a non-negative integer this shell can
# actually compute with.
#
# All-digits is not the same test. `$(( now + 999999999999999999999999 ))` is a
# fatal error in dash -- "Illegal number", and dash is /bin/sh on the CI image --
# so an oversized budget would end the whole script at the deadline calculation,
# before the tarball is written, which is the one outcome every bound in this file
# exists to avoid. `[ n -gt 0 ]` and `head -n n` reject the same values on their
# own terms. Nine digits is orders of magnitude above any real cap or budget, and
# `date +%s` plus a value that size still fits the 64-bit arithmetic dash, bash and
# busybox ash all use (it would not fit a 32-bit one, which is why the bound is on
# the input rather than on the sum).
cozyreport_is_count() {
  case "$1" in
    '' | *[!0-9]*) return 1 ;;
    # A leading zero makes the value octal to `$(( ))` and decimal to `[`, so one
    # string means two different numbers inside this one script. Both readings are
    # wrong in their own way: COZY_REPORT_PODS_BUDGET=008 is not a valid octal
    # literal at all and dash aborts the whole script with "Illegal number: 008" at
    # the deadline calculation -- before the tarball is written, which is the single
    # outcome every bound in this file exists to avoid -- while 010 is valid octal
    # and silently means 8. The second is the worse of the two: an operator who
    # writes 010 gets a bound of 8 and a truncation note quoting the bound that was
    # applied, with nothing anywhere saying it was not the bound they asked for.
    # Rejected here so both land in the malformed-value path, which falls back to
    # the default and says so.
    0?*) return 1 ;;
  esac
  [ "${#1}" -le 9 ]
}

# One predicate, consulted by both the value and the sentence about it. Two `case`
# patterns written to mean the same thing do not: `00` and `01` are all-digits and
# non-zero as strings, so a split validator applies them while reporting them
# ignored (and `--tail=00` is `--tail=0`, which returns no lines and is then
# described as a container that never logged). `10m` is the mirror image: a glob
# like [1-9][0-9]* matches it, so the value quietly becomes the default while
# nothing says so. Compare against the resolved value instead of re-parsing.
cozyreport_pod_tail_ok() {
  [ "$1" = "-1" ] && return 0
  cozyreport_is_count "$1" || return 1
  # In range here, so a leading zero and a plain zero are both caught by the
  # numeric comparison rather than by another pattern that has to agree with this
  # one. `--tail=0` asks kubectl for nothing at all.
  [ "$1" -gt 0 ]
}

# cozyreport_pod_tail: the --tail value for the per-pod log reads.
#
# A line tail rather than --limit-bytes: for these pods the decisive lines are at
# the END, and --limit-bytes cuts mid-line and mid-UTF-8. COZY_REPORT_POD_TAIL=-1
# restores the whole log. A malformed value and 0 both fall back to the default
# (--tail=0 returns nothing, which this script would then describe as a container
# that never logged); cozyreport_pod_tail_note reports either case.
cozyreport_pod_tail() {
  _cpt=${COZY_REPORT_POD_TAIL:-2000}
  cozyreport_pod_tail_ok "$_cpt" || _cpt=2000
  printf '%s' "$_cpt"
}

# cozyreport_pod_tail_note: empty when the knob was usable, otherwise the sentence
# the report should carry about it. Shares cozyreport_pod_tail_ok with the resolver
# so the value and the claim about it cannot disagree.
cozyreport_pod_tail_note() {
  cozyreport_pod_tail_ok "${COZY_REPORT_POD_TAIL:-2000}" && return 0
  printf '%s' "ignored a malformed COZY_REPORT_POD_TAIL='${COZY_REPORT_POD_TAIL:-}' and used 2000."
}

# cozyreport_collect_pod <namespace> <pod> <output-dir>
#
# Everything the report keeps about one broken pod. Every call is `|| true` and the
# function always returns 0: collecting evidence about a broken pod fails routinely,
# and none of it may abort the caller. Every read is bounded, `describe` included,
# since the tarball is written at the very end of the run.
cozyreport_collect_pod() {
  _ccp_ns=$1
  _ccp_pod=$2
  _ccp_dir=$3

  # Not probed for writability the way the previous-logs capture probes its own
  # output directory, and the difference is where the path comes from. That one
  # takes it from its caller, so it can be a file, or missing, or someone else's;
  # this one lives under the `mktemp -d` the top of this script refuses to run
  # without. Under a root that exists and is writable, this `mkdir` fails only
  # when the filesystem does -- and that is the case where the note explaining it
  # has nowhere to go either.
  mkdir -p "$_ccp_dir" || true
  cozyreport_read_object "$_ccp_dir/pod.yaml" \
    kubectl get pod -n "$_ccp_ns" "$_ccp_pod" -o yaml
  cozyreport_read_object "$_ccp_dir/describe.txt" \
    kubectl describe pod -n "$_ccp_ns" "$_ccp_pod"

  # kubectl's own reason for not answering is the finding, so keep its stderr
  # instead of letting it escape to the console and reporting a bare "could not
  # list containers" that names no cause. The scratch file is mktemp'd outside the
  # report tree: a predictable name under /tmp is a symlink an unprivileged
  # process can plant ahead of the redirect, and a run killed between writing and
  # removing it inside the tree would ship a stray dotfile in the artifact.
  _ccp_err=$(mktemp "${TMPDIR:-/tmp}/cozyreport-stderr.XXXXXX" 2>/dev/null) || _ccp_err=""
  # shellcheck disable=SC2086
  if _ccp_containers=$($COZYREPORT_BOUND kubectl get pod -n "$_ccp_ns" "$_ccp_pod" \
      -o jsonpath='{.spec.containers[*].name} {.spec.initContainers[*].name}' \
      2>"${_ccp_err:-/dev/null}"); then
    _ccp_rc=0
  else
    _ccp_rc=$?
  fi
  # Blank, not empty. The template above holds two expressions with a literal
  # space between them, and kubectl emits that space whatever the fields contain,
  # so a read that names no container arrives as " " and not as "". Command
  # substitution strips trailing NEWLINES, not spaces, so `[ -z ]` is false for it
  # -- the read falls through to the `elif`, which reports "kubectl named 0
  # container(s) and then exited", and on a clean exit falls past both, leaving a
  # pod directory holding pod.yaml and describe.txt and no logs at all. That is
  # exactly the "reads as containers that were silent" shape the rest of this file
  # exists to remove, reintroduced by the one branch meant to catch it.
  #
  # `case`, not `tr -d '[:space:]'`: same answer without forking twice per pod, and
  # the bracket-class form behaves identically under dash, bash and BSD sh.
  case "$_ccp_containers" in
    *[![:space:]]*) _ccp_named=1 ;;
    *)              _ccp_named=0 ;;
  esac
  if [ "$_ccp_named" -eq 0 ]; then
    # printf, not echo: kubectl's message is untrusted text, and under dash
    # (/bin/sh on the CI image) echo expands backslash escapes, so a message
    # carrying one could forge extra lines in a file whose whole job is to be
    # believed.
    #
    # kubectl's stderr is copied whole rather than reduced to a chosen line. This
    # is a file of its own, not a log line with neighbours to bury, so there is
    # nothing to be gained by picking -- and picking is where it goes wrong: the
    # cluster-wide read below prefixes its reason with one klog discovery-retry
    # line per attempt, and the same pod read gets the same preamble whenever
    # discovery is cold, which a multi-hour failed run will produce.
    {
      printf '%s\n' "no per-container logs were collected for $_ccp_ns/$_ccp_pod: its container list came back empty"
      if [ -n "$_ccp_err" ] && [ -s "$_ccp_err" ]; then
        printf '%s\n' "kubectl said:"
        cat "$_ccp_err"
      elif cozyreport_timed_out "$_ccp_rc"; then
        printf '%s\n' "kubectl said nothing: the read was cut off by $(cozyreport_cutoff_desc "$_ccp_rc")"
      elif [ -z "$_ccp_err" ]; then
        printf '%s\n' "kubectl exited $_ccp_rc; its message could not be captured (no writable temp dir here), so whether it said anything is unknown"
      else
        printf '%s\n' "kubectl said nothing and exited $_ccp_rc"
      fi
    } > "$_ccp_dir/logs-UNAVAILABLE.txt" || true
  elif [ "$_ccp_rc" -ne 0 ]; then
    # Names arrived AND the read failed: the list is a prefix, not the pod's
    # containers. Without this the pod directory holds logs for the containers
    # kubectl managed to name and nothing at all for the rest -- no file, no
    # marker, and a reader counts the log files and believes that is the pod.
    # The empty-list branch above is not reached, because the list is not empty;
    # it is just not all of it.
    {
      printf '%s\n' "the container list for $_ccp_ns/$_ccp_pod is INCOMPLETE: kubectl named $(printf '%s' "$_ccp_containers" | wc -w | tr -d ' ') container(s) and then exited $_ccp_rc, so containers it never named have no log file here and their absence says nothing about them"
      if [ -n "$_ccp_err" ] && [ -s "$_ccp_err" ]; then
        printf '%s\n' "kubectl said:"
        cat "$_ccp_err"
      fi
    } > "$_ccp_dir/CONTAINER-LIST-INCOMPLETE.txt" || true
  fi
  [ -z "$_ccp_err" ] || rm -f "$_ccp_err"

  # Every container is asked, including on a pod that never started one: kubectl's
  # refusal is itself the finding. An empty result is ambiguous -- against an
  # unscheduled pod kubectl can exit 0 having printed nothing -- so the placeholder
  # reports what was observed and leaves the cause open.
  for _ccp_c in $_ccp_containers; do
    # COZYREPORT_COLLECT_DEADLINE, when the caller set one, bounds this loop as
    # well as the walk above it. Without it a single pod's overshoot past the
    # collection budget would grow with its container count -- 60s more per
    # container against a degraded apiserver -- and a pod with a dozen of them
    # would blow a ceiling stated in terms of one. Checked between containers,
    # never inside one, so each log file present is a whole log file.
    if [ -n "${COZYREPORT_COLLECT_DEADLINE:-}" ] &&
       [ "$(date +%s)" -ge "$COZYREPORT_COLLECT_DEADLINE" ]; then
      # Says which containers were ATTEMPTED, not which were read whole. The
      # budget runs out because reads are slow, and the ordinary way for a read to
      # be slow is to hit its own 30s ceiling -- so a log file in this directory
      # carrying its own TRUNCATED marker is the likely case, not the exotic one,
      # and "were read in full" would be false about the file lying next to this
      # one. Same for pod.yaml and describe.txt: they are attempted before any
      # container, but describe is the most expensive read here and can be cut off
      # like anything else. Every bounded file states its own bound on its last
      # line, so point there rather than making a claim this function cannot check.
      printf '%s\n' \
        "the collection budget elapsed part way through this pod: the containers with a log file here were attempted, and the ones with no log file were not" \
        "whether each of those reads finished is stated on the last line of the file itself: a read cut short carries a [cozyreport] TRUNCATED line" \
        "pod.yaml and describe.txt were attempted before any container log, and carry the same marker if they were cut short" \
        > "$_ccp_dir/CONTAINER-LOGS-TRUNCATED.txt" || true
      break
    fi
    cozyreport_read_container_log "$_ccp_dir/logs-$_ccp_c.txt" current \
      "$_ccp_ns" "$_ccp_pod" "$_ccp_c"
    cozyreport_read_container_log "$_ccp_dir/logs-$_ccp_c-previous.txt" previous \
      "$_ccp_ns" "$_ccp_pod" "$_ccp_c" --previous
  done
  return 0
}

# cozyreport_collect_broken_pods <report-dir>
#
# Join the selection to the collection: select not-Ready pods, order tenant
# namespaces first, and collect per-pod evidence for at most COZY_REPORT_PODS_MAX
# of them within COZY_REPORT_PODS_BUDGET seconds.
#
# The budget is a deadline for STARTING a read, checked between pods and between a
# pod's containers, so the budget itself never cuts a read in progress and the
# overshoot is bounded by the reads already in flight rather than by the pod's
# container count. A file can still end early -- its own per-read bound, or kubectl
# failing mid-stream -- and when it does it says so on its last line; what the
# deadline never does is leave a file short with nothing recording why.
#
# Neither bound is silent: whichever fired says so in COLLECTION-TRUNCATED.txt, and
# a pod list that never returned leaves COLLECTION-FAILED.txt rather than the empty
# tree a healthy cluster produces.
cozyreport_collect_broken_pods() {
  _cbp_root=$1
  # Without `timeout` every bounded read would exit 127, so the reads run unbounded
  # instead and the report says so. Refusing to collect would have been the other
  # option, and it is the wrong one twice over: an unbounded read still collects
  # the tree, so refusing turns a missing local binary into a lost section, and
  # notes-and-proceeds for the same dependency -- two halves of one tarball
  # answering the same question differently is worse than either answer.
  if [ -z "$COZYREPORT_BOUND" ]; then
    mkdir -p "$_cbp_root" || true
    printf '%s\n' \
      "timeout is not on PATH, so every read in this report ran unbounded -- not only the ones behind this directory" \
      "this is a missing dependency on the machine that produced the report, not a statement about the cluster: no read anywhere in this tarball was cut off by a wall-clock timeout, because there was none to fire" \
      "the note sits here because this is the section that always exists; it is a statement about the run. That is the only bound it is about. The pod cap, the collection budget and the per-log --tail all still applied, and each says so in its own file where it fired" \
      > "$_cbp_root/COLLECTION-UNBOUNDED.txt" || true
  fi
  # Both knobs fall back to their default on a malformed value, and both say so
  # rather than swallowing it. An operator who wrote COZY_REPORT_PODS_BUDGET=10m
  # and got 600 with no trace would read the resulting truncation note as the
  # bound they asked for; a bound that hides its own effect is the defect this
  # whole change is about. The note names the value that was applied, never the
  # typo, so the two cannot be confused.
  _cbp_notes=""
  _cbp_max=${COZY_REPORT_PODS_MAX:-$COZYREPORT_PODS_DEFAULT}
  if ! cozyreport_is_count "$_cbp_max"; then
    _cbp_notes="ignored a malformed COZY_REPORT_PODS_MAX='${COZY_REPORT_PODS_MAX:-}' and used $COZYREPORT_PODS_DEFAULT."
    _cbp_max=$COZYREPORT_PODS_DEFAULT
  fi
  _cbp_budget=${COZY_REPORT_PODS_BUDGET:-600}
  if ! cozyreport_is_count "$_cbp_budget"; then
    _cbp_notes="${_cbp_notes:+$_cbp_notes }ignored a malformed COZY_REPORT_PODS_BUDGET='${COZY_REPORT_PODS_BUDGET:-}' and used 600."
    _cbp_budget=600
  fi
  # The third knob is resolved per read rather than here, so its note is fetched
  # rather than produced: one source for the value, one for the sentence about it.
  _cbp_tail_note=$(cozyreport_pod_tail_note)
  _cbp_notes="${_cbp_notes:+$_cbp_notes }${_cbp_tail_note}"
  # Trim the separator a leading empty half leaves behind.
  _cbp_notes=${_cbp_notes# }
  _cbp_notes=${_cbp_notes% }

  # The pod list is read on its own, keeping its exit status: piping it into the
  # selection discards that status, and then a refused list renders identically to
  # a cluster where every pod is Ready.
  #
  # mktemp rather than a `$$` name: a predictable path under a world-writable
  # directory is a symlink an unprivileged process can plant ahead of the redirect.
  _cbp_err=$(mktemp "${TMPDIR:-/tmp}/cozyreport-podlist.XXXXXX" 2>/dev/null) || _cbp_err=""
  # The `if` rather than a bare assignment plus `$?`: a caller that sourced these
  # helpers under `set -e` would abort on the failing assignment before the branch
  # that reports it ever ran.
  # shellcheck disable=SC2086
  if _cbp_all=$($COZYREPORT_BOUND kubectl get pod -A --no-headers 2>"${_cbp_err:-/dev/null}"); then
    _cbp_rc=0
  else
    _cbp_rc=$?
  fi
  # A failed read that printed rows first is a PARTIAL list, not an absent one.
  # Discarding those rows loses the only pods the read managed to name, and the
  # note below would then say nothing was ever looked at while kubectl had just
  # named two crash-looping tenants. Kept and marked, the same way a partial log
  # is kept and marked -- and the summary's own pod list already does this, so
  # discarding here would have one tarball answering the same question two ways.
  if [ "$_cbp_rc" -ne 0 ] && [ -n "$_cbp_all" ]; then
    mkdir -p "$_cbp_root" || true
    printf '%s\n' "[cozyreport] the cluster-wide pod list did not return (kubectl exit $_cbp_rc); the pods collected here are the ones it named before it stopped, and there may be others it never reached" \
      >> "$_cbp_root/COLLECTION-FAILED.txt" || true
    if [ -n "$_cbp_err" ] && [ -s "$_cbp_err" ]; then
      printf 'kubectl said: %s\n' "$( { tail -n 1 "$_cbp_err" | cut -c1-200; } || true )" \
        >> "$_cbp_root/COLLECTION-FAILED.txt" || true
    fi
    [ -z "$_cbp_err" ] || rm -f "$_cbp_err"
  elif [ "$_cbp_rc" -ne 0 ]; then
    mkdir -p "$_cbp_root" || true
    # A read cut off by its own timeout is killed before kubectl writes anything,
    # so its stderr is empty; a refusal comes with the reason. Reporting the two
    # apart matters because they are different problems -- an apiserver too slow
    # to answer versus one answering "forbidden" -- and a bare "kubectl said:"
    # with nothing after it names neither.
    # LAST line, not the first: a failing cluster-wide list is preceded by one
    # klog "couldn't get current server API group list" line per discovery retry,
    # each long enough that trimming it to 200 characters cuts off the cause it
    # ends with. kubectl's own error handler writes the actionable message last.
    # `|| true`, because `$([ -n "$v" ] && cmd)` exits 1 when the guard is false
    # and hack/cozyreport-summary.sh sources these helpers under `set -eu`.
    _cbp_said=$( { [ -n "$_cbp_err" ] && tail -n 1 "$_cbp_err" 2>/dev/null | cut -c1-200; } || true )
    # printf, not echo: kubectl's text is untrusted and dash's echo expands
    # backslash escapes, so a message carrying one could forge a second line in a
    # file whose whole job is to be believed.
    {
      printf '%s\n' "no pod evidence was collected: the cluster-wide pod list did not return"
      printf '%s\n' "this is NOT a report that every pod was Ready -- nothing was ever looked at"
      if [ -n "$_cbp_said" ]; then
        printf 'kubectl said: %s\n' "$_cbp_said"
      elif cozyreport_timed_out "$_cbp_rc"; then
        # Branched, not a disjunction: "exited 1: at 124 or 137 the read was cut
        # off by its own 30s timeout" hands the reader a mechanism that was not in
        # play and asks them to evaluate a condition the code already evaluated.
        #
        # And the timeout branch comes FIRST, matching the three sibling sites.
        # The two conditions overlap whenever a killed read also had nowhere to
        # write its stderr, and the comment thirty lines up settles which answer is
        # better there: a read cut off at 124 is killed before kubectl writes
        # anything, so its stderr is empty by construction and "we could not
        # capture the message" leads with the accident rather than the cause. With
        # the orders reversed one tarball answered "why did this read produce
        # nothing" two ways, in three files, about a mechanism it had observed.
        printf '%s\n' "kubectl said nothing: the read was cut off by $(cozyreport_cutoff_desc "$_cbp_rc")"
      elif [ -z "$_cbp_err" ]; then
        printf '%s\n' "kubectl exited $_cbp_rc; its message could not be captured (no writable temp dir here), so whether it said anything is unknown"
      else
        printf '%s\n' "kubectl said nothing and exited $_cbp_rc"
      fi
    } > "$_cbp_root/COLLECTION-FAILED.txt" || true
    # A knob the operator set and this run could not use is reported here too. The
    # note block at the end of this function is unreachable from this branch, so
    # without this an operator who mistyped a bound AND hit an unreachable
    # apiserver would be told about the second and never about the first -- while
    # the comment on that block promises the value is named whether or not either
    # bound fired.
    if [ -n "$_cbp_notes" ]; then
      printf '%s\n' "This run $_cbp_notes" >> "$_cbp_root/COLLECTION-FAILED.txt" || true
    fi
    [ -z "$_cbp_err" ] || rm -f "$_cbp_err"
    return 0
  fi
  [ -z "$_cbp_err" ] || rm -f "$_cbp_err"
  _cbp_rows=$(printf '%s\n' "$_cbp_all" | cozyreport_pods_not_ready | cozyreport_pods_prioritize) || true

  _cbp_total=$(printf '%s\n' "$_cbp_rows" | grep -c . || true)
  case "$_cbp_total" in '' | *[!0-9]*) _cbp_total=0 ;; esac

  _cbp_deadline=$(( $(date +%s) + _cbp_budget ))
  # Handed to the per-pod collection as well, so the overshoot past the budget is
  # one container's reads rather than one pod's however-many. Cleared before this
  # function returns: leaving it set would silently bound whatever a later caller
  # collects, with a deadline computed for a walk that has already finished.
  COZYREPORT_COLLECT_DEADLINE=$_cbp_deadline
  _cbp_done=0
  # A zero cap is handled here rather than left to `head -n 0`: GNU head treats
  # that as an empty result, BSD/macOS head rejects it ("illegal line count -- 0")
  # and would abort a local run. The overflow is still reported below, so a cap of
  # zero collects nothing visibly rather than silently.
  if [ "$_cbp_max" -eq 0 ]; then
    _cbp_admitted=""
  else
    _cbp_admitted=$(printf '%s\n' "$_cbp_rows" | head -n "$_cbp_max")
  fi
  _cbp_admitted_n=$(printf '%s\n' "$_cbp_admitted" | grep -c . || true)
  case "$_cbp_admitted_n" in '' | *[!0-9]*) _cbp_admitted_n=0 ;; esac
  printf '%s\n' "$_cbp_admitted" |
    while read -r _cbp_ns _cbp_pod _; do
      # An empty selection still arrives as one blank line. Skipping it here
      # rather than short-circuiting above keeps one guard for the case instead
      # of two, so the one that remains is the one under test.
      [ -n "$_cbp_ns" ] && [ -n "$_cbp_pod" ] || continue
      # Checked before the pod rather than inside it: this is the only point at
      # which stopping leaves a complete pod behind and a note explaining the
      # stop, which is why the budget bounds when a pod may start rather than
      # when the walk must end. The loop body runs in a subshell, so the note is
      # written from in here instead of counted up and reported after the pipe.
      if [ "$(date +%s)" -ge "$_cbp_deadline" ]; then
        mkdir -p "$_cbp_root" || true
        # Denominator is what the walk could have reached, not the whole
        # selection: the cap already removed everything past $_cbp_max before the
        # loop began, so "$_cbp_done of $_cbp_total" offers a reach the collector
        # never had. Both bounds are named when both applied -- an operator who
        # reads only the budget line and raises the budget would get $_cbp_max
        # pods and no explanation, which is the bound-hiding-its-own-effect defect
        # this file argues against elsewhere.
        printf '%s\n' \
          "stopped after $_cbp_done of $_cbp_admitted_n not-Ready pods it could reach: the ${_cbp_budget}s collection budget elapsed (COZY_REPORT_PODS_BUDGET=$_cbp_budget)." \
          "If a pod here holds a CONTAINER-LOGS-TRUNCATED.txt then the budget expired part way through that one. For every other pod here each read was attempted; whether it finished is on the last line of each file, since a read cut short by its own timeout says so there." \
          "The rest are still listed in the report's kubernetes/pods.txt (one level up from this directory)." \
          > "$_cbp_root/COLLECTION-TRUNCATED.txt" || true
        if [ "$_cbp_total" -gt "$_cbp_max" ]; then
          printf '%s\n' \
            "COZY_REPORT_PODS_MAX=$_cbp_max also applied: only $_cbp_max of $_cbp_total not-Ready pods were eligible before the budget ran out, so raising the budget alone will not collect the rest." \
            >> "$_cbp_root/COLLECTION-TRUNCATED.txt" || true
        fi
        break
      fi
      cozyreport_collect_pod "$_cbp_ns" "$_cbp_pod" "$_cbp_root/$_cbp_ns/$_cbp_pod"
      _cbp_done=$((_cbp_done + 1))
    done

  # The budget can also run out DURING the last pod the cap admitted, and then
  # there is no next iteration to notice it: the loop ends because the cap ended
  # it, and a cap-only note would date the stop to the wrong bound while calling
  # a pod complete that holds fewer container logs than it has containers.
  #
  # The signal is the marker the per-pod collection leaves when it actually skips
  # containers, not the clock. "The deadline has passed by now" is a different
  # question and answering it here would report a truncation that never happened:
  # a final pod whose reads were slow but all completed skipped nothing, and a
  # zero-second budget on a healthy cluster would file a truncation note for a
  # collection that had nothing to collect.
  _cbp_cut=$(find "$_cbp_root" -type f -name CONTAINER-LOGS-TRUNCATED.txt 2>/dev/null | head -n 1)
  if [ -n "$_cbp_cut" ]; then
    _cbp_expired=1
  else
    _cbp_expired=0
  fi

  # The budget note the loop writes when it stops BETWEEN pods is the more
  # proximate truth and is not overwritten here: a walk that ran out of time
  # stopped short of the cap as well, and saying "reached the cap" of a walk that
  # never got there would send a reader looking for pods never attempted.
  if [ ! -f "$_cbp_root/COLLECTION-TRUNCATED.txt" ] &&
     { [ "$_cbp_total" -gt "$_cbp_max" ] || [ "$_cbp_expired" -eq 1 ]; }; then
    mkdir -p "$_cbp_root" || true
    {
      if [ "$_cbp_total" -gt "$_cbp_max" ]; then
        printf '%s\n' \
          "collected $_cbp_max of $_cbp_total not-Ready pods (COZY_REPORT_PODS_MAX=$_cbp_max)." \
          "The $((_cbp_total - _cbp_max)) not collected here are still listed in the report's kubernetes/pods.txt (one level up from this directory)."
      fi
      if [ "$_cbp_expired" -eq 1 ]; then
        printf '%s\n' \
          "the ${_cbp_budget}s collection budget (COZY_REPORT_PODS_BUDGET=$_cbp_budget) ran out part way through a pod collected here, so it holds fewer container logs than that pod has containers." \
          "The CONTAINER-LOGS-TRUNCATED.txt beside them names it. For every other pod here each read was attempted; a read cut short by its own timeout says so on the last line of its own file."
      fi
    } > "$_cbp_root/COLLECTION-TRUNCATED.txt" || true
  fi

  COZYREPORT_COLLECT_DEADLINE=""

  # A discarded knob value is reported whether or not either bound fired: the
  # operator who set it is entitled to know it was not the bound that applied, and
  # a run that then completed inside the default would otherwise carry no trace of
  # the value it ignored.
  if [ -n "$_cbp_notes" ]; then
    mkdir -p "$_cbp_root" || true
    if [ -f "$_cbp_root/COLLECTION-TRUNCATED.txt" ]; then
      printf '%s\n' "This run also $_cbp_notes" >> "$_cbp_root/COLLECTION-TRUNCATED.txt" || true
    else
      printf '%s\n' "This run $_cbp_notes" > "$_cbp_root/COLLECTION-NOTES.txt" || true
    fi
  fi
  return 0
}

# cozyreport_collect_runner_identity <dir>: the runner VM's own identity, read on
# that machine by hack/e2e-runner-identity.sh before this container existed and
# carried in with the tree.
#
# Copied rather than re-read: it describes the machine a layer above this
# container, whose link-local metadata service this container has no guaranteed
# route to, so an attempt from in here would answer "unreachable" about a machine
# that is fine.
#
# One filename either way, holding either the record or the reason there is none.
# A marker file beside an absent one would carry the same words and be found by
# nobody, because the absence is where a reader stops.
#
# A function rather than four lines in the module below, so both arms are
# reachable from the unit suite. The module itself needs a cluster, and this is the
# one part of it whose behaviour is decided entirely by a local file.
cozyreport_collect_runner_identity() {
  _cri_dir=$1
  _cri_file=${COZY_RUNNER_IDENTITY_FILE:-_out/runner-identity.txt}
  # `-s` rather than `-f`: a write cut off by a full disk leaves the file there and
  # empty, and copying that through would put a zero-byte runner-identity.txt in
  # the report -- holding neither the record nor a reason, which is the one outcome
  # the promise below rules out.
  if [ -s "$_cri_file" ]; then
    # Through the same bounded reader as the host commands in the module below,
    # for the uniformity rather than for the risk: it is a local file of a few
    # hundred bytes, and the reader is what gives it the same TRUNCATED and
    # READ-WARNINGS treatment as its neighbours.
    cozyreport_read_object "$_cri_dir/runner-identity.txt" cat "$_cri_file"
    return 0
  fi
  # Three causes, not one. The capture that writes this record is taken on the
  # runner VM before this container exists (hack/e2e-runner-identity.sh, from
  # packages/core/testing/Makefile) and it never fails the run, so a missing file
  # says the step did not run, OR that it ran and could not write -- in which case
  # the record is in that step's own log and only the artifact copy is missing.
  # Naming one of them would be this collector asserting a cause it did not
  # observe, in a file whose whole premise is that its notes can be believed.
  printf '%s\n' "# [cozyreport] no runner identity record at $_cri_file (absent, or present and empty), so this report says nothing about the machine the sandbox ran on. Three things produce this: the capture step did not run, a sandbox was created by hand, or the capture ran and could not write its file -- and in that last case the record is in the log of the step that took it, which exits 0 either way. It is NOT a statement that the runner was an ordinary one: an unidentified runner and an unremarkable one leave the same absence here." \
    > "$_cri_dir/runner-identity.txt" || true
  return 0
}

# Let a focused BATS test, or hack/cozyreport-summary.sh, source the helpers
# above without running the full cluster report. Nothing below this point runs
# for such a caller.
if [ -n "${COZYREPORT_LIB:-}" ]; then
  return 0 2>/dev/null
fi

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPORT_DATE=$(date +%Y-%m-%d_%H-%M-%S)
REPORT_NAME=${1:-cozyreport-$REPORT_DATE}
REPORT_PDIR=$(mktemp -d)
# Fail here rather than carry an empty prefix forward. This file sets no errexit,
# so a failing `mktemp -d` leaves REPORT_PDIR empty and the script runs on: every
# path below becomes absolute from the filesystem root. Nothing stops the run
# there: with no errexit a refused `mkdir -p` is just a non-zero status nobody
# reads, and the six that carry `|| true` do not even leave that. As an
# unprivileged user that is a permission
# error and an empty tree; as root in a container -- which is how CI runs this --
# it is directory creation at `/` and a report written into the host's root, with
# the tarball then assembled from nothing.
#
# `${VAR:?}` exits a non-interactive shell on its own, which is what makes this
# work without errexit; the two `|| exit` dependency checks below rely on the same
# property of `||` rather than on a shell option.
: "${REPORT_PDIR:?mktemp -d failed, refusing to write a report to the filesystem root}"
REPORT_DIR=$REPORT_PDIR/$REPORT_NAME

# -- check dependencies
command -V kubectl >/dev/null || exit $?
command -V tar >/dev/null || exit $?
# `timeout` is what bounds every read in this file. Without it they run with no
# ceiling instead, which is what this collector did throughout before the bounds
# existed and is strictly better than refusing to collect, so this is a warning
# rather than an exit: the caller wraps this script in `|| true`, and exiting here
# would trade the whole tarball -- Talos, LINSTOR, Flux, COSI, all of it -- for one
# dependency. COLLECTION-UNBOUNDED.txt says so in the artifact, and the step's own
# ceiling is then the only thing left between a hung read and a lost tarball.
if [ -z "$COZYREPORT_BOUND" ]; then
  echo "WARNING: timeout is not on PATH; every read will be taken with no ceiling, and the report will say so"
fi

# -- cozystack module
echo "Collecting Cozystack information..."
mkdir -p $REPORT_DIR/cozystack
# cozy-system/cozystack-operator, the same Deployment the log reads below name.
# This line asked for `cozystack`, which the installer stopped shipping when it
# moved to the operator, so the first file of the report -- the one that says
# which build produced everything after it -- carried `deployments.apps
# "cozystack" not found` on every run. Same defect as the copy-pasted kamaji gate
# below: a name that exists nowhere fails quietly and looks like evidence.
cozyreport_read_object "$REPORT_DIR/cozystack/image.txt" \
  kubectl get deploy -n cozy-system cozystack-operator -o jsonpath='{.spec.template.spec.containers[0].image}'
if cozyreport_probe "cozystack operator logs" kubectl get deploy -n cozy-system cozystack-operator; then
  cozyreport_read_object "$REPORT_DIR/cozystack/operator.log" \
    kubectl logs -n cozy-system deploy/cozystack-operator --tail=2000
  cozyreport_read_object "$REPORT_DIR/cozystack/operator-previous.log" \
    kubectl logs -n cozy-system deploy/cozystack-operator --tail=2000 --previous
fi
cozyreport_select_objects "cozystack configmaps" kubectl get cm -n cozy-system --no-headers | awk '$1 ~ /^cozystack/' |
  while read NAME _; do
    DIR=$REPORT_DIR/cozystack/configs
    mkdir -p $DIR
    cozyreport_read_object "$DIR/$NAME.yaml" kubectl get cm -n cozy-system "$NAME" -o yaml
  done

# -- flux module
echo "Collecting Flux controller state..."
mkdir -p $REPORT_DIR/flux
for ctrl in helm-controller source-controller notification-controller kustomize-controller; do
  if cozyreport_probe "flux $ctrl logs" kubectl get deploy -n cozy-fluxcd "$ctrl"; then
    cozyreport_read_object "$REPORT_DIR/flux/$ctrl.log" \
      kubectl logs -n cozy-fluxcd "deploy/$ctrl" --tail=2000
    cozyreport_read_object "$REPORT_DIR/flux/$ctrl-previous.log" \
      kubectl logs -n cozy-fluxcd "deploy/$ctrl" --tail=2000 --previous
  fi
done

echo "Collecting Flux sources..."
for kind in helmrepositories.source.toolkit.fluxcd.io ocirepositories.source.toolkit.fluxcd.io gitrepositories.source.toolkit.fluxcd.io externalartifacts.source.toolkit.fluxcd.io; do
  short=${kind%%.*}
  cozyreport_read_object "$REPORT_DIR/flux/$short.txt" kubectl get "$kind" -A
  cozyreport_read_object "$REPORT_DIR/flux/$short.yaml" kubectl get "$kind" -A -o yaml
done

# -- cert-manager module
if cozyreport_probe "cert-manager" kubectl get crd certificates.cert-manager.io; then
  echo "Collecting cert-manager state..."
  DIR=$REPORT_DIR/cert-manager
  mkdir -p $DIR
  cozyreport_read_object "$DIR/certificates.txt" kubectl get certificates.cert-manager.io -A
  cozyreport_read_object "$DIR/certificaterequests.txt" kubectl get certificaterequests.cert-manager.io -A
  cozyreport_read_object "$DIR/orders.txt" kubectl get orders.acme.cert-manager.io -A
  cozyreport_read_object "$DIR/challenges.txt" kubectl get challenges.acme.cert-manager.io -A
  # Per non-Ready cert: full yaml + describe
  cozyreport_select_objects "certificates" kubectl get certificates.cert-manager.io -A --no-headers | awk '$3 != "True"' | \
    while read NAMESPACE NAME _; do
      cdir=$DIR/certificates/$NAMESPACE/$NAME
      mkdir -p $cdir
      cozyreport_read_object "$cdir/cert.yaml" \
        kubectl get certificates.cert-manager.io -n "$NAMESPACE" "$NAME" -o yaml
      cozyreport_read_object "$cdir/describe.txt" \
        kubectl describe certificates.cert-manager.io -n "$NAMESPACE" "$NAME"
    done
  if cozyreport_probe "cert-manager logs" kubectl get deploy -n cozy-cert-manager cert-manager; then
    cozyreport_read_object "$DIR/cert-manager.log" \
      kubectl logs -n cozy-cert-manager deploy/cert-manager --tail=2000
    cozyreport_read_object "$DIR/cert-manager-webhook.log" \
      kubectl logs -n cozy-cert-manager deploy/cert-manager-webhook --tail=2000
  fi
fi

# -- kubernetes module

echo "Collecting Kubernetes information..."
mkdir -p $REPORT_DIR/kubernetes
cozyreport_read_object "$REPORT_DIR/kubernetes/version.txt" kubectl version

echo "Collecting nodes..."
cozyreport_read_object "$REPORT_DIR/kubernetes/nodes.txt" kubectl get nodes -o wide
cozyreport_select_objects "nodes" kubectl get nodes --no-headers | awk '$2 != "Ready"' |
  while read NAME _; do
    DIR=$REPORT_DIR/kubernetes/nodes/$NAME
    mkdir -p $DIR
    cozyreport_read_object "$DIR/node.yaml" kubectl get node "$NAME" -o yaml
    cozyreport_read_object "$DIR/describe.txt" kubectl describe node "$NAME"
  done

echo "Collecting namespaces..."
cozyreport_read_object "$REPORT_DIR/kubernetes/namespaces.txt" kubectl get ns -o wide
cozyreport_select_objects "namespaces" kubectl get ns --no-headers | awk '$2 != "Active"' |
  while read NAME _; do
    DIR=$REPORT_DIR/kubernetes/namespaces/$NAME
    mkdir -p $DIR
    cozyreport_read_object "$DIR/namespace.yaml" kubectl get ns "$NAME" -o yaml
    cozyreport_read_object "$DIR/describe.txt" kubectl describe ns "$NAME"
  done

echo "Collecting events..."
# The read most likely to hit the 30s ceiling in this whole file, and worth knowing
# before `events.txt` is read as a complete picture. `--sort-by` sorts CLIENT-side,
# so kubectl pulls every event in the cluster and orders them locally, against the
# degraded apiserver this tool exists for -- and it does that under the same flat
# ceiling as a single-object describe. A truncated events.txt says so on its last
# line; an events.txt that is merely SHORT is the ordinary case for a large cluster.
cozyreport_read_object "$REPORT_DIR/kubernetes/events.txt" \
  kubectl get events -A --sort-by=.lastTimestamp
# Filter to warning-class and recent for quick triage
cozyreport_read_object "$REPORT_DIR/kubernetes/events-warnings.txt" \
  kubectl get events -A --sort-by=.lastTimestamp \
  -o jsonpath='{range .items[?(@.type!="Normal")]}{.lastTimestamp}{"\t"}{.involvedObject.namespace}/{.involvedObject.kind}/{.involvedObject.name}{"\t"}{.reason}{"\t"}{.message}{"\n"}{end}'

echo "Collecting helmreleases..."
cozyreport_read_object "$REPORT_DIR/kubernetes/helmreleases.txt" kubectl get hr -A
cozyreport_select_objects "helmreleases" kubectl get hr -A --no-headers | awk '$4 != "True"' | \
  while read NAMESPACE NAME _; do
    DIR=$REPORT_DIR/kubernetes/helmreleases/$NAMESPACE/$NAME
    mkdir -p $DIR
    cozyreport_read_object "$DIR/hr.yaml" kubectl get hr -n "$NAMESPACE" "$NAME" -o yaml
    cozyreport_read_object "$DIR/describe.txt" kubectl describe hr -n "$NAMESPACE" "$NAME"
    # Helm storage secrets: latest revision per release. Bounded with the bare
    # wrapper rather than through a reader: both of these feed a pipeline whose
    # output is decoded rather than kept, so there is no file for a marker to go
    # on, and a note per HelmRelease would put one line per release into
    # COLLECTION-FAILED.txt on a cluster whose apiserver is refusing -- which is
    # the state a report is taken in. The release's own hr.yaml and describe.txt
    # are beside this and carry their own markers.
    # shellcheck disable=SC2086  # empty COZYREPORT_BOUND must vanish, not become ""
    $COZYREPORT_BOUND kubectl get secret -n $NAMESPACE -l owner=helm,name=$NAME --sort-by='.metadata.creationTimestamp' --no-headers 2>/dev/null | \
      tail -1 | awk '{print $1}' | while read SECRET; do
        [ -z "$SECRET" ] && continue
        # shellcheck disable=SC2086
        $COZYREPORT_BOUND kubectl get secret -n $NAMESPACE $SECRET -o jsonpath='{.data.release}' 2>/dev/null \
          | base64 -d | base64 -d | gzip -d > $DIR/helm-release.json 2>&1 || true
      done
  done

echo "Collecting packages..."
cozyreport_read_object "$REPORT_DIR/kubernetes/packages.txt" kubectl get packages
cozyreport_select_objects "packages" kubectl get packages --no-headers | awk '$3 != "True"' | \
  while read NAME _; do
    DIR=$REPORT_DIR/kubernetes/packages/$NAME
    mkdir -p $DIR
    cozyreport_read_object "$DIR/package.yaml" kubectl get package "$NAME" -o yaml
    cozyreport_read_object "$DIR/describe.txt" kubectl describe package "$NAME"
  done

echo "Collecting packagesources..."
cozyreport_read_object "$REPORT_DIR/kubernetes/packagesources.txt" kubectl get packagesources
cozyreport_select_objects "packagesources" kubectl get packagesources --no-headers | awk '$3 != "True"' | \
  while read NAME _; do
    DIR=$REPORT_DIR/kubernetes/packagesources/$NAME
    mkdir -p $DIR
    cozyreport_read_object "$DIR/packagesource.yaml" kubectl get packagesource "$NAME" -o yaml
    cozyreport_read_object "$DIR/describe.txt" kubectl describe packagesource "$NAME"
  done

echo "Collecting cozystack apps..."
DIR=$REPORT_DIR/cozystack-apps
mkdir -p $DIR
# Two kinds, reached two different ways, and the old loop got both wrong while
# looking like it had them right. It gated all three of its kinds on
# `kubectl get crd <kind>.apps.cozystack.io`, so the section never ran once.
#
# ApplicationDefinition is a real CRD, but in group cozystack.io and cluster-
# scoped -- hence no `-A`, which the old walk passed and which would have put
# `<none>` where a namespace goes.
cozyreport_read_object "$DIR/applicationdefinitions.txt" \
  kubectl get applicationdefinitions.cozystack.io
# Tenants come from the aggregated apps.cozystack.io APIService served by
# cozystack-api, so there is no CRD to ask about; asking is what kept the section
# shut. Read through the bounded reader rather than gated: an aggregated API that
# is down is the state a failed install is in, and it must arrive as kubectl's
# refusal instead of rendering as a cluster with no tenants.
#
# The third kind the old loop named, `applications.apps.cozystack.io`, is not
# collected: that group serves one resource per ApplicationDefinition, discovered
# from the cozyrds, so there is no `applications` resource to read and listing the
# real ones means enumerating the group at runtime.
cozyreport_read_object "$DIR/tenants.txt" \
  kubectl get tenants.apps.cozystack.io -A

echo "Collecting pods..."
cozyreport_read_object "$REPORT_DIR/kubernetes/pods.txt" kubectl get pod -A -o wide
cozyreport_collect_broken_pods "$REPORT_DIR/kubernetes/pods"

echo "Collecting virtualmachines..."
cozyreport_read_object "$REPORT_DIR/kubernetes/vms.txt" kubectl get vm -A
COZYREPORT_OBJECTS_DEADLINE=$(( $(date +%s) + COZYREPORT_OBJECTS_BUDGET_DEFAULT ))
# The selector read is bounded too. The budget below only starts mattering once
# rows arrive, so an apiserver that hangs on the cluster-wide list spends the
# whole collection step without the deadline ever being consulted -- the one
# shape of failure these bounds exist to stop.
# shellcheck disable=SC2086  # empty COZYREPORT_BOUND must vanish, not become ""
cozyreport_select_objects "virtualmachines" kubectl get vm -A --no-headers | cozyreport_kubevirt_not_ready | cozyreport_admit_objects "virtualmachines" |
  while read NAMESPACE NAME _; do
    if cozyreport_objects_deadline_passed; then
      cozyreport_objects_budget_note "virtualmachines"
      break
    fi
    DIR=$REPORT_DIR/kubernetes/vm/$NAMESPACE/$NAME
    mkdir -p $DIR
    cozyreport_read_object "$DIR/vm.yaml" kubectl get vm -n "$NAMESPACE" "$NAME" -o yaml
    cozyreport_read_object "$DIR/describe.txt" kubectl describe vm -n "$NAMESPACE" "$NAME"
  done

echo "Collecting virtualmachine instances..."
cozyreport_read_object "$REPORT_DIR/kubernetes/vmis.txt" kubectl get vmi -A
COZYREPORT_OBJECTS_DEADLINE=$(( $(date +%s) + COZYREPORT_OBJECTS_BUDGET_DEFAULT ))
# The selector read is bounded too. The budget below only starts mattering once
# rows arrive, so an apiserver that hangs on the cluster-wide list spends the
# whole collection step without the deadline ever being consulted -- the one
# shape of failure these bounds exist to stop.
# shellcheck disable=SC2086  # empty COZYREPORT_BOUND must vanish, not become ""
cozyreport_select_objects "virtualmachine instances" kubectl get vmi -A --no-headers | cozyreport_kubevirt_not_ready | cozyreport_admit_objects "virtualmachine instances" |
  while read NAMESPACE NAME _; do
    if cozyreport_objects_deadline_passed; then
      cozyreport_objects_budget_note "virtualmachine instances"
      break
    fi
    DIR=$REPORT_DIR/kubernetes/vmi/$NAMESPACE/$NAME
    mkdir -p $DIR
    cozyreport_read_object "$DIR/vmi.yaml" kubectl get vmi -n "$NAMESPACE" "$NAME" -o yaml
    cozyreport_read_object "$DIR/describe.txt" kubectl describe vmi -n "$NAMESPACE" "$NAME"
  done

echo "Collecting services..."
cozyreport_read_object "$REPORT_DIR/kubernetes/services.txt" kubectl get svc -A
COZYREPORT_OBJECTS_DEADLINE=$(( $(date +%s) + COZYREPORT_OBJECTS_BUDGET_DEFAULT ))
# The selector read is bounded too. The budget below only starts mattering once
# rows arrive, so an apiserver that hangs on the cluster-wide list spends the
# whole collection step without the deadline ever being consulted -- the one
# shape of failure these bounds exist to stop.
# shellcheck disable=SC2086  # empty COZYREPORT_BOUND must vanish, not become ""
cozyreport_select_objects "services" kubectl get svc -A --no-headers | cozyreport_svc_pending | cozyreport_admit_objects "services" |
  while read NAMESPACE NAME _; do
    if cozyreport_objects_deadline_passed; then
      cozyreport_objects_budget_note "services"
      break
    fi
    DIR=$REPORT_DIR/kubernetes/services/$NAMESPACE/$NAME
    mkdir -p $DIR
    # Bounded, like the vm/vmi loops and for a sharper reason: this loop had never
    # executed before the selector above was corrected, because it filtered a
    # column that never carries the value it looked for. Correcting the selector
    # is what makes a hang here possible at all, and the platform ships several
    # LoadBalancer services, so a failed install with no LB provider selects all
    # of them at once. The tarball is written on this file's last line, so a read
    # that never returns costs the whole report rather than this section: the
    # step's outer ceiling ends the step, and there is nothing written yet to
    # upload.
    cozyreport_read_object "$DIR/service.yaml" kubectl get svc -n "$NAMESPACE" "$NAME" -o yaml
    cozyreport_read_object "$DIR/describe.txt" kubectl describe svc -n "$NAMESPACE" "$NAME"
  done

echo "Collecting pvcs..."
cozyreport_read_object "$REPORT_DIR/kubernetes/pvcs.txt" kubectl get pvc -A
cozyreport_select_objects "pvcs" kubectl get pvc -A --no-headers | awk '$3 != "Bound"'  |
  while read NAMESPACE NAME _; do
    DIR=$REPORT_DIR/kubernetes/pvc/$NAMESPACE/$NAME
    mkdir -p $DIR
    cozyreport_read_object "$DIR/pvc.yaml" kubectl get pvc -n "$NAMESPACE" "$NAME" -o yaml
    cozyreport_read_object "$DIR/describe.txt" kubectl describe pvc -n "$NAMESPACE" "$NAME"
  done

# -- objectstorage (COSI) module

if cozyreport_probe "objectstorage (COSI)" kubectl get crd bucketclaims.objectstorage.k8s.io; then
  echo "Collecting objectstorage (COSI) state..."
  DIR=$REPORT_DIR/objectstorage
  mkdir -p $DIR
  # The COSI CRDs ship no printer columns, so plain `kubectl get` shows
  # only NAME/AGE — pull the readiness fields explicitly.
  cozyreport_read_object "$DIR/bucketclaims.txt" \
    kubectl get bucketclaims.objectstorage.k8s.io -A \
    -o custom-columns='NAMESPACE:.metadata.namespace,NAME:.metadata.name,READY:.status.bucketReady,BUCKET:.status.bucketName,CLASS:.spec.bucketClassName'
  cozyreport_read_object "$DIR/bucketaccesses.txt" \
    kubectl get bucketaccesses.objectstorage.k8s.io -A \
    -o custom-columns='NAMESPACE:.metadata.namespace,NAME:.metadata.name,GRANTED:.status.accessGranted,ACCOUNT:.status.accountID,CLAIM:.spec.bucketClaimName'
  cozyreport_read_object "$DIR/buckets.txt" \
    kubectl get buckets.objectstorage.k8s.io \
    -o custom-columns='NAME:.metadata.name,READY:.status.bucketReady,ID:.status.bucketID,CLAIMNS:.spec.bucketClaim.namespace,CLAIM:.spec.bucketClaim.name'
  for kind in bucketclaims.objectstorage.k8s.io bucketaccesses.objectstorage.k8s.io; do
    short=${kind%%.*}
    cozyreport_read_object "$DIR/$short.yaml" kubectl get "$kind" -A -o yaml
  done
  for kind in buckets.objectstorage.k8s.io bucketclasses.objectstorage.k8s.io bucketaccessclasses.objectstorage.k8s.io; do
    short=${kind%%.*}
    cozyreport_read_object "$DIR/$short.yaml" kubectl get "$kind" -o yaml
  done
  if cozyreport_probe "objectstorage controller logs" kubectl get deploy -n cozy-objectstorage-controller container-object-storage-controller; then
    cozyreport_read_object "$DIR/objectstorage-controller.log" \
      kubectl logs -n cozy-objectstorage-controller deploy/container-object-storage-controller --tail=2000
    cozyreport_read_object "$DIR/objectstorage-controller-previous.log" \
      kubectl logs -n cozy-objectstorage-controller deploy/container-object-storage-controller --tail=2000 --previous
  fi
  # seaweedfs COSI provisioners run per seaweedfs instance, one per namespace
  cozyreport_select_objects "objectstorage provisioners" kubectl get deploy -A --no-headers | awk '$2 ~ /objectstorage-provisioner$/ {print $1" "$2}' |
    while read NAMESPACE NAME; do
      cozyreport_read_object "$DIR/provisioner-$NAMESPACE.log" \
        kubectl logs -n "$NAMESPACE" "deploy/$NAME" --all-containers --tail=2000
      cozyreport_read_object "$DIR/provisioner-$NAMESPACE-previous.log" \
        kubectl logs -n "$NAMESPACE" "deploy/$NAME" --all-containers --tail=2000 --previous
    done
fi

# -- kamaji module
#
# Gated on kamaji's own deployment, NOT on cozy-linstor's linstor-controller. The
# condition opening the linstor block below is one copy-paste away and looks right
# here; with it, a cluster without LINSTOR -- a failed install where it never got
# created, or a config with no storage -- gets no kamaji controller log, no
# KamajiControlPlanes and no TenantControlPlanes, with nothing saying why. A
# tenant-kubernetes suite that times out on control-plane bootstrap is exactly the
# run that needs them.
if cozyreport_probe "kamaji" kubectl get deploy -n cozy-kamaji kamaji; then
  echo "Collecting kamaji resources..."
  DIR=$REPORT_DIR/kamaji
  mkdir -p $DIR
  # `--tail`, like every other controller log here. Without it this is the only
  # unbounded-in-SIZE log in the file, which under a wall-clock ceiling makes it
  # the one most likely to be cut off part way -- and a kamaji log cut off part way
  # loses its END, which is where a control-plane bootstrap failure is.
  cozyreport_read_object "$DIR/kamaji-controller.log" \
    kubectl logs -n cozy-kamaji deployment/kamaji --tail=2000
  cozyreport_read_object "$DIR/kamajicontrolplanes.txt" \
    kubectl get kamajicontrolplanes.controlplane.cluster.x-k8s.io -A
  cozyreport_read_object "$DIR/kamajicontrolplanes.yaml" \
    kubectl get kamajicontrolplanes.controlplane.cluster.x-k8s.io -A -o yaml
  cozyreport_read_object "$DIR/tenantcontrolplanes.txt" \
    kubectl get tenantcontrolplanes.kamaji.clastix.io -A
  cozyreport_read_object "$DIR/tenantcontrolplanes.yaml" \
    kubectl get tenantcontrolplanes.kamaji.clastix.io -A -o yaml
fi

# -- linstor module

if cozyreport_probe "linstor" kubectl get deploy -n cozy-linstor linstor-controller; then
  echo "Collecting linstor resources..."
  DIR=$REPORT_DIR/linstor
  mkdir -p $DIR
  # `--container` named explicitly, like the two bundle reads below. Without it
  # kubectl announces which container it defaulted to, on stderr, on every call --
  # so a healthy cluster would leave a READ-WARNINGS.txt here on every single run,
  # and this file argues at length that a marker firing on healthy installs is one
  # a reader learns to skip. Removed at the source rather than filtered afterwards.
  cozyreport_read_exec "$DIR/nodes.txt" \
    kubectl exec -n cozy-linstor deploy/linstor-controller --container=linstor-controller \
    -- linstor --no-color n l
  cozyreport_read_exec "$DIR/storage-pools.txt" \
    kubectl exec -n cozy-linstor deploy/linstor-controller --container=linstor-controller \
    -- linstor --no-color sp l
  cozyreport_read_exec "$DIR/resources.txt" \
    kubectl exec -n cozy-linstor deploy/linstor-controller --container=linstor-controller \
    -- linstor --no-color r l
  # Cluster-wide ErrorReport index (IDs + timestamps + node + category)
  # for fast triage before diving into the per-satellite bundles below.
  cozyreport_read_exec "$DIR/error-reports-index.txt" \
    kubectl exec -n cozy-linstor deploy/linstor-controller --container=linstor-controller \
    -- linstor --no-color error-reports list

  # Controller-side ErrorReports live on the controller pod at
  # /var/log/linstor-controller/ and cover autoplace decisions, RPC
  # errors, and controller-JVM exceptions the index above only
  # references by ID. Bundle them the same way as the satellite ones
  # below so both ends of the storage stack are recoverable offline.
  cozyreport_read_exec "$DIR/controller-error-reports.tgz" \
    kubectl -n cozy-linstor exec deploy/linstor-controller --container=linstor-controller -- sh -c '
    cd /var/log/linstor-controller 2>/dev/null || exit 0
    tar -czf - ErrorReport-*.log 2>/dev/null || true
  '
  # Drop the bundle if empty. `tar -czf - <no-match>` produces a valid
  # 45-byte gzipped empty archive (extracts cleanly, `tar -tzf` exits
  # 0), so a plain readability check keeps that stub in the tree.
  # Require the archive to contain at least one member to keep it.
  if [ ! -s "$DIR/controller-error-reports.tgz" ] || [ -z "$(tar -tzf "$DIR/controller-error-reports.tgz" 2>/dev/null | head -n 1)" ]; then
    rm -f "$DIR/controller-error-reports.tgz"
  fi

  # LINSTOR satellite ErrorReports carry the actual storage-driver error
  # text (the linstor-Satellite log only references them by ID:
  # `ERROR ... Failed to create zfsvolume [Report number 6A4A394E-...]`).
  # crust-gather ships only pod stdout, so without this capture the
  # report body stays on the satellite ephemeral filesystem and is lost
  # when the sandbox is torn down. Copy them off every satellite pod so
  # post-mortem of a `CreateVolume ResourceExhausted` or
  # `Failed to create zfsvolume` retry loop has the concrete cause
  # (out-of-space, dataset conflict, kernel error) instead of just the
  # driver-level symptom.
  DIR=$REPORT_DIR/linstor/error-reports
  mkdir -p "$DIR"
  COZYREPORT_SELECT_NOTE_DIR=$DIR
  for pod in $(cozyreport_select_objects "linstor satellites" kubectl -n cozy-linstor get pods -l app.kubernetes.io/component=linstor-satellite -o name); do
    # Read the node name directly off the pod spec so the tarball key
    # is stable across piraeus DaemonSet regenerations. Fallback to the
    # pod name if .spec.nodeName is not readable for any reason (last
    # resort; collisions between DS pod generations are theoretically
    # possible but the bundle would still land in a distinct file).
    # Bounded with the bare wrapper: the value is read into a variable, not into
    # a file, so there is nothing for a marker to be attached to, and the fallback
    # on the next line is what covers a read that does not answer.
    # shellcheck disable=SC2086  # empty COZYREPORT_BOUND must vanish, not become ""
    node=$($COZYREPORT_BOUND kubectl -n cozy-linstor get "$pod" -o jsonpath='{.spec.nodeName}' 2>/dev/null)
    [ -z "$node" ] && node=$(basename "$pod")
    # Tar the ErrorReport-*.log files into a per-satellite bundle so a
    # burst of retry-loop reports (dozens per incident) does not explode
    # the artefact tree, and a missing directory or empty set never
    # fails the whole cozyreport run.
    cozyreport_read_exec "$DIR/$node.tgz" \
      kubectl -n cozy-linstor exec "$pod" --container=linstor-satellite -- sh -c '
      cd /var/log/linstor-satellite 2>/dev/null || exit 0
      tar -czf - ErrorReport-*.log 2>/dev/null || true
    '
    # Drop the bundle if empty. `tar -czf - <no-match>` yields a valid
    # 45-byte gzipped empty archive that would otherwise slip past a
    # readability check. Require at least one member to keep it. A
    # satellite with zero ErrorReports (healthy case) leaves nothing.
    if [ ! -s "$DIR/$node.tgz" ] || [ -z "$(tar -tzf "$DIR/$node.tgz" 2>/dev/null | head -n 1)" ]; then
      rm -f "$DIR/$node.tgz"
    fi
  done
  COZYREPORT_SELECT_NOTE_DIR=""
  # Drop the empty parent directory when no satellite had reports.
  rmdir "$DIR" 2>/dev/null || true
fi

# -- sandbox-host module

echo "Collecting sandbox host context..."
DIR=$REPORT_DIR/sandbox-host
mkdir -p $DIR
# Bounded although they touch no cluster: `df` blocks indefinitely on a stale NFS
# or wedged fuse mount, and this script writes its archive on its last line.
cozyreport_read_object "$DIR/df.txt" df -h
cozyreport_read_object "$DIR/free.txt" free -m
cozyreport_read_object "$DIR/ps.txt" ps auxww
# Which machine this container is running on. Everything else in this directory
# describes that same host, but describes it as it is now; this says which host it
# is -- which shape, which region, which processor -- and that is the one fact the
# run cannot recover afterwards, because the runner is ephemeral.
cozyreport_collect_runner_identity "$DIR"
# A pipeline, so the bare wrapper rather than the reader: the reader owns the
# redirect and `dmesg | tail` needs its own. dash has no pipefail, so the status
# belongs to `tail` and a cut-off dmesg leaves no marker -- said here rather than
# left to be found, and the 200-line tail is the bound that shapes this file
# anyway.
# shellcheck disable=SC2086  # empty COZYREPORT_BOUND must vanish, not become ""
$COZYREPORT_BOUND dmesg 2>&1 | tail -200 > $DIR/dmesg.txt 2>&1 || true
if [ -f /workspace/talosconfig ]; then
  # Bare wrapper for the same reason as the satellite node name above: this is
  # read into a variable and the documented fallback below covers a read that
  # never answers.
  # shellcheck disable=SC2086  # empty COZYREPORT_BOUND must vanish, not become ""
  NODES=$($COZYREPORT_BOUND kubectl get nodes -o jsonpath='{range .items[*]}{.status.addresses[?(@.type=="InternalIP")].address}{"\n"}{end}' 2>/dev/null)
  for node in ${NODES:-192.168.123.11 192.168.123.12 192.168.123.13}; do
    [ -z "$node" ] && continue
    cozyreport_collect_talos_node /workspace/talosconfig "$node" "$DIR"
  done
fi

# -- finalization

echo "Generating summary..."
"$SCRIPT_DIR/cozyreport-summary.sh" > "$REPORT_DIR/summary.txt" 2>&1 || true

# Fold in the per-test crust-gather snapshots cozytest.sh captured on failure
# (host + each nested tenant cluster) so the uploaded artifact carries an
# inspectable, `crust-gather serve`-able state for every failed test.
SNAP_DIR="${COZY_REPORT_DIR:-_out/cozyreport}/snapshots"
[ -d "$SNAP_DIR" ] && cp -a "$SNAP_DIR" "$REPORT_DIR/snapshots" 2>/dev/null || true

echo "Creating archive..."
tar -czf $REPORT_NAME.tgz -C $REPORT_PDIR .
echo "Report created: $REPORT_NAME.tgz"

echo "Cleaning up..."
rm -rf $REPORT_PDIR
