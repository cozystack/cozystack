#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for the pure selection helper in hack/cozyreport.sh that decides
# which pods the report collects evidence for, and which ones
# hack/cozyreport-summary.sh names as broken:
#
#   - cozyreport_pods_not_ready -- keep every pod that is neither finished nor
#                                  fully ready by its READY column. That column
#                                  counts ready containers, not the pod's Ready
#                                  condition; the one case the two disagree on is
#                                  pinned below rather than claimed away.
#
# The kubectl calls those two scripts wrap around this helper are not
# unit-testable (they need a live cluster); the selection is, and it is the part
# that decides whether the pod that caused a failure appears in the uploaded
# report at all. The case that matters is a pod whose STATUS is Running while its
# READY column is 0/1: a readiness probe that never passes blocks everything
# downstream of it, and a STATUS-only filter drops it, so the artifact ends up
# with no trace of the pod the run died on.
#
# Strategy: source hack/cozyreport.sh with COZYREPORT_LIB set, which its
# sourcing guard honours by defining the helpers and returning before it starts a
# report -- so no cluster is required. Each @test then feeds the helper the exact
# text `kubectl get pod -A --no-headers` produces.
#
# Title syntax constraints (inherited from cozytest.sh's awk parser):
#   - Titles delimited by ASCII double quotes; embedded quotes truncate.
#   - Only [A-Za-z0-9] from the title survives into the function name, so keep
#     titles distinctive in their alphanumeric run.
#
# Run with: hack/cozytest.sh hack/cozyreport.bats
#           (or `bats hack/cozyreport.bats` if the bats binary is installed;
#           cozytest.sh is the CI path.)
# -----------------------------------------------------------------------------

HACK_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")" && pwd)"
SCRIPT="$HACK_DIR/cozyreport.sh"

COZYREPORT_LIB=1
# shellcheck source=/dev/null
. "$SCRIPT"

# One realistic cluster snapshot reused across the tests below: a running-but-
# unready replica, a healthy pod, a finished job, a multi-container pod that is
# partially ready, and the phase-based cases that were already collected.
SNAPSHOT="$(printf '%s\n' \
  'cozy-keycloak  keycloak-db-2    0/1  Running            0  6m' \
  'kube-system    coredns-abc      1/1  Running            0  2h' \
  'tenant-test    migrate-job-x    0/1  Completed          0  1h' \
  'tenant-test    backup-job-y     0/1  Succeeded          0  1h' \
  'cozy-system    puller-z         0/1  ImagePullBackOff   0  4m' \
  'cozy-system    unscheduled-w    0/1  Pending            0  4m' \
  'cozy-linstor   satellite-v      2/3  Running            1  3h')"

# Print a file with comment lines dropped and shell line continuations folded, so
# a guard can match a COMMAND rather than a physical line. Every source-scanning
# check below wants the former and, written the obvious way, gets the latter: a
# pattern anchored to one line passes the moment the thing it looks for sits one
# continuation down, which in shell is where it usually sits.
#
# Top level rather than inside a test, because cozytest.sh ends an @test at the
# first bare `}`.
# One spelling of "kubectl, however POSIX lets it be reached", shared by every
# scanner below so widening it is a single edit rather than four. `command` and
# `env` may prefix it, `env` may carry assignments, and any whitespace run
# separates the parts.
#
# What this deliberately does NOT cover: an absolute path, a variable, `sudo`, or
# a shell function wrapping the name. None appears in these files and a separate
# check fails if one ever does, so the limit is enforced rather than assumed.
KUBECTL_RE='(command[[:space:]]+|env[[:space:]]+([A-Za-z_][A-Za-z0-9_]*=[^[:space:]]*[[:space:]]+)*)*kubectl[[:space:]]'

fold_source() {
  grep -v '^[[:space:]]*#' "$1" \
    | awk '{ line = line $0
             if (line ~ /\\$/) { sub(/\\$/, " ", line); next }
             print line; line = "" }
           END { if (line != "") print line }'
}

@test "the kamaji and linstor modules are gated on their own deployments" {
  # The kamaji module was gated on cozy-linstor's controller, byte-identical to the
  # condition opening the linstor module below it, so on a cluster without LINSTOR
  # the kamaji controller log and the TenantControlPlanes were absent with nothing
  # saying why -- and a tenant-kubernetes suite that times out on control-plane
  # bootstrap is exactly the run that needs them. A copy-pasted gate is invisible
  # to a reader precisely because it looks like its neighbour.
  # Matched as a namespace/name PAIR. `*kamaji*` alone is satisfied by the
  # `-n cozy-kamaji` that any gate in this module carries, so a gate naming a
  # deployment that does not exist in that namespace -- which would keep the module
  # shut forever -- passed unnoticed.
  # Matched as "the gate line", not as "a line starting with kubectl": the gates
  # run through cozyreport_probe, so the read no longer begins the line and a
  # pattern anchored on it finds nothing and fails on the empty result rather
  # than on a wrong gate.
  gate=$(awk '/^# -- kamaji module/,/^  echo "Collecting kamaji/' "$SCRIPT" | grep -E '^if .*kubectl get')
  case "$gate" in
    *"cozy-kamaji kamaji"*) ;;
    *) echo "FAIL: the kamaji module is not gated on cozy-kamaji/kamaji: $gate"; false ;;
  esac
  gate=$(awk '/^# -- linstor module/,/^  echo "Collecting linstor/' "$SCRIPT" | grep -E '^if .*kubectl get')
  case "$gate" in
    *"cozy-linstor linstor-controller"*) ;;
    *) echo "FAIL: the linstor module is not gated on cozy-linstor/linstor-controller: $gate"; false ;;
  esac
}

@test "CSR capture lists and reads every request on the host cluster and each tenant kubeconfig on disk" {
  host=$(fold_source "$SCRIPT" | awk '/^echo "Collecting CertificateSigningRequests/,/^echo "Collecting tenant kubernetes CertificateSigningRequests/')
  [ -n "$host" ] || { echo "FAIL: could not locate the host CSR walk"; false; }
  printf '%s\n' "$host" | grep -q '^cozyreport_read_object "\$REPORT_DIR/kubernetes/csr.txt" kubectl get csr -o wide$' || {
    echo "FAIL: the host CSR listing is not a bounded read of every request"; false; }
  printf '%s\n' "$host" | grep -q '^cozyreport_select_objects "csr" kubectl get csr --no-headers |$' || {
    echo "FAIL: the host CSR walk does not select through the bounded selector"; false; }
  printf '%s\n' "$host" | grep -q '^    cozyreport_read_object "\$DIR/csr.yaml" kubectl get csr "\$NAME" -o yaml$' || {
    echo "FAIL: the host CSR walk does not read each request's yaml through the bounded reader"; false; }

  tenant=$(fold_source "$SCRIPT" | awk '/^echo "Collecting tenant kubernetes CertificateSigningRequests/,/^done$/')
  [ -n "$tenant" ] || { echo "FAIL: could not locate the tenant CSR walk"; false; }
  printf '%s\n' "$tenant" | grep -q '^for TENANT_KC in tenantkubeconfig-\*; do$' || {
    echo "FAIL: the tenant CSR walk does not glob every leftover tenant kubeconfig"; false; }
  printf '%s\n' "$tenant" | grep -q '^  \[ -f "\$TENANT_KC" \] || continue$' || {
    echo "FAIL: the tenant CSR walk does not skip a glob match with no matching file"; false; }
  printf '%s\n' "$tenant" | grep -q '^  cozyreport_read_object "\$DIR/csr.txt" kubectl --kubeconfig "\$TENANT_KC" get csr -o wide$' || {
    echo "FAIL: the tenant CSR listing is not a bounded read of every request"; false; }
  printf '%s\n' "$tenant" | grep -q '^  cozyreport_select_objects "tenant csr (\$TENANT_NAME)" kubectl --kubeconfig "\$TENANT_KC" get csr --no-headers |$' || {
    echo "FAIL: the tenant CSR walk does not select through the bounded selector"; false; }
  printf '%s\n' "$tenant" | grep -q '^      cozyreport_read_object "\$DIR/\$NAME.yaml" kubectl --kubeconfig "\$TENANT_KC" get csr "\$NAME" -o yaml$' || {
    echo "FAIL: the tenant CSR walk does not read each request's yaml through the bounded reader"; false; }
}

@test "no tenant kubeconfig on disk means no tenant CSR read is attempted" {
  # tenantkubeconfig-<test> only exists while a kubernetes-* Chainsaw test is
  # running or failed before its own cleanup (hack/e2e-chainsaw/_lib/run-
  # kubernetes.sh). Every other suite, and a kubernetes suite that passed,
  # leaves cwd with no such file, and the glob must not turn that absence into
  # a literal `--kubeconfig tenantkubeconfig-*` read.
  tmp=$(mktemp -d)
  mkdir -p "$tmp/bin" "$tmp/work"
  cat > "$tmp/bin/kubectl" <<'STUB'
#!/bin/sh
echo "kubectl $*" >> "$KUBECTL_CALL_LOG"
exit 0
STUB
  chmod +x "$tmp/bin/kubectl"

  body=$(fold_source "$SCRIPT" | awk '/^for TENANT_KC in tenantkubeconfig-\*; do$/,/^done$/')
  [ -n "$body" ] || { echo "FAIL: could not locate the tenant CSR walk"; false; }

  KUBECTL_CALL_LOG="$tmp/calls.txt"
  : > "$KUBECTL_CALL_LOG"
  REPORT_DIR="$tmp/report"
  export KUBECTL_CALL_LOG REPORT_DIR
  ( cd "$tmp/work" && PATH="$tmp/bin:$PATH" eval "$body" )

  [ ! -s "$KUBECTL_CALL_LOG" ] || {
    echo "FAIL: kubectl was invoked with no tenant kubeconfig present on disk"
    cat "$KUBECTL_CALL_LOG"
    false
  }
  [ ! -e "$REPORT_DIR" ] || {
    echo "FAIL: a report directory was created with nothing to walk"
    false
  }
  rm -rf "$tmp"
}

@test "every Deployment this script names by hand is one somebody chose to name" {
  # Pinning the two gates above only guards the two lines that were looked at.
  # The same defect sat in the cozystack module the whole time: the image read
  # asked for `cozy-system/cozystack`, a Deployment the installer stopped shipping
  # when it moved to cozystack-operator, so `cozystack/image.txt` -- the first file
  # of the report, the one naming the build that produced everything after it --
  # held `deployments.apps "cozystack" not found` on every run. The line below it
  # already asked for the right name, which is what makes this class invisible in
  # review: the wrong name looks exactly like its correct neighbour.
  #
  # Frozen as a LIST rather than checked against the repo's manifests, because
  # most of these Deployments come from upstream subcharts and exist nowhere in
  # this tree as a literal. What the list buys is that renaming or adding one has
  # to be typed here too, by someone who then has a reason to check it resolves.
  #
  # Literal names only. The flux and per-object loops build a name from the
  # cluster's own output at runtime; there is nothing to pin and nothing to get
  # stale.
  # A trailing `;` is stripped from every name. The gates used to end in
  # `>/dev/null 2>&1; then`, which put the shell separator on a field of its own;
  # through cozyreport_probe the deployment name is the last word before it, so
  # without this the same name appears twice, once as itself and once with the
  # semicolon attached, and the guard reports a set that changed when nothing did.
  found=$(awk '
    /kubectl/ {
      ns = ""
      for (i = 1; i < NF; i++) if ($i == "-n") { ns = $(i + 1); break }
      if (ns == "") next
      for (i = 1; i <= NF; i++) {
        if ($i ~ /^deploy\//) { n = $i; sub(/^deploy\//, "", n); sub(/;$/, "", n); print ns "/" n }
        else if ($i == "deploy" && i < NF && $(i + 1) == "-n" && i + 2 < NF && $(i + 3) !~ /^-/) {
          n = $(i + 3); sub(/;$/, "", n); print ns "/" n
        }
      }
    }
  ' "$SCRIPT" | grep -v '\$' | LC_ALL=C sort -u | tr '\n' ' ')

  expected='cozy-cert-manager/cert-manager cozy-cert-manager/cert-manager-webhook cozy-kamaji/kamaji cozy-linstor/linstor-controller cozy-objectstorage-controller/container-object-storage-controller cozy-system/cozystack-operator '

  if [ "$found" != "$expected" ]; then
    echo "FAIL: the set of Deployments this script names changed."
    echo "A name that exists on no cluster does not fail loudly -- it writes"
    echo "kubectl's NotFound into the file that was supposed to hold evidence,"
    echo "or holds a module shut for the whole run."
    echo "expected: $expected"
    echo "found:    $found"
    false
  fi
}

@test "every Kubernetes kind these scripts name by hand is one somebody chose it" {
  # A gate naming something that is not a CRD does not fail loudly, and neither
  # does a mistyped kind in a list. The cozystack-apps section gated on `kubectl get crd
  # applications.apps.cozystack.io`. apps.cozystack.io is an aggregated
  # APIService, not a CRD, so the probe answered NotFound on every cluster and the
  # section never ran once; the group serves one resource per ApplicationDefinition
  # discovered from the cozyrds, so `applications` was not a resource in it under
  # any gate, and the real ApplicationDefinition CRD is in group cozystack.io and
  # cluster-scoped. Three wrong facts in one loop, none of which failed loudly --
  # the section was simply skipped, for years, exactly like a cluster that has no
  # such objects. It is deleted rather than repaired: collecting the cozyrd-derived
  # kinds means enumerating the API group at runtime, which is a feature and not a
  # correction.
  #
  # Frozen as a list for the same reason the Deployment names are: cert-manager's
  # and COSI's CRDs come from upstream charts and appear nowhere in this tree, so
  # checking against the manifests would be false-red for them and false-green for
  # anything typed wrong. What the list buys is that adding a gate has to be typed
  # here too, by someone who then has a reason to check the kind exists and is
  # served as a CRD at all.
  # Every spelling, not just the gates. `summary_crd "$kind"` inside a
  # `for kind in ...` loop passes four Flux CRDs that a grep for literal arguments
  # never sees, and the COSI block iterates five more. A typo anywhere in those
  # lists is as quiet as a wrong gate -- the kind is simply never found and the
  # tree beneath it is never written -- so the union is what gets frozen.
  # Comments stripped first. Prose names these kinds too -- this very test's
  # rationale does -- and a guard that reads them is answering "where is this
  # string mentioned" while reading as "what does this script ask the cluster for".
  code=$(mktemp)
  { fold_source "$SCRIPT"; fold_source "$HACK_DIR/cozyreport-summary.sh"; } > "$code"
  found=$(
    {
      # Whitespace matched as a RUN, not as one space: folding a continuation
      # leaves several spaces where the backslash was, and a pattern demanding
      # exactly one is evaded by the very construct the folding exists to expose.
      grep -oE 'get crd[[:space:]]+[a-z][a-z0-9.-]+' "$code" | sed 's/^get crd[[:space:]]*//'
      grep -oE 'summary_crd[[:space:]]+[a-z][a-z0-9.-]+' "$code" | sed 's/^summary_crd[[:space:]]*//'
      grep -E '^[[:space:]]*for kind in ' "$code" |
        sed -e 's/^[[:space:]]*for kind in //' -e 's/;.*//' | tr ' ' '\n'
      # And the kinds read directly, with no gate at all: a fully-qualified name
      # is exactly as silent when mistyped, and dropping the gate is now one of
      # the deliberate choices here rather than an oversight.
      grep -oE 'kubectl get[[:space:]]+[a-z][a-z0-9-]*\.[a-z0-9.-]+' "$code" | sed 's/^kubectl get[[:space:]]*//'
    } | grep -E '^[a-z][a-z0-9-]*\.[a-z0-9.-]+$' | LC_ALL=C sort -u | tr '\n' ' '
  )
  rm -f "$code"
  expected='applicationdefinitions.cozystack.io bucketaccessclasses.objectstorage.k8s.io bucketaccesses.objectstorage.k8s.io bucketclaims.objectstorage.k8s.io bucketclasses.objectstorage.k8s.io buckets.objectstorage.k8s.io certificaterequests.cert-manager.io certificates.cert-manager.io challenges.acme.cert-manager.io externalartifacts.source.toolkit.fluxcd.io gitrepositories.source.toolkit.fluxcd.io helmreleases.helm.toolkit.fluxcd.io helmrepositories.source.toolkit.fluxcd.io kamajicontrolplanes.controlplane.cluster.x-k8s.io ocirepositories.source.toolkit.fluxcd.io orders.acme.cert-manager.io tenantcontrolplanes.kamaji.clastix.io tenants.apps.cozystack.io '

  if [ "$found" != "$expected" ]; then
    echo "FAIL: the set of CRDs these scripts gate on changed."
    echo "A gate naming something that is not a CRD does not fail loudly --"
    echo "it holds its section shut and reads as a cluster with no such objects."
    echo "expected: $expected"
    echo "found:    $found"
    false
  fi
}

@test "no explanation in these files stops in the middle of its sentence" {
  # A comment that ends mid-sentence is what an edit leaves behind when it cuts
  # prose out of a block without rereading the seam: "Same guard, same reason, as"
  # followed by a blank line, or a clause with its own middle removed. In files
  # whose subject is that an explanation has to be trustworthy, a half-written
  # explanation in the source is the same defect one level up, and it is the kind
  # a person skims straight past.
  #
  # Checks the LAST line of each comment block only: a conjunction mid-block is
  # just a wrapped sentence.
  bad=$(for f in cozyreport.sh cozyreport-summary.sh e2e-capture-previous-logs.sh \
                 cozyreport.bats capture-previous-logs.bats cozyreport-talos.bats; do
    awk -v f="$f" '
      function flush(   x) {
        if (prev != "" && prev ~ /([ \t](as|and|the|of|for|to|a|an|is|was|by|with|that|than|because|rather|same|but|or|in|on|at|from|its|it)|,)$/)
          printf "%s:%d: %s\n", f, prevn, prev
        prev = ""
      }
      {
        line = $0
        sub(/^[[:space:]]*/, "", line)
        if (line ~ /^#/) {
          sub(/^#+[[:space:]]*/, "", line)
          if (line != "") { prev = line; prevn = NR; next }
        }
        flush()
      }
      END { flush() }
    ' "$HACK_DIR/$f"
  done)

  if [ -n "$bad" ]; then
    echo "FAIL: a comment block ends mid-sentence:"
    printf '%s\n' "$bad"
    false
  fi
}

@test "every knob a script reads is named in that script's Environment block" {
  # Same shape as the marker-parity guard above, for the other thing that drifts:
  # behaviour gets added to a knob and its header block stays as it was. That is
  # how COZY_PREVLOG_TAIL ended up validated, given a special -1 meaning and a
  # per-log note while its documentation still read "lines per container
  # (default 200)" -- and a knob whose only description is its default is one an
  # operator will set to something the script now rejects.
  #
  # Read from the script rather than listed here: a hand-kept list is the thing
  # that goes stale, which is the defect being guarded against.
  # All three scripts, not the two that happened to have a block already: the
  # test's name says "a script", and a guard whose file list is narrower than its
  # claim is the same defect as a marker regex that enumerates known prefixes.
  # cozyreport-summary.sh reads COZY_REPORT_PODS_MAX and had no block at all.
  for f in cozyreport.sh cozyreport-summary.sh e2e-capture-previous-logs.sh; do
    block=$(awk '/^# Environment:/,/^[^#]/' "$HACK_DIR/$f")
    [ -n "$block" ] || { echo "FAIL: $f has no Environment block"; false; }
    # Knobs reached through a sourced helper count too. Grepping only the file's
    # own text answers "every knob whose name appears here", not "every knob this
    # script reads" -- the claim in this test's name. COZY_REPORT_PREFER_NS
    # reordered the summary's pod listing while appearing nowhere in that file,
    # and this guard could not see it. Same defect the marker guard had: the check
    # was narrower than the sentence describing it.
    #
    # Only the LIBRARY portion of the sourced file counts -- everything above the
    # COZYREPORT_LIB guard, which is where the `.` returns. Scanning the whole
    # file would attribute knobs the sourcing script never reaches: cozyreport.sh
    # reads COZY_REPORT_DIR in its report body, far below that return, and the
    # summary never executes it. A guard that over-reports is retired as noise
    # just as fast as one that under-reports.
    #
    # Comments stripped before matching: every one of these files documents its
    # knobs in prose right above the code, so scanning raw text finds the name in
    # the Environment block and reports the knob as "read" by a file that only
    # describes it. Three guards in this file need the same strip for the same
    # reason: prose that explains a rule names the thing the rule forbids.
    knobs=$(grep -Ev '^[[:space:]]*#' "$HACK_DIR/$f" | grep -oE 'COZY_[A-Z_]+' || true)
    if grep -q '^\. "\$SCRIPT_DIR/cozyreport.sh"' "$HACK_DIR/$f"; then
      # Only the knobs read by helpers this file actually CALLS. Sourcing makes
      # every helper available, but availability is not use: the summary sources
      # cozyreport_pod_tail and never invokes it, so attributing COZY_REPORT_POD_TAIL
      # to it would demand documentation for a knob that changes nothing here. A
      # guard that over-reports gets retired as noise exactly as fast as one that
      # under-reports, so this walks call sites rather than the whole library.
      for fn in $(grep -oE 'cozyreport_[a-z_]+' "$HACK_DIR/$f" | sort -u); do
        body=$(awk -v fn="$fn" '$0 ~ "^"fn"\\(\\) \\{" {inb=1} inb {print} inb && /^}$/ {exit}' "$HACK_DIR/cozyreport.sh" | grep -Ev '^[[:space:]]*#')
        knobs="$knobs
$(printf '%s\n' "$body" | grep -oE 'COZY_[A-Z_]+' || true)"
      done
    fi
    for v in $(printf '%s\n' "$knobs" | sort -u); do
      if ! printf '%s\n' "$block" | grep -q "$v"; then
        echo "FAIL: $f reads $v but its Environment block never names it"
        false
      fi
    done
  done
}

@test "an exec is bounded through its own reader and classified by neither" {
  # The cut-off classification rests on 124 meaning the deadline expired. That
  # holds only because `timeout` passes a command's own status through and no
  # command given to the wrapper returns 124: `get`, `logs` and `describe` do not.
  # `kubectl exec` does whatever the remote command did, 124 included, and would
  # turn a container's own exit status into "this read was cut off by its own 30s
  # timeout" -- a sentence about the report's machine describing something that
  # happened inside a pod.
  #
  # Three assertions, because the earlier one-line form was satisfied by leaving
  # every exec unbounded, and that is the state this file was in: the sharpest
  # reads in it -- a shell inside a pod, streaming a tarball -- had no ceiling of
  # any kind, and the guard read as though that were the safe arrangement.
  # Bounding them without the middle assertion is worse than leaving them alone,
  # because a 124 would then be classified as this script's own deadline.
  #
  # Line continuations are folded first. Every form below routinely puts the argv
  # on the next physical line, so matching per line asks whether the word `exec`
  # shares a line with the call rather than whether it is in its arguments -- and
  # passes while the exec sits one line down, which is where it would actually be.
  code=$(fold_source "$SCRIPT")

  # 1. No exec reaches the readers whose 124 means "our deadline fired".
  # Every reader that reaches cozyreport_cutoff_desc, not just the two that were
  # thought of first: cozyreport_probe and cozyreport_select_objects call it too,
  # so an exec routed through either would be misclassified AND invisible here.
  # The guard's reach has to equal the helper's, or it reports a clean sweep of
  # the half it happens to look at.
  bounded=$(printf '%s\n' "$code" | grep -E '\$COZYREPORT_BOUND[[:space:]]|cozyreport_(read_object|probe|select_objects)[[:space:]]' || true)
  [ -n "$bounded" ] || { echo "FAIL: found no bounded reads at all"; false; }
  if printf '%s\n' "$bounded" | grep -qE "[[:space:]]exec[[:space:]]"; then
    echo "FAIL: a bounded read is an exec, whose exit status is the remote command's"
    printf '%s\n' "$bounded" | grep -E "[[:space:]]exec[[:space:]]"
    false
  fi

  # 2. And every exec IS bounded, through the reader written for one. Matched on
  # the verb as a field rather than on the string `kubectl exec`: half of these
  # calls spell it `kubectl -n cozy-linstor exec`, which that string never sees,
  # and a guard blind to half its subject reports a clean sweep of the other half.
  #
  # The reader's own body is cut out first. It is the one place in this file
  # whose subject IS the exec -- in its code and in the sentence it writes into
  # the report -- so scanning it answers "where is an exec mentioned" while
  # reading as "which reads are execs", and every future edit to that sentence
  # fails this guard for a reason unrelated to it. Its boundedness is asserted
  # on its own terms below instead of inferred from the scan.
  sites=$(printf '%s\n' "$code" |
    awk '/^cozyreport_read_exec\(\) \{/ {inb=1} !inb {print} inb && /^}$/ {inb=0}')
  execs=$(printf '%s\n' "$sites" | grep -E "$KUBECTL_RE" | grep -E "[[:space:]]exec[[:space:]]" || true)
  [ -n "$execs" ] || { echo "FAIL: found no exec reads at all, so this proves nothing"; false; }
  unbounded=$(printf '%s\n' "$execs" | grep -v 'cozyreport_read_exec' || true)
  if [ -n "$unbounded" ]; then
    echo "FAIL: an exec read runs with no ceiling; a wedged pod holds it open forever"
    printf '%s\n' "$unbounded"
    false
  fi

  # 3. That reader states the ambiguity rather than resolving it. Reaching the
  # shared describer from in there is the whole defect wearing a different call
  # site: the sentence it returns names this script's timeout as the cause.
  body=$(awk '/^cozyreport_read_exec\(\) \{/ {inb=1} inb {print} inb && /^}$/ {exit}' "$SCRIPT" |
    grep -Ev '^[[:space:]]*#')
  [ -n "$body" ] || { echo "FAIL: could not locate cozyreport_read_exec"; false; }
  # Cutting its body out of the scan above costs the one assertion that scan
  # would have made about it, so make that one here: the reader every exec now
  # routes through has to be the thing that applies the ceiling.
  printf '%s\n' "$body" | grep -q '\$COZYREPORT_BOUND' || {
    echo "FAIL: the exec reader does not apply the wall-clock bound"
    false
  }
  if printf '%s\n' "$body" | grep -q 'cozyreport_cutoff_desc'; then
    echo "FAIL: the exec reader describes its cutoff with the helper written for reads whose 124 is unambiguous"
    false
  fi
}

# Stub PATH for the exec reader: a `kubectl` whose behaviour is chosen by
# STUB_EXEC_MODE and a `timeout` that runs its command rather than timing it, so
# the assertions do not depend on the host having GNU coreutils.
exec_stub_dir() {
  _sd=$1
  mkdir -p "$_sd/bin" "$_sd/out"
  cat > "$_sd/bin/kubectl" <<'STUB'
#!/bin/sh
case "${STUB_EXEC_MODE:-}" in
  # Killed mid-stream: the last line has no terminating newline, which is what a
  # stream cut short actually leaves behind.
  cut)     printf 'Node | ONLINE\nsrv1 | partial ro'; exit 124 ;;
  # Killed before the command inside the pod wrote a byte -- the wedged controller
  # the ticket is about. No stdout AND no stderr, because there was nothing to say
  # it with.
  silentcut) exit 124 ;;
  # Rows out, then a failure on the command's own terms rather than on the clock.
  # A wedged controller does not have to produce 124.
  partialfail) printf 'Node | Ready\nsrv1 | Yes\nsrv2 | Ye'
               echo 'error: rpc failed' >&2; exit 1 ;;
  refused) echo 'error: unable to upgrade connection: container not found' >&2; exit 1 ;;
  # What `kubectl exec` really leaves behind when the remote command fails: the
  # cause first, and its own `command terminated` line appended LAST.
  linstor) echo 'ERROR: unable to connect to linstor://localhost:3370' >&2
           echo 'command terminated with exit code 10' >&2; exit 10 ;;
  *)       echo 'Defaulted container "linstor-controller" out of: linstor-controller, init' >&2
           printf 'Node | ONLINE\nsrv1 | Yes\n' ;;
esac
STUB
  chmod +x "$_sd/bin/kubectl"
  cat > "$_sd/bin/timeout" <<'STUB'
#!/bin/sh
[ "$1" = "-k" ] && shift 2
shift
exec "$@"
STUB
  chmod +x "$_sd/bin/timeout"
  COZYREPORT_BOUND="timeout -k 5 $COZYREPORT_READ_TIMEOUT"
}

@test "an exec cut off short says what its status cannot tell apart" {
  tmp=$(mktemp -d)
  exec_stub_dir "$tmp"

  STUB_EXEC_MODE=cut PATH="$tmp/bin:$PATH" \
    cozyreport_read_exec "$tmp/out/nodes.txt" kubectl exec -n ns deploy/c -- linstor n l

  # The partial output is kept, never discarded: it is the only copy of what the
  # read managed to get out of the pod.
  grep -q 'partial ro' "$tmp/out/nodes.txt"
  # And the table says so about ITSELF, like every other .txt in the tarball. A
  # short node list with the explanation one directory up reads as a small
  # cluster, and that walk is the one a triager does not make.
  grep -q '^# \[cozyreport\] TRUNCATED' "$tmp/out/nodes.txt" || {
    echo "FAIL: a cut-off table carries no marker of its own, or not in the shared form"
    cat "$tmp/out/nodes.txt"
    false
  }
  [ -f "$tmp/out/COLLECTION-FAILED.txt" ] || {
    echo "FAIL: a cut-off exec left no note at all"; false; }
  grep -q 'nodes.txt' "$tmp/out/COLLECTION-FAILED.txt"
  grep -q 'passes the remote command' "$tmp/out/COLLECTION-FAILED.txt"
  # And it does NOT borrow the sentence written for reads whose 124 is this
  # script's own clock. That sentence is the false claim this path exists to
  # avoid, so its absence is the assertion, not a nicety.
  if grep -q "its own ${COZYREPORT_READ_TIMEOUT}s timeout" "$tmp/out/COLLECTION-FAILED.txt"; then
    echo "FAIL: the note names a mechanism the status does not establish"
    cat "$tmp/out/COLLECTION-FAILED.txt"
    false
  fi

  # A refusal is a different fact and gets a different sentence. Pasting the
  # ambiguity onto every failure would make the wording noise a reader skips,
  # which costs exactly the case it was written for.
  STUB_EXEC_MODE=refused PATH="$tmp/bin:$PATH" \
    cozyreport_read_exec "$tmp/out/pools.txt" kubectl exec -n ns deploy/c -- linstor sp l
  grep -q 'container not found' "$tmp/out/COLLECTION-FAILED.txt"
  [ "$(grep -c 'passes the remote command' "$tmp/out/COLLECTION-FAILED.txt")" -eq 1 ]

  # A refusal is what the LISTING has to say for itself, so it goes in the file
  # too. Sent only beside it, `pools.txt` is zero bytes -- the "did it return
  # nothing, or never run" question this collector exists to answer, and the shape
  # the object reader argues against for exactly the same reason.
  [ -s "$tmp/out/pools.txt" ] || {
    echo "FAIL: a refused listing was left as a zero-byte file"; false; }
  grep -q 'container not found' "$tmp/out/pools.txt"

  # And the WHOLE message survives, not its last line. `kubectl exec` appends
  # `command terminated with exit code N` after whatever the remote command said,
  # so on this path the last line is the one line that never carries the cause --
  # the opposite of the `kubectl get` klog preamble this file quotes tail -n 1 for.
  STUB_EXEC_MODE=linstor PATH="$tmp/bin:$PATH" \
    cozyreport_read_exec "$tmp/out/res.txt" kubectl exec -n ns deploy/c -- linstor r l
  grep -q 'unable to connect to linstor' "$tmp/out/COLLECTION-FAILED.txt" || {
    echo "FAIL: only kubectl's own trailer survived; the cause was dropped"
    grep 'res.txt' -A3 "$tmp/out/COLLECTION-FAILED.txt"
    false
  }
  grep -q 'unable to connect to linstor' "$tmp/out/res.txt"

  # The archive sentence belongs only to the two reads that stream one. A cut-off
  # .txt that carried it would be told about a mechanism its read never went
  # through -- the same fault as naming a timeout that did not fire.
  STUB_EXEC_MODE=cut PATH="$tmp/bin:$PATH" \
    cozyreport_read_exec "$tmp/out/idx.txt" kubectl exec -n ns deploy/c -- linstor error-reports list
  if grep -q 'idx.txt' "$tmp/out/COLLECTION-FAILED.txt" &&
     grep -A2 'idx.txt' "$tmp/out/COLLECTION-FAILED.txt" | grep -q 'EITHER beside it'; then
    echo "FAIL: a cut-off listing was described as an archive"
    false
  fi
  rm -rf "$tmp"
}

@test "both readers mark the same file type in the same greppable form" {
  # Nothing held this and the two readers had drifted: the object reader wrote
  # `# [cozyreport]` into a .txt and the exec reader wrote it bare. A triager greps
  # `^# [cozyreport]` over the unpacked tarball, gets part of it, and has no way to
  # know the rest exists.
  #
  # BOTH outcomes, because the first version of this test checked only the path
  # where the command produced output -- and the three zero-output branches drifted
  # underneath it, in the same commit that stated the rule. Zero output is the
  # ticket's own case: a wedged controller killed before it writes a byte.
  tmp=$(mktemp -d)
  exec_stub_dir "$tmp"
  printf '#!/bin/sh\nprintf "row one\\nrow tw"\nexit 1\n' > "$tmp/objstub"
  chmod +x "$tmp/objstub"

  STUB_EXEC_MODE=cut PATH="$tmp/bin:$PATH" \
    cozyreport_read_exec "$tmp/out/exec-partial.txt" kubectl exec -n ns deploy/c -- linstor n l
  STUB_EXEC_MODE=silentcut PATH="$tmp/bin:$PATH" \
    cozyreport_read_exec "$tmp/out/exec-empty.txt" kubectl exec -n ns deploy/c -- linstor n l
  COZYREPORT_BOUND="" cozyreport_read_object "$tmp/out/object-partial.txt" "$tmp/objstub"

  for f in exec-partial.txt exec-empty.txt object-partial.txt; do
    [ -s "$tmp/out/$f" ] || { echo "FAIL: $f is empty"; false; }
    grep -q '^# \[cozyreport\]' "$tmp/out/$f" || {
      echo "FAIL: $f does not carry the shared marker form"
      tail -n 2 "$tmp/out/$f"
      false
    }
  done
  # And no `[cozyreport]` line anywhere in them escapes the form.
  for f in exec-partial.txt exec-empty.txt object-partial.txt; do
    if grep -q '^\[cozyreport\]' "$tmp/out/$f"; then
      echo "FAIL: $f carries an unprefixed [cozyreport] line"
      grep -n '^\[cozyreport\]' "$tmp/out/$f"
      false
    fi
  done
  rm -rf "$tmp"
}

@test "the satellite walk points the selector at its own directory" {
  # The behaviour is parameterised and the parameterisation is tested -- but the
  # CALL SITE that sets it was held by nothing: deleting the assignment left every
  # test green while satellite notes went back to the shared kubernetes file. That
  # is the gap two other guards in this file exist for, and neither can see this
  # one, because what is missing is a variable rather than a function.
  body=$(fold_source "$SCRIPT" | awk '/^  DIR=\$REPORT_DIR\/linstor\/error-reports$/,/^  rmdir /')
  [ -n "$body" ] || { echo "FAIL: could not locate the satellite walk"; false; }
  printf '%s\n' "$body" | grep -q '^  COZYREPORT_SELECT_NOTE_DIR=\$DIR$' || {
    echo "FAIL: the satellite walk does not point the selector at its own directory"
    printf '%s\n' "$body" | head -5
    false
  }
  printf '%s\n' "$body" | grep -q '^  COZYREPORT_SELECT_NOTE_DIR=""$' || {
    echo "FAIL: the satellite walk leaves the note directory set for the next caller"
    false
  }
}

@test "a table that died part way through says so in the table itself" {
  # Not every wedged controller produces 124. One that streams three rows and then
  # exits 1 leaves a file that reads as a complete three-node listing, and the note
  # a directory up is the walk a triager does not make -- the same argument the
  # timeout branch already acts on, and the same one the object reader acts on for
  # this exact shape.
  tmp=$(mktemp -d)
  exec_stub_dir "$tmp"

  STUB_EXEC_MODE=partialfail PATH="$tmp/bin:$PATH" \
    cozyreport_read_exec "$tmp/out/nodes.txt" kubectl exec -n ns deploy/c -- linstor n l

  grep -q 'srv2 | Ye' "$tmp/out/nodes.txt"
  grep -q '^# \[cozyreport\] TRUNCATED' "$tmp/out/nodes.txt" || {
    echo "FAIL: a partial table that failed on its own terms carries no marker, or not in the shared form"
    cat "$tmp/out/nodes.txt"
    false
  }
  # And it does not borrow the timeout branch's wording, which names a clock that
  # did not fire.
  if grep -q 'killed at exit' "$tmp/out/nodes.txt"; then
    echo "FAIL: a non-timeout failure is described as a kill"; false; fi
  rm -rf "$tmp"
}

@test "a table that got nothing still says why instead of shipping empty" {
  # The sharpest case in the ticket: a wedged controller holds the stream open and
  # the read is killed before the command writes a byte, so there is no stdout to
  # mark and no stderr to copy. Left alone, `linstor/nodes.txt` ships at zero
  # length and reads as a cluster with no nodes -- and unlike the two archive
  # reads, these four have no call-site emptiness check to remove it.
  tmp=$(mktemp -d)
  exec_stub_dir "$tmp"

  STUB_EXEC_MODE=silentcut PATH="$tmp/bin:$PATH" \
    cozyreport_read_exec "$tmp/out/nodes.txt" kubectl exec -n ns deploy/c -- linstor n l

  [ -s "$tmp/out/nodes.txt" ] || {
    echo "FAIL: a killed table shipped as a zero-byte file"; false; }
  grep -q 'killed at exit 124 before the command inside the pod wrote anything' "$tmp/out/nodes.txt"

  # And it must say what the emptiness is ABOUT. "This file is empty" is a fact
  # about the file; without the contrast a reader supplies the other subject and
  # concludes the command found nothing.
  grep -q 'not because the command' "$tmp/out/nodes.txt" || {
    echo "FAIL: the placeholder does not separate an empty file from an empty result"
    cat "$tmp/out/nodes.txt"
    false
  }
  rm -rf "$tmp"
}

@test "an exec read with nowhere to write stderr does not claim it was silent" {
  # `mktemp` fails on a machine with no writable temp dir, so stderr goes to
  # /dev/null and the reader cannot know whether the command spoke. Every other
  # reader in this file carries that distinction; this one did not, and the merge
  # base put the message in the file unconditionally through `2>&1`, so losing it
  # here is a narrow regression rather than a gap.
  tmp=$(mktemp -d)
  exec_stub_dir "$tmp"
  ro="$tmp/readonly"; mkdir -p "$ro"; chmod 500 "$ro"

  STUB_EXEC_MODE=refused TMPDIR="$ro" PATH="$tmp/bin:$PATH" \
    cozyreport_read_exec "$tmp/out/pools.txt" kubectl exec -n ns deploy/c -- linstor sp l

  chmod 700 "$ro"
  [ -s "$tmp/out/pools.txt" ] || {
    echo "FAIL: the read left nothing at all behind"; false; }
  grep -q 'could not be captured' "$tmp/out/pools.txt" || {
    echo "FAIL: a lost message is reported as the command having said nothing"
    cat "$tmp/out/pools.txt"
    false
  }
  rm -rf "$tmp"
}

@test "a cut-off bundle note says both of the two things that can happen to it" {
  # The note used to assert one outcome and was wrong both times it tried. A
  # stream that got members across is kept and truncated; a stream killed before
  # its first byte leaves zero bytes and the call site's emptiness check removes
  # it. This path runs before that check, so it cannot know which -- and the
  # wedged controller the ticket is about is the second case, the one the
  # confident wording denied.
  tmp=$(mktemp -d)
  exec_stub_dir "$tmp"

  STUB_EXEC_MODE=cut PATH="$tmp/bin:$PATH" \
    cozyreport_read_exec "$tmp/out/bundle.tgz" kubectl exec -n ns deploy/c -- sh -c 'true'

  note="$tmp/out/COLLECTION-FAILED.txt"
  # The archive gets NO in-file marker: a line of prose inside a gzip stream is a
  # file nothing can open, which is the whole reason this reader writes beside.
  if tr -d '\0' < "$tmp/out/bundle.tgz" 2>/dev/null | grep -q 'cozyreport'; then
    echo "FAIL: prose was appended into an archive"
    false
  fi
  grep -q 'EITHER beside it and truncated' "$note" || {
    echo "FAIL: the note does not offer the kept-and-truncated outcome"; cat "$note"; false; }
  grep -q 'OR gone' "$note" || {
    echo "FAIL: the note does not offer the removed-because-empty outcome"; cat "$note"; false; }
  rm -rf "$tmp"
}

@test "a read that is not kubectl is not reported as kubectl in its own marker" {
  # This reader served kubectl alone until the sweep gave it talosctl and the four
  # host commands. Its markers named kubectl and called every non-YAML target an
  # object, so a `df` that hit a stale mount produced "kubectl exited 1 ... not
  # because the object ended" -- two false statements in one line, in an artifact
  # whose whole premise is that its notes can be believed.
  tmp=$(mktemp -d)
  mkdir -p "$tmp/bin" "$tmp/out"
  cat > "$tmp/bin/df" <<'STUB'
#!/bin/sh
printf 'Filesystem Size\n/dev/disk1 100G\n'
echo 'df: cannot read table of mounted file systems' >&2
exit 1
STUB
  chmod +x "$tmp/bin/df"

  COZYREPORT_BOUND="" PATH="$tmp/bin:$PATH" \
    cozyreport_read_object "$tmp/out/df.txt" df -h

  marker=$(tail -n 1 "$tmp/out/df.txt")
  case "$marker" in
    *kubectl*) echo "FAIL: a df read is reported as kubectl: $marker"; false ;;
  esac
  case "$marker" in
    *"the object ended"*) echo "FAIL: a df listing is called an object: $marker"; false ;;
  esac
  printf '%s\n' "$marker" | grep -q 'df exited 1'
  rm -rf "$tmp"
}

@test "a probe cut off is not left looking like a component that is absent" {
  tmp=$(mktemp -d)
  exec_stub_dir "$tmp"
  REPORT_DIR="$tmp/report"
  mkdir -p "$REPORT_DIR/kubernetes"

  # The gate keeps its meaning: a probe that did not answer is not a probe that
  # answered "no", so the caller still skips the module.
  rc=0
  STUB_EXEC_MODE=cut PATH="$tmp/bin:$PATH" \
    cozyreport_probe "linstor" kubectl get deploy -n cozy-linstor linstor-controller || rc=$?
  [ "$rc" -ne 0 ] || { echo "FAIL: a cut-off probe reported the module as present"; false; }

  # And the skip is recorded, because the tree it leaves behind is the tree a
  # cluster without LINSTOR leaves: no directory, no marker, nothing to read.
  [ -f "$REPORT_DIR/kubernetes/COLLECTION-FAILED.txt" ] || {
    echo "FAIL: a cut-off probe skipped its section in silence"; false; }
  grep -q 'linstor' "$REPORT_DIR/kubernetes/COLLECTION-FAILED.txt"
  grep -q 'NOT a statement that the component is absent' "$REPORT_DIR/kubernetes/COLLECTION-FAILED.txt"
  rm -rf "$tmp"
}

@test "a probe answering plainly leaves nothing behind either way" {
  tmp=$(mktemp -d)
  exec_stub_dir "$tmp"
  REPORT_DIR="$tmp/report"
  mkdir -p "$REPORT_DIR/kubernetes"

  PATH="$tmp/bin:$PATH" cozyreport_probe "kamaji" kubectl get deploy -n cozy-kamaji kamaji

  # A component that IS installed writes no note, and neither does one that is
  # not: "this cluster does not run that" is the cluster answering correctly, and
  # a marker that fires on every install without KubeVirt or without LINSTOR is
  # one a triager learns to skip -- which costs the runs where it names something
  # real. Only the read that never came back is worth a line.
  rc=0
  STUB_EXEC_MODE=refused PATH="$tmp/bin:$PATH" \
    cozyreport_probe "cert-manager" kubectl get crd certificates.cert-manager.io || rc=$?
  [ "$rc" -ne 0 ] || { echo "FAIL: an absent component was reported as present"; false; }
  if [ -f "$REPORT_DIR/kubernetes/COLLECTION-FAILED.txt" ]; then
    echo "FAIL: an ordinary probe filed a collection failure"
    cat "$REPORT_DIR/kubernetes/COLLECTION-FAILED.txt"
    false
  fi
  rm -rf "$tmp"
}

@test "an exec that finished leaves no failure note and no prose in its output" {
  tmp=$(mktemp -d)
  exec_stub_dir "$tmp"

  PATH="$tmp/bin:$PATH" \
    cozyreport_read_exec "$tmp/out/nodes.txt" kubectl exec -n ns deploy/c -- linstor n l

  grep -q 'srv1 | Yes' "$tmp/out/nodes.txt"
  if [ -f "$tmp/out/COLLECTION-FAILED.txt" ]; then
    echo "FAIL: a read that finished was filed as a failure"
    cat "$tmp/out/COLLECTION-FAILED.txt"
    false
  fi
  # kubectl announces the container it defaulted to on every exec against a
  # Deployment. Merged into the output it is a line at the top of a table a
  # person reads, and in the two calls that stream a tarball it is a corrupt
  # archive, so it goes beside the file the same way a warning on any other
  # successful read does.
  if grep -q 'Defaulted container' "$tmp/out/nodes.txt"; then
    echo "FAIL: kubectl's own line was merged into the output it describes"
    false
  fi
  grep -q 'Defaulted container' "$tmp/out/READ-WARNINGS.txt"
  rm -rf "$tmp"
}

@test "no read in the report body is taken without a wall clock ceiling" {
  # This replaces a guard that froze WHICH sections were bounded, and a second one
  # that froze how many unbounded reads each of the others had. Both were lists,
  # and a list is the wrong shape for this claim: the header named four sections
  # while the file had ten, and the counts went stale inside the commit that was
  # relying on them. The claim worth holding is universal -- no read here runs
  # without a ceiling -- and a universal claim is checked by finding a
  # counterexample, not by matching a table.
  #
  # Scoped to the report BODY, below the library guard. Above it the same words
  # appear in the sentences these readers write into the artifact, so a scan of
  # the whole file answers "where is kubectl mentioned" rather than "what does
  # this script run"; the readers themselves are asserted elsewhere, one by one.
  guard=$(grep -n 'COZYREPORT_LIB:-' "$SCRIPT" | head -1 | cut -d: -f1)
  [ -n "$guard" ] || { echo "FAIL: could not find the library guard"; false; }
  body=$(awk -v g="$guard" 'NR>g' "$SCRIPT" | grep -v '^[[:space:]]*#' |
    awk '{ line = line $0
           if (line ~ /\\$/) { sub(/\\$/, " ", line); next }
           print line; line = "" }
         END { if (line != "") print line }')

  # `command -V kubectl` is the dependency check, not a read: it never contacts a
  # cluster and cannot hang on one.
  #
  # The four host commands are in the pattern too, anchored at command position so
  # the words cannot match prose. They talk to no cluster, which is exactly why
  # they were missed: `df` blocks forever on a stale mount and takes the tarball
  # with it, and a scan that only knows about kubectl reports a clean sweep while
  # the header's claim -- EVERY read -- is false about four lines of the file.
  reads=$(printf '%s\n' "$body" |
    grep -E '(kubectl|talosctl)[[:space:]]|^[[:space:]]*(df|free|ps|dmesg)[[:space:]]' |
    grep -v 'command -V' || true)
  # A scan that suddenly matches nothing is a broken scan, not a clean file, and
  # it would report the strongest possible result while checking nothing at all.
  n=$(printf '%s\n' "$reads" | grep -c . || true)
  # A FLOOR against a scan that stopped matching, not a count of the reads: the
  # body has around ninety, so a pattern that broke would fall far below this
  # rather than just under it. It was set at 40 while the real figure was 94, which
  # left room for a dozen reads to go invisible without the floor noticing -- the
  # number is now close enough to the truth to be worth something, and deliberately
  # not exact, because an exact one is a frozen count and this file has spent the
  # whole change removing those.
  [ "$n" -ge 80 ] || {
    echo "FAIL: the scan found only $n reads in the report body, which is far fewer than this file has;"
    echo "the pattern has stopped matching rather than the reads having gone away."
    false
  }

  # Bounded means: through one of the four readers, through the collector that
  # uses them, or through the bare wrapper at the sites where the reader cannot own
  # the redirect -- a value captured into a variable, or a pipeline that transforms
  # the output before anything lands. Described rather than counted: the header
  # carried "three" of those while the file had five.
  offenders=$(printf '%s\n' "$reads" |
    grep -vE 'cozyreport_(read_object|read_exec|select_objects|probe|collect_pod|collect_broken_pods|collect_talos_node)|\$COZYREPORT_BOUND' || true)
  if [ -n "$offenders" ]; then
    echo "FAIL: a read in the report body runs with no wall-clock ceiling:"
    printf '%s\n' "$offenders"
    echo "The tarball is written on this script's last line, so a read that never"
    echo "returns loses the whole artifact rather than truncating one file."
    false
  fi

  # What this cannot see, said out loud rather than left to be found: a folded
  # line carrying two commands satisfies it if either one is bounded. Nothing in
  # this file is written that way, and the gates below hold the other spellings.
  #
  # And the header makes the same claim in words, beside the code that keeps it.
  # A guard on the code alone leaves the sentence free to drift, which is the
  # failure the list-shaped version of this test kept having.
  head -n 60 "$SCRIPT" | grep -q '^# EVERY read below carries a wall-clock ceiling' || {
    echo "FAIL: the header no longer claims that every read is bounded"
    false
  }
}

@test "the step that runs this collector has a ceiling of its own everywhere" {
  # Every bound inside the script protects one read. None of them protects the
  # STEP: the tarball is written on the collector's last line, the upload is the
  # next step in the job, and a step with no ceiling of its own inherits only the
  # job's -- so a read that never returns spends the rest of the job budget, the
  # job is cancelled, and the upload never runs. That loses the artifact instead
  # of truncating it, on the run where it is the only evidence left.
  #
  # All three workflows that run the collector, not the one that was looked at:
  # the same step is copied into each, and a ceiling added to one of them leaves
  # the other two with the defect while the fix looks done.
  for wf in pull-requests nightly e2e-tag; do
    f="$HACK_DIR/../.github/workflows/$wf.yaml"
    [ -f "$f" ] || { echo "FAIL: .github/workflows/$wf.yaml is missing"; false; }
    # The job's own cap is the last one seen above the step, told apart from the
    # step's by indentation: job keys sit at four spaces, step keys at eight.
    # Comparing against it is what makes the step's number mean something -- a
    # ceiling at or above the job's is the same as no ceiling at all.
    caps=$(awk '
      /^    timeout-minutes: [0-9]+$/  { job = $2 }
      /^      - name: Collect report$/ { inb = 1; next }
      inb && /^      - name: /         { inb = 0 }
      inb && /^        timeout-minutes: [0-9]+$/ { step = $2; cap = job }
      inb && /^        continue-on-error: true$/ { tol = "yes" }
      END { print cap "/" step "/" tol }' "$f")
    cap=${caps%%/*}
    rest=${caps#*/}
    step=${rest%/*}
    tol=${rest#*/}
    if [ -z "$step" ]; then
      echo "FAIL: the Collect report step in $wf.yaml carries no timeout-minutes"
      echo "Without one it inherits the job's, and a hung read costs the upload"
      echo "that follows it rather than costing this step."
      false
    fi
    [ -n "$cap" ] || { echo "FAIL: could not find the job cap in $wf.yaml"; false; }
    if [ "$step" -ge "$cap" ]; then
      echo "FAIL: the Collect report step in $wf.yaml is capped at $step minutes,"
      echo "which the job's own $cap does not leave room under."
      false
    fi
    # And the ceiling must not change what the job REPORTS. `|| true` on the make
    # call is this step's standing statement that collecting diagnostics is never
    # fatal; a step killed at its ceiling never reaches that `|| true`, because the
    # runner ends the process rather than the shell. Without continue-on-error a
    # passing E2E run whose collection ran long is published as a failed one, and
    # every later `failure()` in the job starts meaning "the tests failed OR the
    # report was slow" -- a ceiling added for diagnostics would then be deciding
    # merges.
    if [ "$tol" != "yes" ]; then
      echo "FAIL: the Collect report step in $wf.yaml has a ceiling but no continue-on-error,"
      echo "so hitting that ceiling turns a passing run red on a diagnostics overrun."
      false
    fi
    # And the overrun stays visible. `continue-on-error` keeps a fired ceiling out
    # of the job's result, which also keeps it out of everyone's view: the step
    # renders green and the only trace is in the raw log. This collector's whole
    # standard is that a bound which fires is recorded, and the outer bound is not
    # exempt from it -- so the step is addressable by id and a later step turns its
    # outcome into a warning annotation.
    grep -q '^        id: collect_report$' "$f" || {
      echo "FAIL: the Collect report step in $wf.yaml has no id, so nothing can key on its outcome"
      false
    }
    grep -q "steps.collect_report.outcome == 'failure'" "$f" || {
      echo "FAIL: nothing in $wf.yaml surfaces a Collect report overrun;"
      echo "continue-on-error renders it green and the ceiling fires in silence."
      false
    }
  done
}

@test "every helper these scripts define is wired to at least one call site" {
  # Written as a shape rather than as a list of "these two calls must appear in
  # those three loops", because that shape of guard is blind to the helper added
  # next -- which is exactly how it failed here: cozyreport_objects_deadline_passed
  # had its own passing test while one loop had stopped calling it, and the
  # wiring assertion named other functions.
  #
  # A helper with no call site is the general form of that gap: the behaviour is
  # covered, the wiring is not, and both tests stay green. Definition and use are
  # counted separately so the definition line cannot satisfy the check.
  orphans=""
  for f in cozyreport.sh e2e-capture-previous-logs.sh; do
    for h in $(grep -oE '^[a-z_][a-z0-9_]*\(\)' "$HACK_DIR/$f" | tr -d '()'); do
      case "$h" in
        # `log` in the capture script is called from every branch of the walk,
        # but the name is too short for a word-boundary grep to separate it from
        # ordinary prose. Its wiring is covered by the tests that assert on the
        # lines it emits.
        log) continue ;;
      esac
      uses=$(fold_source "$HACK_DIR/$f" \
        | grep -vE "^[a-z_][a-z0-9_]*\(\)" \
        | grep -cE "(^|[^a-zA-Z0-9_])$h([^a-zA-Z0-9_]|$)" || true)
      # cozyreport.sh's helpers may legitimately be called only from the summary,
      # which sources them.
      if [ "$uses" -eq 0 ] && [ "$f" = cozyreport.sh ]; then
        uses=$(fold_source "$HACK_DIR/cozyreport-summary.sh" \
          | grep -cE "(^|[^a-zA-Z0-9_])$h([^a-zA-Z0-9_]|$)" || true)
      fi
      [ "$uses" -gt 0 ] || orphans="$orphans $f:$h"
    done
  done

  if [ -n "$orphans" ]; then
    echo "FAIL: helper defined but never called:$orphans"
    echo "A helper with no call site is behaviour that is tested and not reached."
    false
  fi
}

@test "the pod and kubevirt per-object reads all go through the bounded reader" {
  # The header scopes the bound by naming sections. Sharing one READY selector widened the pod and VMI sections and
  # NARROWED the VM one: VM already selected on READY through a fixed $5, and $NF
  # only drops the short rows where that $5 was spuriously empty. Only the
  # pod section had been bounded, and
  # nothing asserted the boundedness of the other two, so the selector change
  # passed without touching a single assertion about ceilings. A VMI at
  # Phase=Running whose guest agent never connected is the normal shape of a failed
  # tenant-kubernetes run, so the new fan-out lands exactly where a degraded
  # apiserver is most likely to hang a read.
  # The pod section first: it is the change's primary behaviour, and its call site
  # was the last unpinned seam -- replacing this line with a no-op left every test
  # green, because the collector function is covered from every angle and nothing
  # asserted that the report invokes it.
  pods=$(awk '/^echo "Collecting pods/,/^echo "Collecting virtualmachines/' "$SCRIPT")
  [ -n "$pods" ] || { echo "FAIL: could not locate the pod section"; false; }
  printf '%s\n' "$pods" | grep -q '^cozyreport_collect_broken_pods "' || {
    echo "FAIL: the pod section does not call the collector"
    printf '%s\n' "$pods"
    false
  }
  if printf '%s\n' "$pods" | grep -qE '^kubectl get pod .*\| *awk '; then
    echo "FAIL: the pod section still selects inline"
    false
  fi

  for section in vm vmi; do
    # Comments stripped first: the selector helper's docstring quotes the same
    # `kubectl get vm -A --no-headers` line it documents, and an unstripped range
    # starts there and swallows everything down to the next `done`.
    body=$(fold_source "$SCRIPT" | awk "/kubectl get $section -A --no-headers/,/^  done/")
    [ -n "$body" ] || { echo "FAIL: could not locate the $section loop"; false; }
    # Every kubectl in the loop goes through the bounded reader rather than a bare
    # call, which is also what attaches a truncation marker when a read is cut off.
    if printf '%s\n' "$body" | grep -qE "^[[:space:]]+$KUBECTL_RE"; then
      echo "FAIL: the $section loop still has an unbounded kubectl read"
      printf '%s\n' "$body" | grep -nE "^[[:space:]]+$KUBECTL_RE"
      false
    fi
    printf '%s\n' "$body" | grep -q 'cozyreport_read_object' || {
      echo "FAIL: the $section loop does not use the bounded reader"
      false
    }
    # And the SELECTOR is wired in, not just the reader. Reverting either call site
    # to its pre-change filter -- `awk '$4 != "Running"'` for vmi, `awk '$5 !=
    # "True"'` for vm -- left every test green: the helper was covered, the wiring
    # was not, and "the VMI section moved off a phase filter" is one of this
    # change's three headline behaviours.
    printf '%s\n' "$body" | grep -q 'cozyreport_kubevirt_not_ready' || {
      echo "FAIL: the $section selection does not go through the shared helper"
      printf '%s\n' "$body" | head -3
      false
    }
    if printf '%s\n' "$body" | grep -qE "^kubectl get $section .*\| *awk "; then
      echo "FAIL: the $section selection still filters inline"
      false
    fi
  done

  # Both object bounds are WIRED, not merely defined. The helpers have their own
  # tests; a loop that stopped calling them would keep those green while losing
  # the bound entirely, which is the same gap the selector call sites had.
  for section in 'kubectl get vm -A --no-headers' 'kubectl get vmi -A --no-headers' 'kubectl get svc -A --no-headers'; do
    loop=$(fold_source "$SCRIPT" | awk -v s="$section" 'index($0, s) > 0, /^  done/')
    [ -n "$loop" ] || { echo "FAIL: could not locate the loop for $section"; false; }
    printf '%s\n' "$loop" | grep -q 'cozyreport_admit_objects' || {
      echo "FAIL: $section is not capped"; false
    }
    # The exact form, not just the name: `if ! cozyreport_objects_deadline_passed`
    # satisfies a name check while inverting the bound -- a live budget would stop
    # the walk on its first object and an expired one would let it run on.
    printf '%s\n' "$loop" | grep -qE 'if[[:space:]]+cozyreport_objects_deadline_passed;[[:space:]]*then' || {
      echo "FAIL: $section does not check the object budget in the expected form"
      printf '%s\n' "$loop" | grep -n 'deadline_passed' || true
      false
    }
    printf '%s\n' "$loop" | grep -q 'cozyreport_objects_budget_note' || {
      echo "FAIL: $section breaks on the budget without recording that it did"; false
    }
    # And the SELECTOR read is bounded. The budget only starts counting once rows
    # arrive, so an apiserver that hangs on the cluster-wide list spends the whole
    # collection step without the deadline ever being consulted. Bounding the
    # per-object reads and leaving the read that feeds them unbounded protects the
    # cheap half of the loop and not the expensive one.
    # Bounded AND status-keeping. A pipeline in dash has no pipefail, so a
    # selector piped straight in loses its exit code: a read killed at 124 leaves
    # no rows, no error and no marker, which is byte-identical to a cluster with
    # nothing to select. cozyreport_select_objects does the read, keeps the
    # status, and records the failure.
    printf '%s\n' "$loop" | head -1 | grep -q 'cozyreport_select_objects' || {
      echo "FAIL: the $section selector read does not keep its exit status"
      printf '%s\n' "$loop" | head -1
      false
    }
  done

  # The services loop, on the same footing as vm/vmi: correcting its selector is
  # what made the body reachable, so it is also what made a hang inside it
  # possible. A section that never ran cannot have been bounded by accident.
  svcloop=$(fold_source "$SCRIPT" | awk '/kubectl get svc -A --no-headers/,/^  done/')
  [ -n "$svcloop" ] || { echo "FAIL: could not locate the services loop"; false; }
  if printf '%s\n' "$svcloop" | grep -qE "^[[:space:]]+$KUBECTL_RE"; then
    echo "FAIL: the services loop still has an unbounded kubectl read"
    printf '%s\n' "$svcloop" | grep -nE "^[[:space:]]+$KUBECTL_RE"
    false
  fi
  printf '%s\n' "$svcloop" | grep -q 'cozyreport_read_object' || {
    echo "FAIL: the services loop does not use the bounded reader"
    false
  }

  # The cozystack apps section, whose every read was gated on a CRD that does not
  # exist. Two separate assertions, because the two kinds are reached two
  # different ways and the old loop got both wrong the same way.
  apps=$(fold_source "$SCRIPT" | awk '/^echo "Collecting cozystack apps/,/^echo "Collecting pods/')
  [ -n "$apps" ] || { echo "FAIL: could not locate the cozystack apps section"; false; }
  if printf '%s\n' "$apps" | grep -q 'get crd'; then
    echo "FAIL: a cozystack apps read is gated on a CRD again"
    printf '%s\n' "$apps" | grep -n 'get crd'
    false
  fi
  # Tenants come from an aggregated APIService: a down API must arrive as
  # kubectl's refusal in the file, which is what the bounded reader guarantees,
  # rather than as an absent file that reads as a cluster with no tenants.
  printf '%s\n' "$apps" | grep -q 'cozyreport_read_object "$DIR/tenants.txt"' || {
    echo "FAIL: the tenant listing does not go through the bounded reader"
    false
  }
  # ApplicationDefinition is cluster-scoped. `-A` there is the other half of the
  # old loop's mistake, and it is silent: the namespace column prints <none>.
  if printf '%s\n' "$apps" | grep -q 'applicationdefinitions.*-A'; then
    echo "FAIL: a cluster-scoped kind is read with --all-namespaces"
    false
  fi

  # Same wiring assertion for the services section, whose inline selector read the
  # wrong column for its whole life. An inline `awk '$N == ...'` is unreachable
  # from a test, so every one of them is a column index nobody has ever run
  # against a real row -- which is how `$4 == "<pending>"` survived.
  svc=$(fold_source "$SCRIPT" | awk '/kubectl get svc -A --no-headers/,/^  done/')
  [ -n "$svc" ] || { echo "FAIL: could not locate the services loop"; false; }
  printf '%s\n' "$svc" | grep -q 'cozyreport_svc_pending' || {
    echo "FAIL: the services selection does not go through the shared helper"
    printf '%s\n' "$svc" | head -3
    false
  }
  if printf '%s\n' "$svc" | grep -qE '^kubectl get svc .*\| *awk '; then
    echo "FAIL: the services selection still filters inline"
    false
  fi

  # Asserting the two loops by name only guards the two that exist. The defect was
  # that a section without a boundedness test drifted silently, and a section added
  # next month would drift the same way -- so freeze the inventory of reads that do
  # NOT go through the bounded reader. The file deliberately leaves those alone
  # (its header says which sections are in scope and why), but the number must not
  # grow without someone deciding to let it: a new unbounded section moves this
  # count and fails here at the moment it is written, rather than when a degraded
  # apiserver hangs it in production.
  # Frozen as a per-section LIST, not as a total. A total does not distinguish the
  # cases it is counted for: bounding one read in section A while adding an
  # unbounded one to section B leaves the sum unchanged, so the guard stays silent
  # while precisely the thing it exists to catch -- a new section reading straight
  # into the tree -- ships. A swap of that shape leaves the total unmoved and the
  # test green.
  #
  # Keyed by section rather than by line number: line numbers move with any edit
  # above them, which would make this a standing false red that somebody deletes
  # within a week. The list is also its own documentation -- it names which
  # sections still read unbounded, so the follow-up that bounds them has its scope
  # written down here rather than in a comment holding a third copy of the number.
  #
  # LC_ALL=C on the sort: without it the order depends on the caller's collation,
  # so this guard passed on one machine and failed on another with an identical
  # tree. A check whose verdict depends on the environment it runs in is not a
  # check.
  # Section header matched at ANY indentation, redirect matched by SHAPE.
  #
  # Both of those were wrong, and each blinded the guard to one of the two cases
  # the comment above claims it catches. The header pattern was anchored at column
  # zero, so the four sections that sit inside an `if` -- cert-manager,
  # objectstorage, kamaji, linstor -- had their reads credited to whatever
  # top-level section came last; that is why the frozen list said `pvcs=17` when
  # the pvcs section has two. And the redirect pattern was an allowlist of three
  # variable names, so the cert-manager loop writing into `$cdir` was invisible
  # entirely. A new read added in that loop's own style moved nothing, and a swap
  # between two sections sharing a bucket moved nothing either -- the exact swap
  # the comment says per-section keying eliminated. It was verified once, between
  # two sections that happened not to share a bucket.
  #
  # An allowlist of names is the same defect as the marker regex that enumerated
  # known prefixes: it can only see the cases somebody already thought of.
  # Folded before counting, and the redirect matched in both variable spellings:
  # a read whose `>` sits one continuation down, or which writes to `${DIR}/x`
  # rather than `$DIR/x`, is the same unbounded read and was invisible to a
  # pattern that assumed one line and one form.
  # The per-section inventory of unbounded reads that used to sit here is gone,
  # and its replacement is the guard above: every read in the body is bounded, so
  # the counts it froze are all zero and a table of zeroes says less than the
  # sentence does. It was also blind by construction -- it matched a redirect to
  # `$VAR/`, which no gate, selector or command substitution has -- so its numbers
  # were "how many of a section's unbounded reads this pattern can see", and they
  # were quoted as though they were all of them.
  #
  # What stays here is the BOUNDARY every one of these scans rests on. They all
  # look for the bare name, so kubectl invoked by path, through a variable, or
  # under sudo is invisible to them -- and an invisible read is reported as no
  # read at all, which is the strongest possible result and the wrong one. Fail
  # here instead, in the same commit that introduces the spelling.
  offbeat=$(fold_source "$SCRIPT" \
    | grep -nE '(^|[[:space:]|(])(/[A-Za-z0-9_/.-]*kubectl|sudo[[:space:]]+kubectl|\$\{?KUBECTL)' || true)
  if [ -n "$offbeat" ]; then
    echo "FAIL: kubectl is invoked in a form these scans cannot see:"
    printf '%s\n' "$offbeat"
    echo "Either keep invoking it by bare name, or widen the scans in the same commit."
    false
  fi
}

@test "a failed mktemp refuses to write the report to the filesystem root" {
  tmp=$(mktemp -d)
  bin="$tmp/bin"; mkdir -p "$bin"
  for b in sh dash grep sed awk cat rm mkdir tail head cut ls date printf tar dirname basename; do
    p=$(command -v "$b" 2>/dev/null) && ln -sf "$p" "$bin/$b"
  done
  printf '#!/bin/sh\nexit 1\n' > "$bin/mktemp"; chmod +x "$bin/mktemp"
  printf '#!/bin/sh\nexit 0\n' > "$bin/kubectl"; chmod +x "$bin/kubectl"

  # This file sets no errexit, so a failing `mktemp -d` leaves REPORT_PDIR empty
  # and every path below becomes absolute from `/`. With no errexit a refused
  # `mkdir -p` stops nothing: an unprivileged user gets an empty tree, but
  # root in a container -- how CI runs this -- gets directories created at the
  # filesystem root and a report written into the host's `/`.
  out=$(cd "$tmp" && PATH="$bin" sh "$SCRIPT" 2>&1 || true)

  printf '%s\n' "$out" | grep -q 'refusing to write a report to the filesystem root'
  # And it stopped there: no section header from the body may appear.
  if printf '%s\n' "$out" | grep -q 'Collecting Cozystack information'; then
    echo "FAIL: the script carried on past an empty report directory"
    printf '%s\n' "$out"
    false
  fi
  rm -rf "$tmp"
}

@test "no live jsonpath nests one filter inside another" {
  # kubectl rejects `[?( ... [?( ... )] ... )]` outright with "unterminated filter",
  # client-side, before the read reaches the apiserver: its parser breaks on the
  # FIRST `)` outside a quoted string and never tracks nesting. With stderr sent to
  # /dev/null the caller sees an empty result, which is indistinguishable from a
  # healthy cluster -- so the section renders as "everything is Ready" forever, and
  # nothing in the artifact says otherwise.
  #
  # Two sites had this. One was found and fixed; the other sat in this same file
  # and was found only because someone grepped the tree for the PATTERN rather than
  # re-reading the fix. That is what this test is: the grep, kept.
  #
  # Comments are stripped first, because both files legitimately quote the broken
  # form while explaining it -- the same false positive the EXIT-trap guard hit.
  offenders=""
  for f in cozyreport.sh cozyreport-summary.sh e2e-capture-previous-logs.sh; do
    # Folded: the two halves of a nested filter can sit either side of a line
    # continuation and concatenate into the forbidden form at runtime, which no
    # per-line pattern sees.
    if fold_source "$HACK_DIR/$f" | grep -qoE '\[\?\([^]]*\[\?\('; then
      offenders="$offenders $f"
    fi
  done
  if [ -n "$offenders" ]; then
    echo "FAIL: jsonpath with a filter nested inside a filter in:$offenders"
    echo "kubectl refuses to parse it; use --output=custom-columns, which takes one filter per column."
    false
  fi
}

@test "the budget overshoot the header states is the one the container loop takes" {
  # The header and e2e-testing.md both put a number on how far past
  # COZY_REPORT_PODS_BUDGET a run can go, and that number is a count of reads
  # taken after the last deadline check -- not something either file can derive
  # for itself. It has already been wrong twice: first stated per POD, which grows
  # without bound as the container count grows, then per container LOG, when each
  # container is read twice, current and previous, with no check between them.
  #
  # So pin the two counts the sentence rests on. Changing either -- adding a third
  # read, or moving the check between the two logs -- fails here, next to the
  # prose that has to change with it, rather than in a report whose ceiling
  # quietly stopped matching its own documentation.
  loop=$(awk '/^  for _ccp_c in \$_ccp_containers; do/,/^  done/' "$SCRIPT")
  [ -n "$loop" ] || { echo "FAIL: could not locate the container loop"; false; }

  reads=$(printf '%s\n' "$loop" | grep -c 'cozyreport_read_container_log ')
  [ "$reads" -eq 2 ] || {
    echo "FAIL: the container loop takes $reads bounded reads, the docs assume 2"
    false
  }
  # Counted by the clock read, not by the knob's name: the name also appears in
  # the comment above the check and in the `-n` test guarding it, so counting it
  # answers "how often is the knob mentioned" while reading as "how often is the
  # deadline checked". Every check reads the clock exactly once, and nothing else
  # in this loop reads it at all.
  checks=$(printf '%s\n' "$loop" | grep -c 'date +%s')
  [ "$checks" -eq 1 ] || {
    echo "FAIL: the container loop checks the deadline $checks times, the docs assume 1"
    false
  }

  # And both files say so in the same terms. A guard on the code alone leaves the
  # sentence free to drift, which is the failure being prevented.
  grep -q "current AND previous log reads" "$SCRIPT" || {
    echo "FAIL: the header no longer states the overshoot in terms of both reads"
    false
  }
  grep -q "both log reads of one container" "$HACK_DIR/../docs/agents/e2e-testing.md" || {
    echo "FAIL: e2e-testing.md no longer states the overshoot in terms of both reads"
    false
  }
}

@test "every marker file the docs promise is one the script can write" {
  # The doc bullet is how a reader learns which filenames to look for in the
  # tarball. A marker the script writes but the docs never name leaves whoever
  # finds it with nothing explaining it; a marker the docs promise but the script
  # never writes sends them grepping for a file that cannot exist, and they
  # conclude the case did not arise. Tests pin the code side, so without this
  # nothing holds the doc side to it.
  doc="$HACK_DIR/../docs/agents/e2e-testing.md"
  [ -f "$doc" ]
  # Matched on the SHAPE of a marker name, not on a list of the suffixes anybody
  # has used so far. Two earlier spellings enumerated: first the prefixes
  # `(COLLECTION|CONTAINER-LOGS)-`, which was blind to logs-UNAVAILABLE.txt, then
  # the suffixes `(UNAVAILABLE|TRUNCATED|FAILED|UNBOUNDED|NOTES)`, which was blind
  # to CONTAINER-LOGS-BOUND.txt the moment it was added -- by the same commit that
  # was relying on this guard to notice. An enumeration cannot report the case
  # nobody thought of, and each fix to it only moves which case that is.
  #
  # Markers in this tree are SHOUTING names: capitals and hyphens, ending .txt.
  # Nothing else in either file matches that shape, so the guard now asks what a
  # marker looks like rather than what markers exist.
  pat='[A-Z][A-Z-]+\.txt'
  doc_names=$(grep -oE "$pat" "$doc" | sort -u)
  code_names=$(grep -oE "$pat" "$SCRIPT" | sort -u)
  [ -n "$doc_names" ] && [ -n "$code_names" ]
  for n in $doc_names; do
    if ! grep -q -- "$n" "$SCRIPT"; then
      echo "FAIL: docs/agents/e2e-testing.md promises $n but hack/cozyreport.sh never writes it"
      false
    fi
  done
  # And the other direction: a marker the script
  # writes and the docs never name leaves a reader who finds it in the tarball
  # with nothing telling them what it means.
  for n in $code_names; do
    if ! grep -q -- "$n" "$doc"; then
      echo "FAIL: hack/cozyreport.sh writes $n but docs/agents/e2e-testing.md never names it"
      false
    fi
  done
}

@test "pods not ready keeps a running pod whose readiness probe never passed" {
  out="$(printf '%s\n' "$SNAPSHOT" | cozyreport_pods_not_ready)"

  printf '%s\n' "$out" | grep -q 'keycloak-db-2'
}

@test "pods not ready keeps a partially ready multi container pod" {
  out="$(printf '%s\n' "$SNAPSHOT" | cozyreport_pods_not_ready)"

  printf '%s\n' "$out" | grep -q 'satellite-v'
}

@test "pods not ready still keeps the non running phases collected before" {
  out="$(printf '%s\n' "$SNAPSHOT" | cozyreport_pods_not_ready)"

  printf '%s\n' "$out" | grep -q 'puller-z'
  printf '%s\n' "$out" | grep -q 'unscheduled-w'
}

@test "pods not ready drops a fully ready running pod" {
  out="$(printf '%s\n' "$SNAPSHOT" | cozyreport_pods_not_ready)"

  # `! cmd` is vacuous under cozytest's `set -e` (errexit is suppressed for a
  # `!`-negated pipeline), so a regression that let this row through would not
  # fail the test. Assert the absence via `if cmd; then ...; false`.
  if printf '%s\n' "$out" | grep -q 'coredns-abc'; then echo "FAIL: a ready pod must not be collected"; false; fi
}

@test "a pod held unready by a readiness gate is outside what the column can see" {
  # The known gap, pinned rather than claimed away. READY counts ready CONTAINERS;
  # the pod's Ready condition is a different fact, and `spec.readinessGates` is
  # where they part: every container passes its probe, the column prints 1/1, and
  # `status.conditions[type=Ready]` is still False because a gate the kubelet does
  # not evaluate has not been satisfied. Such a pod gets no per-pod evidence.
  #
  # This is a deliberate trade, not an oversight -- the alternative is a
  # structured read per pod in place of one cluster-wide list, on the report of a
  # cluster that is already failing -- and it is here so that changing the trade
  # has to start by changing this test, and so that a reader chasing a missing pod
  # directory finds the reason instead of an empty tree.
  gated='tenant-a  gated-0  1/1  Running  0  4m'

  if [ -n "$(printf '%s\n' "$gated" | cozyreport_pods_not_ready)" ]; then
    echo "FAIL: the selection now sees something this column does not carry"
    false
  fi
}

@test "pods not ready drops finished pods that legitimately end unready" {
  out="$(printf '%s\n' "$SNAPSHOT" | cozyreport_pods_not_ready)"

  # A Completed/Succeeded pod sits at READY=0/1 forever. Selecting it would bury
  # the real failures under every job the suite ever ran.
  if printf '%s\n' "$out" | grep -q 'migrate-job-x'; then echo "FAIL: a Completed pod must not be collected"; false; fi
  if printf '%s\n' "$out" | grep -q 'backup-job-y'; then echo "FAIL: a Succeeded pod must not be collected"; false; fi
}

@test "pods not ready selects exactly the four broken rows of the snapshot" {
  out="$(printf '%s\n' "$SNAPSHOT" | cozyreport_pods_not_ready)"

  # Pins the count as well as the membership, so a widened predicate that starts
  # dragging healthy pods into the report fails here rather than quietly
  # doubling the size of every artifact.
  [ "$(printf '%s\n' "$out" | grep -c .)" -eq 4 ]
}

@test "kubevirt not ready keeps a running vmi whose guest never became ready" {
  # KubeVirt prints NAMESPACE NAME AGE PHASE IP NODENAME READY. A VMI at
  # Phase=Running with Ready=False is a guest that never booted or a guest-agent
  # that never connected, and a PHASE filter drops exactly that -- the same blind
  # spot a STATUS-only pod filter has, in the same script.
  rows="$(printf '%s\n' \
    'tenant-test  vm-booted     13h  Running  10.244.0.5  node0  True' \
    'tenant-test  vm-no-agent   13h  Running  10.244.0.6  node1  False' \
    'tenant-test  vm-pending    5m   Pending  False' \
    'tenant-test  vm-no-ip      13h  Running  node2  True')"

  out="$(printf '%s\n' "$rows" | cozyreport_kubevirt_not_ready)"

  printf '%s\n' "$out" | grep -q 'vm-no-agent'
  # The Pending row has no IP and no node, so it prints five fields where a Running
  # one prints seven.
  printf '%s\n' "$out" | grep -q 'vm-pending'
  if printf '%s\n' "$out" | grep -q 'vm-booted'; then echo "FAIL: collected a ready VMI"; false; fi
  # Six fields: node assigned, IP not reported yet, and Ready=True. This is the row
  # that makes $NF load-bearing -- a fixed $7 is empty here and would drag a ready
  # VMI into the report.
  if printf '%s\n' "$out" | grep -q 'vm-no-ip'; then echo "FAIL: read READY from a fixed column"; false; fi
  [ "$(printf '%s\n' "$out" | grep -c .)" -eq 2 ]
}

@test "the vm selector narrows rather than widens on a short row" {
  # The header's scoping argument names which sections changed and how. VM was
  # already READY-based on main (`$5 != "True"`), so reading $NF NARROWS it: on a
  # row whose STATUS is nil the fixed $5 is empty, empty is not "True", and a
  # healthy VM was collected as broken. New is a strict subset of old here, unlike
  # pods and VMIs which were genuinely widened. The header claimed all three were
  # widened; this pins the distinction so the claim cannot drift back.
  # Narrowing is only safe if it drops exactly the healthy rows. A selector that
  # dropped every short row would trade a false positive for a MISS, and a miss is
  # dearer in a collector: an over-collected object the operator sees and discards,
  # a skipped one does not exist. So both short-row cases are pinned, not just the
  # healthy one.
  healthy_short='tenant-test  vm-c  5m  True'
  broken_short='tenant-test  vm-d  5m  False'

  [ -n "$(printf '%s\n' "$healthy_short" | awk '$5 != "True"')" ] ||
    { echo "FAIL: fixture no longer models the over-collection"; false; }
  if [ -n "$(printf '%s\n' "$healthy_short" | cozyreport_kubevirt_not_ready)" ]; then
    echo "FAIL: the new selector still collects a healthy VM on a short row"
    false
  fi
  # The other half: a short row that is genuinely not Ready must survive. $NF is
  # positional, but it is fail-safe by construction -- anything whose last field is
  # not literally "True" is collected, so an absent READY column over-collects
  # rather than skipping.
  if [ -z "$(printf '%s\n' "$broken_short" | cozyreport_kubevirt_not_ready)" ]; then
    echo "FAIL: a not-Ready VM on a short row was skipped"
    false
  fi
  # The other section's direction, so "which one widened" has an executable
  # counterpart rather than only prose: a VMI at Phase=Running with Ready=False is
  # what the old `$4 != "Running"` filter dropped and the shared selector keeps.
  vmi_widened='tenant-test  worker-0  12m  Running  10.244.1.5  node1  False'
  [ -z "$(printf '%s\n' "$vmi_widened" | awk '$4 != "Running"')" ] ||
    { echo "FAIL: fixture no longer models the VMI blind spot"; false; }
  if [ -z "$(printf '%s\n' "$vmi_widened" | cozyreport_kubevirt_not_ready)" ]; then
    echo "FAIL: a Running VMI whose guest never became ready is still dropped"
    false
  fi

  # And an absent READY column entirely: unknown readiness is collected, not
  # assumed healthy.
  for unknown in 'tenant-test  vm-e  5m  Running' 'tenant-test  vm-f  5m'; do
    if [ -z "$(printf '%s\n' "$unknown" | cozyreport_kubevirt_not_ready)" ]; then
      echo "FAIL: a VM with no READY column was treated as Ready: $unknown"
      false
    fi
  done
}

@test "kubevirt not ready reads the vm layout by the same last column" {
  # The VM section shares this helper with the VMI section above, and its rows have
  # a different shape: NAMESPACE NAME AGE STATUS READY, five fields against seven.
  # Reading READY as $NF is what lets one predicate serve both, so both layouts are
  # pinned here -- otherwise the VM half is a behaviour nobody can regress-test,
  # which is how its selector drifted from its twin in the first place.
  rows="$(printf '%s\n' \
    'tenant-test  vm-up       13h  Running       True' \
    'tenant-test  vm-stopped  13h  Stopped       False' \
    'tenant-test  vm-fresh    3s   True' \
    'tenant-test  vm-prov     40s  Provisioning  False')"

  out="$(printf '%s\n' "$rows" | cozyreport_kubevirt_not_ready)"

  printf '%s\n' "$out" | grep -q 'vm-stopped'
  printf '%s\n' "$out" | grep -q 'vm-prov'
  if printf '%s\n' "$out" | grep -q 'vm-up'; then echo "FAIL: collected a ready VM"; false; fi
  # The row that makes $NF load-bearing on this layout: `.status.printableStatus`
  # is nil on a freshly created VM, so STATUS is absent and the row collapses to
  # four fields. A fixed $5 reads empty here, and empty is not "True", so the old
  # selector dragged this healthy VM into the report -- fail-safe in direction, but
  # still a healthy VM reported as broken, on every cluster with a VM being created.
  if printf '%s\n' "$out" | grep -q 'vm-fresh'; then echo "FAIL: read READY from a fixed column"; false; fi
  [ "$(printf '%s\n' "$out" | grep -c .)" -eq 2 ]
}

@test "pods prioritize matches the namespace field and not a row substring" {
  # Mirror of the sibling test in hack/capture-previous-logs.bats, which that
  # file's comment says this helper follows. The claim was never checked here:
  # swapping index($1, p) for index($0, p) left all 102 tests green.
  #
  # The fixture that bites is a pod NAMED like the prefix but living elsewhere --
  # routine, since controllers are often named after what they manage. Matching the
  # whole row promotes it ahead of the genuine tenant pod and spends the cap on it.
  rows="$(printf '%s\n' \
    'cozy-system   tenant-controller-abc  0/1  Running  0  5m' \
    'tenant-test   real-one               0/1  Running  0  5m' \
    'kube-system   other                  0/1  Running  0  5m')"

  out="$(printf '%s\n' "$rows" | COZY_REPORT_PREFER_NS=tenant- cozyreport_pods_prioritize)"

  first=$(printf '%s\n' "$out" | sed -n '1p' | awk '{print $2}')
  if [ "$first" != "real-one" ]; then
    echo "FAIL: expected the tenant-namespace pod first, got '$first' (matched the whole row)"
    printf '%s\n' "$out"
    false
  fi
  # Reordering never drops.
  [ "$(printf '%s\n' "$out" | grep -c .)" -eq 3 ]
}

@test "kubevirt not ready drops the blank line an empty selection arrives as" {
  # An unguarded predicate emits a blank row straight back: `$NF` on an empty line
  # is `$0`, which is "" and therefore not "True". The caller's
  # `while read NAMESPACE NAME` then runs once with both empty and makes a
  # directory named for nothing.
  #
  # Counted with `wc -l` and not `grep -c .`, and deliberately not routed through
  # `$(...)` first: a blank line is exactly what both of those erase. `grep -c .`
  # counts non-empty lines, and command substitution strips the trailing newline,
  # so the obvious spelling of this assertion passes whether or not the guard is
  # there: that spelling passes with the guard removed.
  count=$(printf '\n' | cozyreport_kubevirt_not_ready | wc -l | tr -d ' ')

  [ "$count" -eq 0 ]
}

# One `kubectl get svc -A --no-headers` snapshot, covering every shape the
# EXTERNAL-IP column takes: a LoadBalancer waiting for an address, one that got
# it, a ClusterIP, and a headless service whose CLUSTER-IP is `None`.
svc_rows="$(printf '%s\n' \
  'default       kubernetes   ClusterIP     10.96.0.1      <none>          443/TCP    30d' \
  'tenant-a      ingress      LoadBalancer  10.96.14.7     <pending>       80:31000/TCP,443:31001/TCP  4m' \
  'tenant-b      ingress      LoadBalancer  10.96.14.9     198.51.100.10   80:31002/TCP  6d' \
  'cozy-system   etcd         ClusterIP     None           <none>          2379/TCP   30d')"

@test "the object loops admit at most their cap and name what they left out" {
  rows=""
  i=1
  while [ "$i" -le 30 ]; do
    rows="$rows
tenant-a  obj-$i  <pending>"
    i=$((i + 1))
  done

  tmp=$(mktemp -d)
  REPORT_DIR="$tmp" mkdir -p "$tmp/kubernetes"
  out=$(printf '%s\n' "$rows" | REPORT_DIR="$tmp" cozyreport_admit_objects "services")

  # Thirty selected, the cap admits its own number, and the overflow is named in
  # the artifact rather than dropped. The pod walk has had both bounds since this
  # change; the object loops had only a per-read ceiling, which bounds one read
  # and not their number -- and correcting the services selector is what made a
  # large selection reachable in the first place.
  [ "$(printf '%s\n' "$out" | grep -c .)" -eq "$COZYREPORT_OBJECTS_DEFAULT" ]
  # "admitted", not "collected". This helper runs before the loop reads anything,
  # so it can only report what the cap let through; on an expired budget the walk
  # then collects none of them, and a note saying "25 collected" would name
  # twenty-five directories that do not exist.
  grep -q "services: 30 selected, $COZYREPORT_OBJECTS_DEFAULT admitted by the cap" \
    "$tmp/kubernetes/COLLECTION-TRUNCATED.txt"
  if grep -q "$COZYREPORT_OBJECTS_DEFAULT collected" "$tmp/kubernetes/COLLECTION-TRUNCATED.txt"; then
    echo "FAIL: the cap note claims objects were collected before the walk ran"
    cat "$tmp/kubernetes/COLLECTION-TRUNCATED.txt"
    false
  fi
  rm -rf "$tmp"
}

@test "a pod walk keeps the pods a dying list managed to name" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"
  rows="$(printf '%s\n' 'tenant-a  broken-0  0/1  CrashLoopBackOff  7  4m' \
                        'tenant-a  broken-1  0/1  CrashLoopBackOff  5  4m')"

  PATH="$tmp:$PATH" STUB_PODLIST_FAIL=partial STUB_POD_ROWS="$rows" \
    cozyreport_collect_broken_pods "$tmp/pods"

  # Before the read was moved into a variable, these rows flowed down the pipe and
  # were collected. Keeping the exit status is the point of that move; throwing
  # the rows away was not, and the note then said nothing was ever looked at while
  # kubectl had just named two crash-looping tenants.
  [ -d "$tmp/pods/tenant-a/broken-0" ] || {
    echo "FAIL: a pod the list named before dying was not collected"
    find "$tmp/pods" -maxdepth 3 | head
    false
  }
  grep -q 'before it stopped, and there may be others' "$tmp/pods/COLLECTION-FAILED.txt" || {
    echo "FAIL: a partial list is not described as partial"
    cat "$tmp/pods/COLLECTION-FAILED.txt"
    false
  }
  if grep -q 'nothing was ever looked at' "$tmp/pods/COLLECTION-FAILED.txt"; then
    echo "FAIL: a partial list claims nothing was looked at"
    false
  fi
  rm -rf "$tmp"
}

@test "a selector keeps the objects a dying read managed to name" {
  tmp=$(mktemp -d); mkdir -p "$tmp/kubernetes"
  fake="$tmp/fakectl"
  printf '#!/bin/sh\nprintf "tenant-a  ingress  <pending>\\n"\necho "error: unexpected EOF" >&2\nexit 1\n' > "$fake"
  chmod +x "$fake"

  out=$(REPORT_DIR="$tmp" COZYREPORT_BOUND="" cozyreport_select_objects "services" "$fake")

  printf '%s\n' "$out" | grep -q 'ingress' || {
    echo "FAIL: the row the read named before dying was dropped"; false
  }
  grep -q 'before it stopped, and there may be others' "$tmp/kubernetes/COLLECTION-FAILED.txt" || {
    echo "FAIL: the partial selection is described as empty"
    cat "$tmp/kubernetes/COLLECTION-FAILED.txt"
    false
  }
  rm -rf "$tmp"
}

@test "a summary read keeps the rows a dying read managed to print" {
  tmp=$(mktemp -d)
  summary_stub_dir "$tmp"
  rows="$(printf '%s\n' 'tenant-a  hr-1  4m  False  install failed one' \
                        'tenant-a  hr-2  4m  False  install failed two')"

  out="$(PATH="$tmp:$PATH" STUB_CRD_PRESENT=1 STUB_HR_ROWS="$rows" STUB_HR_FAIL=partial \
    sh "$HACK_DIR/cozyreport-summary.sh" 2>/dev/null)"

  # The first section of the first file a triager opens, and `kubectl get hr -A`
  # on a broken cluster is the read most likely to be cut off at the 30s ceiling.
  printf '%s\n' "$out" | grep -q 'hr-1' || {
    echo "FAIL: rows printed before the failure were discarded"; printf '%s\n' "$out"; false
  }
  printf '%s\n' "$out" | grep -q 'before it stopped, and there may be others' || {
    echo "FAIL: a partial read is described as an empty section"
    printf '%s\n' "$out"
    false
  }
  rm -rf "$tmp"
}

@test "a selector read that never returned is not rendered as an empty cluster" {
  tmp=$(mktemp -d); mkdir -p "$tmp/kubernetes"
  fake="$tmp/fakectl"; printf '#!/bin/sh\nexit 124\n' > "$fake"; chmod +x "$fake"

  out=$(REPORT_DIR="$tmp" COZYREPORT_BOUND="" cozyreport_select_objects "services" "$fake")

  # A pipeline in dash has no pipefail, so piping the selector straight into the
  # filter throws its exit code away: a read killed at its own deadline produces
  # no rows, no error and no marker, and the section renders exactly like a
  # cluster that has nothing pending. That is the same ambiguity the pod list is
  # read separately to avoid.
  [ -z "$out" ] || { echo "FAIL: a failed selector produced rows"; false; }
  grep -q 'services: the selection read did not return (exit 124)' \
    "$tmp/kubernetes/COLLECTION-FAILED.txt"
  rm -rf "$tmp"
}

@test "a selector failing before its marker directory exists still records it" {
  # Every note this function writes goes under $REPORT_DIR/kubernetes, and that
  # directory is created part way down the report -- after the cozystack and
  # cert-manager sections, both of which now select through here. A redirect into
  # a directory that does not exist fails, `|| true` swallows the failure, and the
  # marker is gone: the function whose whole job is that a failed selection cannot
  # pass for an empty cluster would have failed silently in exactly the sections
  # that run first.
  #
  # The directory is deliberately NOT created here, which is the whole test.
  tmp=$(mktemp -d)
  fake="$tmp/fakectl"; printf '#!/bin/sh\nexit 124\n' > "$fake"; chmod +x "$fake"

  REPORT_DIR="$tmp" COZYREPORT_BOUND="" cozyreport_select_objects "cozystack configmaps" "$fake" >/dev/null

  [ -f "$tmp/kubernetes/COLLECTION-FAILED.txt" ] || {
    echo "FAIL: the marker was lost because its directory did not exist yet"
    ls -R "$tmp"
    false
  }
  grep -q 'cozystack configmaps' "$tmp/kubernetes/COLLECTION-FAILED.txt"
  rm -rf "$tmp"
}

@test "an empty listing says so in the file instead of arriving as zero bytes" {
  # `kubectl get` writes `No resources found` to STDERR and exits 0
  # (kubernetes/kubectl#1667), so an empty list reaches the reader as "succeeded,
  # wrote nothing, and said something". Routed beside the file, that leaves a
  # zero-byte `orders.txt` -- the exact "returned nothing, or never ran" question
  # this collector exists to answer -- plus a READ-WARNINGS.txt on every healthy
  # run, because `orders` and `challenges` are empty on any install with no ACME
  # issuer. Before these listings moved onto the bounded reader their `2>&1` put
  # the sentence in the file, where a person finds it.
  tmp=$(mktemp -d)
  fake="$tmp/fakectl"
  printf '#!/bin/sh\necho "No resources found in cozy-system namespace." >&2\nexit 0\n' > "$fake"
  chmod +x "$fake"

  COZYREPORT_BOUND="" cozyreport_read_object "$tmp/orders.txt" "$fake"

  [ -s "$tmp/orders.txt" ] || { echo "FAIL: an empty listing arrived as a zero-byte file"; false; }
  grep -q 'No resources found' "$tmp/orders.txt"
  if [ -f "$tmp/READ-WARNINGS.txt" ]; then
    echo "FAIL: an ordinary empty list filed a warnings marker on a healthy run"
    cat "$tmp/READ-WARNINGS.txt"
    false
  fi

  # The YAML side keeps the opposite rule, and for its own reason: an unprefixed
  # kubectl line in a document a parser reads costs the whole document. Fixing the
  # listing by loosening this would trade one unreadable file for another.
  COZYREPORT_BOUND="" cozyreport_read_object "$tmp/pod.yaml" "$fake"
  if grep -q '^No resources found' "$tmp/pod.yaml" 2>/dev/null; then
    echo "FAIL: a bare kubectl line landed in a YAML document"
    cat "$tmp/pod.yaml"
    false
  fi
  grep -q 'No resources found' "$tmp/READ-WARNINGS.txt"
  rm -rf "$tmp"
}

@test "a truncated controller log is marked the way a log is, not the way an object is" {
  # The bounded reader used to write objects, describes and section listings only.
  # It now also writes thirteen controller logs, and its marker was shaped for the
  # first set: a `#` prefix, which is a YAML comment and nothing at all in a log,
  # and the words "not because the object ended" about a file that is not an
  # object. The platform's controllers log JSON per line, so the prefix is the
  # part that matters -- `# [cozyreport] ...` is a line no reader of that file can
  # take.
  #
  # The marker still goes INSIDE the log, which is the other half of this test.
  # cozyreport_read_container_log appends its own TRUNCATED line into a partial
  # log, so moving this one beside the file would have two readers in one tarball
  # answering the same question two ways -- worse than either answer on its own.
  tmp=$(mktemp -d)
  exec_stub_dir "$tmp"

  STUB_EXEC_MODE=cut PATH="$tmp/bin:$PATH" \
    cozyreport_read_object "$tmp/out/controller.log" kubectl logs deploy/x --tail=2000

  grep -q 'TRUNCATED' "$tmp/out/controller.log"
  if grep -q '^# \[cozyreport\]' "$tmp/out/controller.log"; then
    echo "FAIL: a log carries the YAML-comment marker, which is not a comment in a log"
    tail -n 1 "$tmp/out/controller.log"
    false
  fi
  if grep -q 'because the object ended' "$tmp/out/controller.log"; then
    echo "FAIL: the marker on a log calls it an object"
    tail -n 1 "$tmp/out/controller.log"
    false
  fi

  # And the object side is unchanged: a YAML target still gets the prefix, or the
  # fix would have traded one unparseable file for another.
  STUB_EXEC_MODE=cut PATH="$tmp/bin:$PATH" \
    cozyreport_read_object "$tmp/out/pod.yaml" kubectl get pod -o yaml
  grep -q '^# \[cozyreport\] TRUNCATED' "$tmp/out/pod.yaml" || {
    echo "FAIL: an object lost the comment prefix its parser needs"
    tail -n 1 "$tmp/out/pod.yaml"
    false
  }
  rm -rf "$tmp"
}

@test "an empty selection does not run the loop body once with an empty name" {
  # `printf '%s\n' ""` emits a blank LINE, not nothing, so a selector that
  # succeeded with no rows used to hand its caller one empty row. Every loop that
  # existed before filtered with `NF &&` and dropped it; the loops that moved onto
  # this selector filter on a named column instead -- `$2 != "Ready"` and friends
  # -- and a blank line satisfies every one of them, because an absent field is
  # not the string it is compared against.
  #
  # The cost is not a wasted iteration. The body runs with an empty name, reads
  # `kubectl get node "" -o yaml`, and the refusal lands in the file the reader
  # opens: a `nodes/node.yaml` holding "resource name may not be empty" reads as a
  # failed read of a real node. Before the selector was wired in, kubectl printed
  # nothing on an empty list and the loop simply never ran.
  #
  # Fixed once in the selector rather than seven times in its callers: every one
  # of them routes through it, and the next loop to be added would need the guard
  # again.
  tmp=$(mktemp -d)
  fake="$tmp/fakectl"; printf '#!/bin/sh\nexit 0\n' > "$fake"; chmod +x "$fake"

  # The caller's exact shape, filter included, since the filter is half the defect.
  ran=$(REPORT_DIR="$tmp" COZYREPORT_BOUND="" cozyreport_select_objects "nodes" "$fake" |
    awk '$2 != "Ready"' |
    while read NAME _; do printf 'RAN[%s]\n' "$NAME"; done)

  [ -z "$ran" ] || {
    echo "FAIL: an empty selection still ran the loop body: $ran"
    false
  }
  rm -rf "$tmp"
}

@test "a selector note lands beside the walk it describes, not in a shared file" {
  # Twelve callers read a Kubernetes listing and their notes belong under
  # kubernetes/. The LINSTOR satellite walk does not: its bundles land under
  # linstor/error-reports/, and a note there saying the pod list came back short is
  # the difference between "three bundles because three satellites" and "three
  # because the list was cut off". Filed in the shared kubernetes file it is in a
  # directory that reader has no reason to open.
  tmp=$(mktemp -d)
  fake="$tmp/fakectl"; printf '#!/bin/sh\nexit 124\n' > "$fake"; chmod +x "$fake"

  COZYREPORT_SELECT_NOTE_DIR="$tmp/linstor/error-reports" \
    REPORT_DIR="$tmp" COZYREPORT_BOUND="" \
    cozyreport_select_objects "linstor satellites" "$fake" >/dev/null

  [ -f "$tmp/linstor/error-reports/COLLECTION-FAILED.txt" ] || {
    echo "FAIL: the note did not land beside the walk"; ls -R "$tmp"; false; }
  if [ -f "$tmp/kubernetes/COLLECTION-FAILED.txt" ]; then
    echo "FAIL: the note also went to the shared kubernetes file"; false; fi

  # And the default is unchanged for every other caller.
  COZYREPORT_SELECT_NOTE_DIR="" REPORT_DIR="$tmp" COZYREPORT_BOUND="" \
    cozyreport_select_objects "nodes" "$fake" >/dev/null
  grep -q 'nodes' "$tmp/kubernetes/COLLECTION-FAILED.txt"
  rm -rf "$tmp"
}

@test "a selector that succeeds leaves no marker directory behind it" {
  # The other half: creating the directory on every call would put an empty
  # kubernetes/ into reports that never needed one, and an empty directory in this
  # tree is a thing a reader interprets.
  tmp=$(mktemp -d)
  fake="$tmp/fakectl"; printf '#!/bin/sh\nprintf "ns name\\n"\n' > "$fake"; chmod +x "$fake"

  out=$(REPORT_DIR="$tmp" COZYREPORT_BOUND="" cozyreport_select_objects "nodes" "$fake")

  [ "$out" = "ns name" ]
  if [ -d "$tmp/kubernetes" ]; then
    echo "FAIL: a clean selection created the failure-marker directory anyway"
    false
  fi
  rm -rf "$tmp"
}

@test "a refused selector carries kubectl's reason, not only its exit code" {
  tmp=$(mktemp -d); mkdir -p "$tmp/kubernetes"
  fake="$tmp/fakectl"
  # An RBAC refusal, deliberately NOT the "no such resource type" wording. Both
  # exit 1, and "the selection read did not return (exit 1)" names a symptom with
  # no cause -- but they are opposite findings: one is the cluster correctly saying
  # it does not run KubeVirt, the other is this report being unable to look. The
  # not-served case has its own test and lands in COLLECTION-NOTES.txt; this one is
  # a real collection failure and has to keep both the marker and the message.
  printf '#!/bin/sh\necho "Error from server (Forbidden): virtualmachines.kubevirt.io is forbidden: User \\"system:serviceaccount:cozy-system:reporter\\" cannot list resource \\"virtualmachines\\" in API group \\"kubevirt.io\\" at the cluster scope" >&2\nexit 1\n' > "$fake"
  chmod +x "$fake"

  REPORT_DIR="$tmp" COZYREPORT_BOUND="" cozyreport_select_objects "virtualmachines" "$fake" >/dev/null

  grep -q "is forbidden" "$tmp/kubernetes/COLLECTION-FAILED.txt" || {
    echo "FAIL: the refusal reached the artifact without its reason"
    cat "$tmp/kubernetes/COLLECTION-FAILED.txt" 2>/dev/null || echo "(no COLLECTION-FAILED.txt at all)"
    false
  }
  # And it must NOT be filed as an ordinary absence: a refusal this report cannot
  # see past is exactly what the FAILED marker exists to name.
  if [ -f "$tmp/kubernetes/COLLECTION-NOTES.txt" ]; then
    echo "FAIL: an RBAC refusal was recorded as a resource type the cluster does not serve"
    cat "$tmp/kubernetes/COLLECTION-NOTES.txt"
    false
  fi
  rm -rf "$tmp"
}

@test "a selector killed by its own bound names the cutoff, not a refusal" {
  tmp=$(mktemp -d); mkdir -p "$tmp/kubernetes"
  fake="$tmp/fakectl"; printf '#!/bin/sh\nexit 124\n' > "$fake"; chmod +x "$fake"

  REPORT_DIR="$tmp" cozyreport_select_objects "services" "$fake" >/dev/null

  # Timeout branch first, as everywhere else: a read killed before kubectl wrote
  # anything has an empty stderr by construction, so leading with "without a
  # message" would report the accident rather than the cause.
  grep -q 'cut off by' "$tmp/kubernetes/COLLECTION-FAILED.txt" || {
    echo "FAIL: a killed selector is not described as cut off"
    cat "$tmp/kubernetes/COLLECTION-FAILED.txt"
    false
  }
  if grep -q 'without a message' "$tmp/kubernetes/COLLECTION-FAILED.txt"; then
    echo "FAIL: a killed selector is reported as a silent refusal"
    false
  fi
  rm -rf "$tmp"
}

@test "a kind this cluster does not serve is a note, not a collection failure" {
  tmp=$(mktemp -d); mkdir -p "$tmp/kubernetes"
  fake="$tmp/fakectl"
  # What kubectl says on an install without KubeVirt. `kubectl get vm -A` exits 1
  # there, and that is the cluster answering correctly, not a read that failed.
  printf '#!/bin/sh\necho %s >&2\nexit 1\n' \
    "'error: the server doesn'\\''t have a resource type \"vm\"'" > "$fake"
  chmod +x "$fake"

  REPORT_DIR="$tmp" COZYREPORT_BOUND="" cozyreport_select_objects "virtualmachines" "$fake" >/dev/null

  # COLLECTION-FAILED.txt is the filename a triager scans the tree for. Filing an
  # ordinary absence under it makes the name cry wolf on every install without
  # KubeVirt, and a marker that fires on healthy clusters is one a reader learns
  # to skip -- which costs the runs where it names something real.
  if [ -f "$tmp/kubernetes/COLLECTION-FAILED.txt" ]; then
    echo "FAIL: a resource type this cluster does not serve was recorded as a failed read"
    cat "$tmp/kubernetes/COLLECTION-FAILED.txt"
    false
  fi
  grep -q 'does not serve that resource type' "$tmp/kubernetes/KIND-NOT-SERVED.txt" || {
    echo "FAIL: the absence was not recorded anywhere at all"
    ls -la "$tmp/kubernetes"
    false
  }
  # kubectl's own words are kept: "absent" and "RBAC refused the list" both reach
  # this branch only through what kubectl said, so the sentence has to carry it.
  grep -q "resource type" "$tmp/kubernetes/KIND-NOT-SERVED.txt"
  # And NOT under COLLECTION-NOTES.txt, which one directory down already means
  # "a knob value the collector could not use". Two subjects behind one filename
  # is a collision the marker guard cannot see, because it matches basenames and
  # the doc names that file for the other meaning.
  if [ -f "$tmp/kubernetes/COLLECTION-NOTES.txt" ]; then
    echo "FAIL: reused the knob-note filename for a statement about the cluster"
    cat "$tmp/kubernetes/COLLECTION-NOTES.txt"
    false
  fi
  rm -rf "$tmp"
}

@test "a selector that returned nothing says nothing at all" {
  tmp=$(mktemp -d); mkdir -p "$tmp/kubernetes"
  fake="$tmp/fakectl"; printf '#!/bin/sh\nexit 0\n' > "$fake"; chmod +x "$fake"

  REPORT_DIR="$tmp" COZYREPORT_BOUND="" cozyreport_select_objects "services" "$fake" >/dev/null

  # The converse: a healthy cluster with no pending LoadBalancer must not leave a
  # failure marker. Reporting every empty selection as a failure would make the
  # marker meaningless, which is the same defect in the other direction.
  if [ -f "$tmp/kubernetes/COLLECTION-FAILED.txt" ]; then
    echo "FAIL: an empty selection was recorded as a failed read"
    cat "$tmp/kubernetes/COLLECTION-FAILED.txt"
    false
  fi
  rm -rf "$tmp"
}

@test "a log cut short at both ends names both bounds, not just the visible one" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  PATH="$tmp:$PATH" STUB_LOG_MODE=partial cozyreport_collect_pod ns hung-0 "$tmp/out"

  # Two bounds applied to this file: `--tail` dropped its oldest lines and the cut
  # dropped its newest. The end-truncation marker was recorded and the tail bound
  # was not, because the tail note sat in the branch reached only after a clean
  # read. Naming one while the other stays silent is worse than naming neither: it
  # tells the reader the file begins where the container did.
  grep -q '^\[cozyreport\] TRUNCATED' "$tmp/out/logs-app.txt"
  grep -q 'holds at most the last 2000 lines' "$tmp/out/CONTAINER-LOGS-BOUND.txt" || {
    echo "FAIL: a partial log does not record the tail bound it was read under"
    ls "$tmp/out"
    false
  }
  rm -rf "$tmp"
}

@test "a walk stopped by the object budget says so instead of vanishing" {
  tmp=$(mktemp -d); mkdir -p "$tmp/kubernetes"

  REPORT_DIR="$tmp" cozyreport_objects_budget_note "services"

  # The cap has a note and the budget did not, so objects removed by the clock
  # disappeared with nothing recording it -- and when both bounds were in play the
  # cap's note took the blame for omissions the clock caused. Every other bound in
  # this tree names itself; this one was silent.
  grep -q 'services: the .*s object budget elapsed' "$tmp/kubernetes/COLLECTION-TRUNCATED.txt"
  rm -rf "$tmp"
}

@test "an object loop under its cap reports no overflow at all" {
  tmp=$(mktemp -d)
  mkdir -p "$tmp/kubernetes"
  out=$(printf 'tenant-a  only-one  <pending>\n' | REPORT_DIR="$tmp" cozyreport_admit_objects "services")

  [ "$(printf '%s\n' "$out" | grep -c .)" -eq 1 ]
  if [ -f "$tmp/kubernetes/COLLECTION-TRUNCATED.txt" ]; then
    echo "FAIL: claimed an overflow that did not happen"
    cat "$tmp/kubernetes/COLLECTION-TRUNCATED.txt"
    false
  fi
  rm -rf "$tmp"
}

@test "the object walk stops on its budget instead of the job's outer limit" {
  # The deadline is checked between objects, never inside one, so a file that
  # exists is a file that was read whole. Past the deadline the loop admits
  # nothing further.
  COZYREPORT_OBJECTS_DEADLINE=$(( $(date +%s) - 1 ))
  cozyreport_objects_deadline_passed || { echo "FAIL: an expired budget is not seen as expired"; false; }

  COZYREPORT_OBJECTS_DEADLINE=$(( $(date +%s) + 600 ))
  if cozyreport_objects_deadline_passed; then
    echo "FAIL: a live budget is reported as spent"
    false
  fi

  # And with no deadline set at all -- a caller driving the helpers directly --
  # the walk is not silently stopped on its first object.
  unset COZYREPORT_OBJECTS_DEADLINE
  if cozyreport_objects_deadline_passed; then
    echo "FAIL: an unset budget stops the walk"
    false
  fi
}

@test "a LoadBalancer waiting for an address is selected by its external column" {
  out="$(printf '%s\n' "$svc_rows" | cozyreport_svc_pending)"

  # `<pending>` is printed only for EXTERNAL-IP, which is $5. The selection read
  # $4 -- CLUSTER-IP, which holds an address, `None` or `<none>` and never
  # `<pending>` -- so it matched nothing on any cluster and the services directory
  # has been empty since the section was written, which a reader takes to mean no
  # LoadBalancer is stuck. A tenant whose ingress never gets an address is exactly
  # the failure this section is for.
  [ "$(printf '%s\n' "$out" | wc -l | tr -d ' ')" -eq 1 ]
  case "$out" in
    "tenant-a      ingress"*) ;;
    *) echo "FAIL: selected the wrong row: $out"; false ;;
  esac
}

@test "a service that already has an address or never wanted one is left alone" {
  # The converse of the test above: widening the column is only correct if it does
  # not also start collecting every healthy service. `None` and `<none>` are the
  # two spellings that would match a sloppier test for "no address yet".
  for row in \
    'default       kubernetes   ClusterIP     10.96.0.1      <none>         443/TCP   30d' \
    'tenant-b      ingress      LoadBalancer  10.96.14.9     198.51.100.10  80:31002/TCP  6d' \
    'cozy-system   etcd         ClusterIP     None           <none>         2379/TCP  30d'
  do
    if [ -n "$(printf '%s\n' "$row" | cozyreport_svc_pending)" ]; then
      echo "FAIL: collected a service that is not waiting: $row"
      false
    fi
  done
}

@test "a service merely named like the pending marker is not collected" {
  # Field-scoped, not an unanchored match on the row. A name cannot literally be
  # `<pending>` on a cluster, but the reason this selection is written as a column
  # test rather than a substring test is the same reason the pod prioritiser is
  # field-scoped, and a whole-row match would pass every other test in this file.
  row='tenant-a      <pending>    ClusterIP     10.96.14.7     <none>    80/TCP   4m'

  if [ -n "$(printf '%s\n' "$row" | cozyreport_svc_pending)" ]; then
    echo "FAIL: matched the name column"
    false
  fi
}

# --------------------------------------------------------------------------- #
# cozyreport_read_object -- the bounded whole-object read behind pod.yaml,       #
# vm.yaml, vmi.yaml and the describes beside them. Three of those four are read  #
# by a parser rather than by a person, which is what decides where stderr goes.  #
#                                                                               #
# cozytest.sh ends an @test at the first bare `}`, so the stub builder lives at  #
# top level.                                                                     #
# --------------------------------------------------------------------------- #

# A stand-in for kubectl that writes a chosen mix of stdout, stderr and exit
# status. read_object runs whatever argv it is handed, so nothing here has to be
# named kubectl.
object_stub() {
  cat > "$1" <<'STUB'
#!/bin/sh
[ -z "$STUB_OUT" ] || printf '%s\n' "$STUB_OUT"
[ -z "$STUB_ERR" ] || printf '%s\n' "$STUB_ERR" >&2
exit "${STUB_RC:-0}"
STUB
  chmod +x "$1"
}

@test "a warning on a read that succeeded stays out of the object it belongs to" {
  tmp=$(mktemp -d)
  object_stub "$tmp/fakectl"

  COZYREPORT_BOUND="" \
  STUB_OUT="apiVersion: v1
kind: Pod" \
  STUB_ERR="Warning: v1 Pod is deprecated in this build" \
  STUB_RC=0 \
    cozyreport_read_object "$tmp/pod.yaml" "$tmp/fakectl"

  # `2>&1` put the warning on line 1 and turned a complete object into a file no
  # parser accepts -- and pod.yaml, vm.yaml and vmi.yaml are read by yq, not by
  # eye. The same rule already applied two functions over in the log reader and in
  # the summary's own read; this one was the exception, in the place where merging
  # costs the most.
  head -n 1 "$tmp/pod.yaml" | grep -q '^apiVersion:'
  if grep -q 'Warning:' "$tmp/pod.yaml"; then
    echo "FAIL: the warning landed in the object file"
    cat "$tmp/pod.yaml"
    false
  fi
  rm -rf "$tmp"
}

@test "a refusal with no object to show is still what the file holds" {
  tmp=$(mktemp -d)
  object_stub "$tmp/fakectl"

  COZYREPORT_BOUND="" \
  STUB_OUT="" \
  STUB_ERR='Error from server (NotFound): pods "gone-0" not found' \
  STUB_RC=1 \
    cozyreport_read_object "$tmp/pod.yaml" "$tmp/fakectl"

  # The converse of the test above, and the reason stderr is diverted rather than
  # discarded: with no object to keep, kubectl's reason IS the evidence, and an
  # empty pod.yaml says nothing about why. Keeping only the successful case would
  # have traded one silent file for another.
  grep -q 'pods "gone-0" not found' "$tmp/pod.yaml"
  rm -rf "$tmp"
}

@test "an object read that died part way through says so instead of looking whole" {
  tmp=$(mktemp -d)
  object_stub "$tmp/fakectl"

  COZYREPORT_BOUND="" \
  STUB_OUT="apiVersion: v1
kind: Pod
metadata:" \
  STUB_ERR='error: unexpected EOF' \
  STUB_RC=1 \
    cozyreport_read_object "$tmp/pod.yaml" "$tmp/fakectl"

  # Diverting stderr opens a third outcome that the merged version could not have:
  # kubectl wrote part of the object and then failed on its own terms. Without a
  # marker the file is a valid-looking prefix and nothing says the rest was never
  # read -- exactly the ambiguity the timeout marker exists to remove, arrived at
  # by a different route. The reason rides on the marker line, not in the body,
  # because the body is what gets parsed.
  head -n 1 "$tmp/pod.yaml" | grep -q '^apiVersion:'
  # Not anchored on the tool's name: the marker names whatever was run, which a
  # test of its own pins. What matters here is that a partial object says so.
  grep -q '^# \[cozyreport\] TRUNCATED.*exited 1 part way through' "$tmp/pod.yaml"
  grep -q 'unexpected EOF' "$tmp/pod.yaml"
  rm -rf "$tmp"
}

@test "a read that returned neither an object nor a message still says so" {
  tmp=$(mktemp -d)
  object_stub "$tmp/fakectl"

  # The fourth outcome, and the one that was missing: kubectl died on a signal
  # that is not 124 or 137 -- a teardown SIGTERM -- writing neither an object nor
  # a word about why. Every other classification site in these scripts covers it;
  # this one left pod.yaml and describe.txt at zero bytes with nothing in them,
  # in a directory whose emptiness e2e-testing.md reads as a claim about the pod.
  COZYREPORT_BOUND="" STUB_OUT="" STUB_ERR="" STUB_RC=143 \
    cozyreport_read_object "$tmp/pod.yaml" "$tmp/fakectl"

  [ -s "$tmp/pod.yaml" ] || { echo "FAIL: the file is empty and explains nothing"; false; }
  grep -q '^# \[cozyreport\] [a-z]* *exited 143' "$tmp/pod.yaml"
  rm -rf "$tmp"
}

@test "a lost stderr file is not turned into a claim that kubectl was silent" {
  tmp=$(mktemp -d)
  object_stub "$tmp/fakectl"

  # The other way into that branch, and it is not about the cluster at all: with
  # no writable temp dir the scratch file was never created, so stderr went to
  # /dev/null. "kubectl said nothing" would then be a statement about the machine
  # writing the report, dressed as one about the machine being reported on.
  COZYREPORT_BOUND="" TMPDIR="$tmp/nonexistent" STUB_OUT="" \
  STUB_ERR='Error from server (Forbidden): pods is forbidden' STUB_RC=1 \
    cozyreport_read_object "$tmp/pod.yaml" "$tmp/fakectl" 2>/dev/null

  grep -q 'could not be captured' "$tmp/pod.yaml"
  if grep -q 'without a message' "$tmp/pod.yaml"; then
    echo "FAIL: a lost message is reported as no message"
    cat "$tmp/pod.yaml"
    false
  fi
  rm -rf "$tmp"
}

@test "a warning on a successful read is kept beside the object, not dropped" {
  tmp=$(mktemp -d)
  object_stub "$tmp/fakectl"

  COZYREPORT_BOUND="" \
  STUB_OUT="apiVersion: v1
kind: Pod" \
  STUB_ERR="Warning: v1 Pod is deprecated in this build" \
  STUB_RC=0 \
    cozyreport_read_object "$tmp/pod.yaml" "$tmp/fakectl"

  # Diverting stderr keeps the object parseable, which is the point. But "not in
  # the object" and "not in the report" are different outcomes, and the second is
  # a silent loss of evidence in a tree whose entire claim is that there are none.
  # `2>&1` keeps the warning, at the cost of the object; a scratch file keeps both.
  if grep -q 'Warning:' "$tmp/pod.yaml"; then
    echo "FAIL: the warning is back inside the object"; false
  fi
  grep -q 'Warning: v1 Pod is deprecated' "$tmp/READ-WARNINGS.txt" || {
    echo "FAIL: the warning was dropped from the report entirely"
    ls "$tmp"
    false
  }
  rm -rf "$tmp"
}

@test "every warning on a successful read is kept, not just the last one" {
  tmp=$(mktemp -d)
  object_stub "$tmp/fakectl"

  COZYREPORT_BOUND="" \
  STUB_OUT="apiVersion: v1" \
  STUB_ERR="Warning: v1 Pod is deprecated in this build
Warning: results are partial, 3 of 7 shards answered" \
  STUB_RC=0 \
    cozyreport_read_object "$tmp/pod.yaml" "$tmp/fakectl"

  # `tail -n 1` is the right reduction for a FAILING read, where klog retry lines
  # bury the cause and one line has to be chosen. A successful read has no
  # preamble: every line is its own warning, and keeping the last one silently
  # discards the rest. Two warnings on one read is ordinary -- a deprecation plus
  # a partial-result notice is exactly what a degraded apiserver produces.
  grep -q 'v1 Pod is deprecated' "$tmp/READ-WARNINGS.txt" || {
    echo "FAIL: the first warning was dropped"; cat "$tmp/READ-WARNINGS.txt"; false
  }
  grep -q 'results are partial' "$tmp/READ-WARNINGS.txt" || {
    echo "FAIL: the second warning was dropped"; cat "$tmp/READ-WARNINGS.txt"; false
  }
  rm -rf "$tmp"
}

@test "a partial container log keeps every line kubectl said, not just the last" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  PATH="$tmp:$PATH" COZYREPORT_BOUND="" STUB_LOG_MODE=partial_rc1_multi \
    cozyreport_read_container_log "$tmp/logs-app.txt" current ns pod-1 app

  # A PARTIAL read is where kubectl has the most to say -- the reset, the refusal
  # that stopped it mid-stream -- and it was the one case that kept only the last
  # line, inside the marker, dropping every other line. The sibling capture in
  # hack/e2e-capture-previous-logs.sh already copies stderr whole regardless of
  # how the read ended; two collectors in one tarball answering the same question
  # two ways is worse than either answer on its own.
  grep -q 'results are partial' "$tmp/READ-WARNINGS.txt" || {
    echo "FAIL: a partial read dropped every stderr line but the last"
    [ -f "$tmp/READ-WARNINGS.txt" ] && cat "$tmp/READ-WARNINGS.txt" || echo "(no READ-WARNINGS.txt at all)"
    false
  }
  grep -q 'unexpected EOF' "$tmp/READ-WARNINGS.txt"
  # The exit status is named, so the file does not read as a clean read that
  # merely carried a warning.
  grep -q 'exited 1 part way through' "$tmp/READ-WARNINGS.txt"
  # The log itself stays machine-readable: the warnings live beside it, and the
  # partial log keeps its own TRUNCATED marker.
  if grep -q 'results are partial' "$tmp/logs-app.txt"; then
    echo "FAIL: a warning line landed inside the log a parser reads"
    cat "$tmp/logs-app.txt"
    false
  fi
  grep -q 'the decisive line' "$tmp/logs-app.txt"
  rm -rf "$tmp"
}

@test "a clean read that returned no object keeps the warning out of it" {
  tmp=$(mktemp -d)
  object_stub "$tmp/fakectl"

  COZYREPORT_BOUND="" STUB_OUT="" STUB_ERR="Warning: some deprecation notice" STUB_RC=0 \
    cozyreport_read_object "$tmp/pod.yaml" "$tmp/fakectl"

  # rc 0, no object, and kubectl still spoke. Copying stderr into the file on the
  # strength of "no stdout" alone writes an unprefixed `Warning:` line where an
  # object belongs -- unparseable, and unexplained, because the emptiness checks
  # further down see a non-empty file and stay quiet. The refusal path is the one
  # that earns a copy into the file; a warning on a successful read is not it.
  if grep -q 'deprecation notice' "$tmp/pod.yaml" 2>/dev/null; then
    echo "FAIL: a warning landed inside the object, with no marker to explain it"
    cat "$tmp/pod.yaml"
    false
  fi
  grep -q 'deprecation notice' "$tmp/READ-WARNINGS.txt" || {
    echo "FAIL: the warning was dropped instead of kept beside the object"
    [ -f "$tmp/READ-WARNINGS.txt" ] && cat "$tmp/READ-WARNINGS.txt" || echo "(no READ-WARNINGS.txt)"
    false
  }
  grep -q 'returned no object' "$tmp/READ-WARNINGS.txt"
  rm -rf "$tmp"
}

@test "a cut-off container read is not attributed to kubectl in the warnings file" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  PATH="$tmp:$PATH" COZYREPORT_BOUND="" STUB_LOG_MODE=partial_rc1_multi_124 \
    cozyreport_read_container_log "$tmp/logs-app.txt" current ns pod-1 app

  # 124 is this script's own clock, not a decision kubectl made. The TRUNCATED
  # marker inside the log names the timeout; the warnings file beside it described
  # the same read as "kubectl exited 124 part way through". Two files in one pod
  # directory, one read, two mechanisms -- and only one of them was observed.
  grep -q 'cut off by' "$tmp/READ-WARNINGS.txt" || {
    echo "FAIL: a timeout is reported as a kubectl exit in the warnings file"
    cat "$tmp/READ-WARNINGS.txt"
    false
  }
  if grep -q 'kubectl exited 124' "$tmp/READ-WARNINGS.txt"; then
    echo "FAIL: the warnings file contradicts the marker inside the log"
    cat "$tmp/READ-WARNINGS.txt"
    false
  fi
  grep -q 'cut off by' "$tmp/logs-app.txt"
  rm -rf "$tmp"
}

@test "a clean container read with no log keeps the warning beside it, not in it" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  PATH="$tmp:$PATH" COZYREPORT_BOUND="" STUB_LOG_MODE=warned_empty \
    cozyreport_read_container_log "$tmp/logs-app.txt" current ns pod-1 app

  # Same shape one function down. An unprefixed kubectl line inside a container log
  # is indistinguishable from a line the container wrote, which is precisely the
  # fabricated cluster fact this tree exists to prevent.
  if grep -q 'results are partial' "$tmp/logs-app.txt"; then
    echo "FAIL: a warning landed inside the container log"
    cat "$tmp/logs-app.txt"
    false
  fi
  grep -q 'results are partial' "$tmp/READ-WARNINGS.txt" || {
    echo "FAIL: the warning was dropped"
    [ -f "$tmp/READ-WARNINGS.txt" ] && cat "$tmp/READ-WARNINGS.txt" || echo "(none)"
    false
  }
  # The WORDING, not just the routing. "returned this log" about a read that
  # returned none contradicts the placeholder written into the log file for the
  # same read, and this is the file a triager opens first because it names the
  # other one. The object reader's twin above asserts its own third-case wording
  # for exactly this reason.
  grep -q 'returned no log, exited 0' "$tmp/READ-WARNINGS.txt" || {
    echo "FAIL: a read that returned nothing is described as having returned the log"
    cat "$tmp/READ-WARNINGS.txt"
    false
  }
  # The placeholder must not claim kubectl exited "without error" when it spoke.
  if grep -q 'without output or error' "$tmp/logs-app.txt"; then
    echo "FAIL: claimed a silent exit in the same run that recorded a message"
    cat "$tmp/logs-app.txt"
    false
  fi
  grep -q 'READ-WARNINGS.txt' "$tmp/logs-app.txt"
  rm -rf "$tmp"
}

@test "a read with nothing on stderr leaves no warnings file at all" {
  tmp=$(mktemp -d)
  object_stub "$tmp/fakectl"

  COZYREPORT_BOUND="" STUB_OUT="apiVersion: v1" STUB_ERR="" STUB_RC=0 \
    cozyreport_read_object "$tmp/pod.yaml" "$tmp/fakectl"

  # The converse: an empty warnings file beside every clean read would train the
  # reader to ignore the name, which costs exactly as much as not writing it.
  if [ -f "$tmp/READ-WARNINGS.txt" ]; then
    echo "FAIL: a clean read left a warnings file"
    cat "$tmp/READ-WARNINGS.txt"
    false
  fi
  rm -rf "$tmp"
}

@test "a truncation marker on an object file does not cost the parser the object" {
  tmp=$(mktemp -d)
  object_stub "$tmp/fakectl"

  # kubectl can finish writing a whole object and still exit non-zero. The marker
  # then lands after a complete mapping, and a BARE line there is a second
  # top-level node, not a note: the file stops parsing. That is the same defect
  # diverting stderr was meant to remove, reintroduced by the fix for it -- so the
  # marker is written as a YAML comment. It stays visible to a person and invisible
  # to yq, which is what reads these three files.
  # Held in a variable rather than only in the call's environment: the comparison
  # below needs the same bytes, and an env prefix on a function call does not
  # outlive it.
  obj='apiVersion: v1
kind: Pod
metadata:
  name: broken-0'

  COZYREPORT_BOUND="" \
  STUB_OUT="$obj" \
  STUB_ERR='error: unexpected EOF' \
  STUB_RC=1 \
    cozyreport_read_object "$tmp/pod.yaml" "$tmp/fakectl"

  grep -q '^# \[cozyreport\] TRUNCATED' "$tmp/pod.yaml"

  # The property, stated without a parser: strip the comment lines and what is
  # left must be byte-for-byte the object kubectl wrote. That IS the proof, not a
  # weaker stand-in for one: the fixture is a valid YAML document, comments are
  # the one construct YAML defines as ignorable, and a file differing from a valid
  # document only by added comment lines is therefore valid too. Any change to the
  # body -- including a marker glued onto a partial final line -- breaks the
  # comparison. A bare marker survives
  # that strip and the comparison fails, which is exactly the regression. Written
  # this way rather than by loading the file, because a check that runs only when
  # the host happens to ship a YAML parser is not a check on the hosts that do
  # not -- it silently degrades to "the line starts with a hash", which is the
  # weaker half and passes even when the object is unloadable.
  printf '%s\n' "$obj" > "$tmp/expected.yaml"
  grep -v '^#' "$tmp/pod.yaml" > "$tmp/stripped.yaml"
  if ! cmp -s "$tmp/expected.yaml" "$tmp/stripped.yaml"; then
    echo "FAIL: the marker changed what a parser would see"
    echo "--- expected ---"; cat "$tmp/expected.yaml"
    echo "--- actual, comments stripped ---"; cat "$tmp/stripped.yaml"
    false
  fi

  # And when a parser is available, load it for real. This is the belt to the
  # braces above, not the assertion the test rests on.
  if command -v yq >/dev/null 2>&1; then
    yq --exit-status '.metadata.name == "broken-0"' "$tmp/pod.yaml" >/dev/null 2>&1 || {
      echo "FAIL: the marked object no longer parses"
      cat "$tmp/pod.yaml"
      false
    }
  fi
  rm -rf "$tmp"
}

# --------------------------------------------------------------------------- #
# cozyreport_collect_pod -- everything the report keeps about one broken pod.    #
# Driven against a stub kubectl (and a stub `timeout` that records its argv), so #
# the artifact these tests assert on is the one a reader of a failed run gets.   #
# The placeholder text is the ONLY evidence for a pod that logged nothing, and   #
# the --tail/timeout bounds are what stop a widened pod selection from costing   #
# the whole report, so both are pinned here rather than left to review.          #
#                                                                               #
# cozytest.sh ends an @test at the first bare `}`, so the stub builder lives at  #
# top level rather than nested inside a test.                                    #
# --------------------------------------------------------------------------- #

collect_stub_dir() {
  _sd="$1"
  cat > "$_sd/kubectl" <<'STUB'
#!/bin/sh
for a in "$@"; do
  case "$a" in
    jsonpath*)
      case "${STUB_JSONPATH_FAIL:-}" in
        # Killed by its wrapper before writing: no stderr, exit 124.
        timeout) exit 124 ;;
        # Refused without saying why: not a timeout, and nothing to quote.
        silent) exit 1 ;;
        # Named some containers and then failed: the list is a prefix of the
        # pod's containers, not the pod's containers.
        partial)
          printf 'app'
          echo 'error: unexpected EOF' >&2
          exit 1 ;;
        '') ;;
        noisy)
          # Discovery is cold, so kubectl retries the API group list and prefixes
          # the reason with one klog line per attempt. A single-pod read goes
          # through the same discovery path as the cluster-wide one.
          i=1
          while [ "$i" -le 3 ]; do
            echo 'E0728 11:51:48.306797   82827 memcache.go:265] "Unhandled Error" err="couldn'"'"'t get current server API group list: Get \"https://192.0.2.1:6443/api?timeout=32s\": dial tcp: lookup timed out after some considerable number of characters"' >&2
            i=$((i + 1))
          done
          echo 'Error from server (NotFound): pods "gone-0" not found' >&2
          exit 1 ;;
        *)
          echo 'Error from server (NotFound): pods "gone-0" not found' >&2
          exit 1 ;;
      esac
      printf '%s' "${STUB_CONTAINERS-app}"
      exit 0 ;;
  esac
done
case "$*" in
  *"get pod -A --no-headers"*)
    case "${STUB_PODLIST_FAIL:-}" in
      # Killed by its own wrapper before it could write: no stderr, exit 124.
      timeout) exit 124 ;;
      # Refused with nothing on stderr: the case that must NOT be described in
      # terms of the read timeout.
      silent) exit 1 ;;
      # Named some pods and then died: a partial list, not an absent one.
      partial)
        printf '%s\n' "${STUB_POD_ROWS:-}"
        echo 'error: unexpected EOF' >&2
        exit 1 ;;
      '') ;;
      # A failing cluster-wide list is preceded by one klog discovery-retry line
      # per attempt, each long enough to survive trimming and bury the cause.
      *)
        i=1
        while [ "$i" -le 3 ]; do
          echo 'E0728 11:51:48.306797   82827 memcache.go:265] "Unhandled Error" err="couldn'"'"'t get current server API group list: Get \"https://192.0.2.1:6443/api?timeout=32s\": dial tcp: lookup timed out after some considerable number of characters"' >&2
          i=$((i + 1))
        done
        echo 'Error from server (Forbidden): pods is forbidden' >&2
        exit 1 ;;
    esac
    printf '%s\n' "${STUB_POD_ROWS:-}"
    exit 0 ;;
esac
case "$1" in
  logs)
    printf '%s\n' "$*" >> "$STUB_ARGV"
    case "${STUB_LOG_MODE:-text}" in
      empty) exit 0 ;;
      error) echo 'Error from server (BadRequest): container "app" is waiting to start: ImagePullBackOff' >&2; exit 1 ;;
      # Exits 0, writes NO log, and still warns. The combination none of the other
      # modes produce, and the one all three had_stdout branches got wrong.
      warned_empty) echo 'Warning: results are partial, 3 of 7 shards answered' >&2; exit 0 ;;
      # Partial stream, a warning, and OUR OWN clock firing: the combination where
      # the status must not be attributed to kubectl.
      partial_rc1_multi_124)
        printf 'startup line\nthe decisive line\n'
        echo 'Warning: results are partial, 3 of 7 shards answered' >&2
        exit 124 ;;
      # Slow enough for the collection budget to run out mid-pod.
      slow) sleep "${STUB_LOG_SLEEP:-2}"; printf 'the decisive line\n' ;;
      # Killed by its wrapper: exit 124, nothing written.
      killed) exit 124 ;;
      # Exits non-zero having written neither a log nor a word about why. Distinct
      # from `error`, which writes a reason, and from `empty`, which exits 0.
      silent) exit 1 ;;
      # Ignored SIGTERM and got SIGKILL from `timeout -k`: 137, nothing written.
      killed9) exit 137 ;;
      # Killed after writing part of the stream: exit 124, output kept.
      partial) printf 'the decisive line\n'; exit 124 ;;
      # Part of the stream, then kubectl failing on its own terms -- a reset to
      # the kubelet mid-read. No clock fired, so this must not be described as a
      # timeout, but the file is still a partial log and not a refusal.
      partial_rc1)
        printf 'startup line\nthe decisive line\n'
        echo 'error: unexpected EOF' >&2
        exit 1 ;;
      # The same partial read, but kubectl says more than one thing on the way
      # out. Two lines, because "copied whole" and "reduced to the last line" are
      # indistinguishable when there is only one.
      partial_rc1_multi)
        printf 'startup line\nthe decisive line\n'
        echo 'Warning: results are partial, 3 of 7 shards answered' >&2
        echo 'error: unexpected EOF' >&2
        exit 1 ;;
      # A refusal with no stdout at all. The commonest shape in the tree: the
      # --previous read of a container that never restarted.
      norestart)
        echo 'Error from server (BadRequest): previous terminated container "app" in pod "db-2" not found' >&2
        exit 1 ;;
      # Our timeout firing against cold discovery: klog retries on stderr, no
      # stdout, exit 124.
      timeout_noisy)
        i=1
        while [ "$i" -le 3 ]; do
          echo 'E0728 11:51:48.306797   82827 memcache.go:265] "Unhandled Error" err="couldn'"'"'t get current server API group list: dial tcp: lookup timed out"' >&2
          i=$((i + 1))
        done
        exit 124 ;;
      # Killed mid-line, which is the normal shape of a SIGKILLed stream: the last
      # line has no terminating newline.
      partial_noeol) printf 'first line\nthe decisive partial line'; exit 124 ;;
      # Same, but via the kill-after path.
      partial9) printf 'the decisive line\n'; exit 137 ;;
      *)     printf 'the decisive line\n' ;;
    esac ;;
  # STUB_OBJ_MODE drives the two whole-object reads. `partial` is a read killed
  # after writing a plausible prefix: describe.txt that stops before its Events
  # section reads exactly like a pod that had no events, and pod.yaml that stops
  # after metadata reads like an object with no status.
  describe)
    case "${STUB_OBJ_MODE:-}" in
      partial)  printf 'Name: pod-1\nStatus: Running\n'; exit 124 ;;
      partial9) printf 'Name: pod-1\nStatus: Running\n'; exit 137 ;;
      *) printf 'Events:\n  Warning  Unhealthy  readiness probe failed\n' ;;
    esac ;;
  get)
    case "${STUB_OBJ_MODE:-}" in
      partial) printf 'apiVersion: v1\nkind: Pod\nmetadata:\n  name: pod-1\n'; exit 124 ;;
      # Same cut, delivered as SIGKILL rather than as an expired deadline.
      partial9) printf 'apiVersion: v1\nkind: Pod\nmetadata:\n  name: pod-1\n'; exit 137 ;;
      # Cut off before the first byte: the file never starts.
      killed) exit 124 ;;
      *) printf 'apiVersion: v1\nkind: Pod\n' ;;
    esac ;;
esac
exit 0
STUB
  chmod +x "$_sd/kubectl"
  # Records that the read really went through `timeout`, then runs the rest.
  # `-k <grace>` is skipped so the recorded line stays the effective duration:
  # the assertions are about the budget, not about how the kill is delivered.
  cat > "$_sd/timeout" <<'STUB'
#!/bin/sh
if [ "$1" = "-k" ]; then
  printf 'kill-after %s\n' "$2" >> "$STUB_ARGV"
  shift 2
fi
printf 'timeout %s\n' "$1" >> "$STUB_ARGV"
shift
exec "$@"
STUB
  chmod +x "$_sd/timeout"
  # And point the bound at that stub. COZYREPORT_BOUND is resolved once, when this
  # file sources cozyreport.sh, which happens before any test has a stub PATH -- so
  # on a host with no `timeout` of its own it is empty here, and every assertion
  # about a bounded read fails for a reason that has nothing to do with the code
  # under test. Eight tests did exactly that. A suite whose verdict depends on what
  # the host happens to have installed is not a suite; the same defect was fixed a
  # commit earlier for sha256sum, and this is its other half.
  COZYREPORT_BOUND="timeout -k 5 $COZYREPORT_READ_TIMEOUT"
}

@test "collect pod writes a placeholder when kubectl returns no output at all" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  PATH="$tmp:$PATH" STUB_LOG_MODE=empty cozyreport_collect_pod ns unready-0 "$tmp/out"

  # A zero-byte log file is indistinguishable from a container that started and
  # stayed quiet, which is the ambiguity this collector exists to remove.
  [ -s "$tmp/out/logs-app.txt" ]
  grep -q 'exited without output or error' "$tmp/out/logs-app.txt"
  grep -q 'exited without output or error' "$tmp/out/logs-app-previous.txt"
  rm -rf "$tmp"
}

@test "collect pod does not report a timed out log read as a clean empty one" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  PATH="$tmp:$PATH" STUB_LOG_MODE=killed cozyreport_collect_pod ns hung-0 "$tmp/out"

  # The read was killed by its own wrapper. "kubectl exited without output or
  # error: it never started, or it started and logged nothing" is a claim about
  # the cluster that nothing observed, on the likeliest path there is: a degraded
  # apiserver is the state a failed run is in and the reason the timeout exists.
  grep -q 'cut off by its own 30s timeout' "$tmp/out/logs-app.txt"
  if grep -q 'exited without output or error' "$tmp/out/logs-app.txt"; then echo "FAIL: a killed read reported as a clean empty one"; false; fi
  rm -rf "$tmp"
}

@test "collect pod treats a kill after timeout the same as a plain one" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  PATH="$tmp:$PATH" STUB_LOG_MODE=killed9 cozyreport_collect_pod ns hung-0 "$tmp/out"

  # `timeout -k` reports 137, not 124, when the command ignored SIGTERM and had to
  # be killed. Recognising only 124 sends the more stubborn of the two cases into
  # the branch that describes a clean exit.
  grep -q 'cut off by' "$tmp/out/logs-app.txt"
  if grep -q 'exited without output or error' "$tmp/out/logs-app.txt"; then echo "FAIL: a killed read reported as a clean empty one"; false; fi
  rm -rf "$tmp"
}

@test "a kill is not asserted to be this script's own deadline firing" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  PATH="$tmp:$PATH" STUB_LOG_MODE=killed9 cozyreport_collect_pod ns hung-0 "$tmp/out"

  # 124 comes from `timeout` and from nothing else. 137 is 128+SIGKILL, which our
  # own `-k` grace produces and so does anything else that kills the read -- the
  # OOM killer on a loaded runner, a teardown signalling the process group. The
  # cut-off branch is right for both, since either way the file ends before the
  # log did; the attribution is not. Reported flatly as "its own 30s timeout", a
  # read killed at second two reads as one that waited the full thirty, and the
  # triager goes looking at apiserver latency.
  grep -q 'SIGKILL' "$tmp/out/logs-app.txt"
  grep -q 'does not tell apart' "$tmp/out/logs-app.txt"
  rm -rf "$tmp"
}

@test "a deadline that plainly expired is stated without a disjunction" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  PATH="$tmp:$PATH" STUB_LOG_MODE=killed cozyreport_collect_pod ns hung-0 "$tmp/out"

  # The converse, and the reason the hedge is spelled per status rather than
  # applied to both: 124 IS unambiguous, and hedging it too would trade one false
  # note for a uselessly vague one everywhere.
  grep -q "cut off by its own ${COZYREPORT_READ_TIMEOUT}s timeout" "$tmp/out/logs-app.txt"
  if grep -q 'SIGKILL' "$tmp/out/logs-app.txt"; then
    echo "FAIL: hedged a 124, which only timeout produces"
    cat "$tmp/out/logs-app.txt"
    false
  fi
  rm -rf "$tmp"
}

@test "collect pod marks a partial log cut short by the kill after bound" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  PATH="$tmp:$PATH" STUB_LOG_MODE=partial9 cozyreport_collect_pod ns hung-0 "$tmp/out"

  # Unmarked, this file reads as a complete log that simply ends there.
  grep -q 'the decisive line' "$tmp/out/logs-app.txt"
  grep -q 'TRUNCATED' "$tmp/out/logs-app.txt"
  rm -rf "$tmp"
}

@test "collect pod does not name a read timeout it never applied" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  # No bound was applied, so a 137 came from something else -- runner teardown, an
  # OOM kill. Quoting "its own 30s timeout" there states a mechanism that was not
  # in play, in the file whose job is to be believed.
  PATH="$tmp:$PATH" COZYREPORT_BOUND="" STUB_LOG_MODE=killed9 \
    cozyreport_collect_pod ns hung-0 "$tmp/out"

  if grep -q '30s timeout' "$tmp/out/logs-app.txt"; then
    echo "FAIL: named a 30s timeout on a read that had no bound"
    cat "$tmp/out/logs-app.txt"
    false
  fi
  grep -q 'unbounded' "$tmp/out/logs-app.txt"
  rm -rf "$tmp"
}

@test "collect pod starts its marker on a line of its own after a killed stream" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  PATH="$tmp:$PATH" STUB_LOG_MODE=partial_noeol cozyreport_collect_pod ns hung-0 "$tmp/out"

  # A killed read is SIGKILLed mid-stream, so an unterminated last line is the
  # normal shape here. Appending blind glues the marker onto it: the obvious grep
  # for the marker misses it, the decisive last line is corrupted, and in reverse a
  # container whose own text ends mid-sentence looks like it emitted the marker.
  grep -q '^\[cozyreport\] TRUNCATED' "$tmp/out/logs-app.txt"
  grep -q '^the decisive partial line$' "$tmp/out/logs-app.txt"
  rm -rf "$tmp"
}

@test "collect pod keeps a partial log and marks it truncated" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  PATH="$tmp:$PATH" STUB_LOG_MODE=partial cozyreport_collect_pod ns hung-0 "$tmp/out"

  # Whatever landed before the cut is still the evidence, and for a previous
  # instance it is the only copy of something already gone from the cluster. Kept,
  # and marked -- unmarked, the reader believes they hold the whole tail.
  grep -q 'the decisive line' "$tmp/out/logs-app.txt"
  grep -q '^\[cozyreport\] TRUNCATED' "$tmp/out/logs-app.txt"
  rm -rf "$tmp"
}

@test "collect pod distinguishes the two instances when both came back empty" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  PATH="$tmp:$PATH" STUB_LOG_MODE=empty cozyreport_collect_pod ns unready-0 "$tmp/out"

  # A clean exit with no bytes is ambiguous, and the readings differ per instance:
  # for the previous one, the first reading is that there was never a previous one.
  grep -q 'it never started, or it started and logged nothing' "$tmp/out/logs-app.txt"
  grep -q 'it never restarted, or the previous instance logged nothing' "$tmp/out/logs-app-previous.txt"
  rm -rf "$tmp"
}

@test "collect pod keeps the whole reason when the container list read is noisy" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  PATH="$tmp:$PATH" STUB_JSONPATH_FAIL=noisy cozyreport_collect_pod ns gone-0 "$tmp/out"

  # This is a file of its own, not a log line with neighbours to bury, so there is
  # nothing to gain by picking one line -- and picking is where it goes wrong: a
  # cold discovery cache, which a multi-hour failed run produces, puts a klog retry
  # line in front of the reason and each is long enough to survive trimming.
  grep -q 'pods "gone-0" not found' "$tmp/out/logs-UNAVAILABLE.txt"
  rm -rf "$tmp"
}

@test "collect pod names the timeout when the container list read was killed" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  PATH="$tmp:$PATH" STUB_JSONPATH_FAIL=timeout cozyreport_collect_pod ns hung-0 "$tmp/out"

  # A killed read writes no stderr, so quoting it yields a bare "kubectl said:"
  # that names nothing. Same guard the pod-list path carries.
  grep -q 'cut off by its own 30s timeout' "$tmp/out/logs-UNAVAILABLE.txt"
  if grep -q 'kubectl said: *$' "$tmp/out/logs-UNAVAILABLE.txt"; then echo "FAIL: quoted an empty reason"; false; fi
  rm -rf "$tmp"
}

@test "collect pod says which tail bound each log was written under" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  PATH="$tmp:$PATH" cozyreport_collect_pod ns chatty-0 "$tmp/out"

  # `--tail` drops the OLDEST lines, so a bounded read starts mid-stream.
  # A file that now begins mid-stream looks exactly like a complete one, and for a
  # pod that never came up the startup lines are the decisive part.
  grep -q 'at most the last 2000 lines' "$tmp/out/CONTAINER-LOGS-BOUND.txt"
  # One note per directory covers both instances: the bound is the same for the
  # current log and the previous one, and repeating it in each file was what put
  # prose into a machine-readable artifact in the first place.
  [ "$(grep -c 'at most the last 2000 lines' "$tmp/out/CONTAINER-LOGS-BOUND.txt")" -eq 1 ]
  rm -rf "$tmp"
}

@test "collect pod claims no tail bound when the complete log was requested" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  PATH="$tmp:$PATH" COZY_REPORT_POD_TAIL=-1 cozyreport_collect_pod ns chatty-0 "$tmp/out"

  grep -q -- '--tail=-1' "$tmp/argv"
  if grep -q 'at most the last' "$tmp/out/logs-app.txt"; then echo "FAIL: claimed a bound that was not applied"; false; fi
  rm -rf "$tmp"
}

@test "collect pod refuses a zero tail that would empty every log" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  PATH="$tmp:$PATH" COZY_REPORT_POD_TAIL=0 cozyreport_collect_pod ns chatty-0 "$tmp/out"

  # `--tail=0` returns no lines, which this script would then describe as a
  # container that never started or stayed quiet: a statement about the cluster
  # produced entirely by a typo.
  grep -q -- '--tail=2000' "$tmp/argv"
  if grep -q -- '--tail=0' "$tmp/argv"; then echo "FAIL: asked kubectl for nothing"; false; fi
  rm -rf "$tmp"
}

@test "a log cut short by kubectl itself is marked, without blaming the timeout" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  PATH="$tmp:$PATH" STUB_LOG_MODE=partial_rc1 cozyreport_collect_pod ns reset-0 "$tmp/out"

  # Three outcomes exist, not two: our timeout fired, kubectl failed on its own,
  # or the read completed. Only the first and third were handled, so a log cut
  # short by a connection reset carried no marker at all -- while the notes in the
  # same tarball state that a read cut short says so on its last line.
  file="$tmp/out/logs-app.txt"
  grep -q 'the decisive line' "$file"
  if ! grep -q '^\[cozyreport\] TRUNCATED' "$file"; then
    echo "FAIL: a partial log left by a non-timeout failure carries no marker"
    cat "$file"
    false
  fi
  # It must not name our timeout, because no timeout fired.
  if grep -qE 'cut off by its own|timeout' "$file"; then
    echo "FAIL: blamed the read timeout for a failure that was not one"
    cat "$file"
    false
  fi
  # kubectl's own reason survives into the file.
  grep -q 'unexpected EOF' "$file"
  # And it is not described as a clean tail window.
  if grep -q 'at most the last' "$file"; then
    echo "FAIL: a truncated log claims to be a complete tail window"
    false
  fi
  rm -rf "$tmp"
}

@test "collect pod keeps the kubectl refusal instead of overwriting it" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  PATH="$tmp:$PATH" STUB_LOG_MODE=error cozyreport_collect_pod ns puller-0 "$tmp/out"

  # kubectl's reason IS the finding; the placeholder must not replace it.
  grep -q 'waiting to start: ImagePullBackOff' "$tmp/out/logs-app.txt"
  if grep -q 'exited without output' "$tmp/out/logs-app.txt"; then echo "FAIL: placeholder overwrote kubectl's reason"; false; fi
  # This file holds a refusal, not a log, so a line about how many lines of log it
  # holds would describe something that is not in it.
  if grep -q 'at most the last' "$tmp/out/logs-app.txt"; then echo "FAIL: described a refusal as a truncated log"; false; fi
  # Nor was anything cut off: nothing was ever produced to cut. This assertion is
  # the one that was missing -- the test drove this path already and checked only
  # the tail note, so a refusal wrongly marked TRUNCATED passed here unnoticed.
  if grep -q 'TRUNCATED' "$tmp/out/logs-app.txt"; then
    echo "FAIL: a refusal is reported as a log that was cut off part way through"
    cat "$tmp/out/logs-app.txt"
    false
  fi
  rm -rf "$tmp"
}

@test "an uncapturable stderr is not reported as kubectl having said nothing" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  # TMPDIR at a path that does not exist, so mktemp fails and the stderr scratch
  # file is never created. The read still runs -- that fallback is deliberate --
  # but nothing captured what kubectl said. "Said nothing" is then a claim about
  # the cluster manufactured by a local filesystem condition, which is the defect
  # this collector is written against.
  PATH="$tmp:$PATH" TMPDIR=/nonexistent-cozyreport-probe STUB_LOG_MODE=error \
    cozyreport_collect_pod ns puller-0 "$tmp/out"

  f="$tmp/out/logs-app.txt"
  if grep -q 'without a message' "$f"; then
    echo "FAIL: an uncapturable message is reported as kubectl having been silent"
    cat "$f"
    false
  fi
  grep -q 'could not be captured' "$f"
  grep -q 'unknown' "$f"
  rm -rf "$tmp"
}

@test "a killed pod list names the cutoff even when its stderr could not be captured" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  # Both conditions at once: the read is killed at 124 AND mktemp has nowhere to
  # write the scratch file. Each is modelled separately elsewhere in this suite;
  # they were never combined, and that overlap is the only place the branch order
  # is observable. With the no-temp-dir branch first this file said the message
  # could not be captured, while the two sibling sites -- given the same pair --
  # named the cutoff. One tarball, two answers, about a mechanism the script had
  # in hand: a read cut off at 124 is killed before kubectl writes anything, so an
  # empty stderr there is a consequence of the timeout, not a separate fact.
  PATH="$tmp:$PATH" TMPDIR=/nonexistent-cozyreport-probe STUB_PODLIST_FAIL=timeout \
    cozyreport_collect_broken_pods "$tmp/out"

  f="$tmp/out/COLLECTION-FAILED.txt"
  grep -q 'the read was cut off by' "$f"
  if grep -q 'could not be captured' "$f"; then
    echo "FAIL: led with the missing temp dir for a read the script knew was killed"
    cat "$f"
    false
  fi
  rm -rf "$tmp"
}

@test "the per-pod collector answers that pair the same way" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  # The mirror of the test above, so the two cannot drift apart from the other
  # side either. Same pair of conditions, same expected leading cause.
  PATH="$tmp:$PATH" TMPDIR=/nonexistent-cozyreport-probe STUB_JSONPATH_FAIL=timeout \
    cozyreport_collect_pod ns slow-0 "$tmp/out"

  f="$tmp/out/logs-UNAVAILABLE.txt"
  [ -f "$f" ] || { echo "FAIL: no container-list failure note written"; ls "$tmp/out"; false; }
  grep -q 'cut off by' "$f"
  if grep -q 'could not be captured' "$f"; then
    echo "FAIL: the two sites answer the same pair differently"
    cat "$f"
    false
  fi
  rm -rf "$tmp"
}

@test "a pod list whose stderr could not be captured says so, not that it was silent" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  PATH="$tmp:$PATH" TMPDIR=/nonexistent-cozyreport-probe STUB_PODLIST_FAIL=1 \
    cozyreport_collect_broken_pods "$tmp/out"

  f="$tmp/out/COLLECTION-FAILED.txt"
  if grep -q 'kubectl said nothing' "$f"; then
    echo "FAIL: claimed silence when the message was never captured"
    cat "$f"
    false
  fi
  grep -q 'could not be captured' "$f"
  # The section still refuses to read as a healthy cluster.
  grep -q 'NOT a report that every pod was Ready' "$f"
  rm -rf "$tmp"
}

@test "a previous-instance refusal is not dressed up as a truncated log" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  # The commonest file in the whole pod tree: --previous against a container that
  # simply never restarted. kubectl refuses, stdout is empty, and the refusal IS
  # the finding. Marking it "cut off part way through" is false on one file per
  # container of every collected pod, which retires the marker as a signal.
  PATH="$tmp:$PATH" STUB_LOG_MODE=norestart cozyreport_collect_pod ns db-2 "$tmp/out"

  file="$tmp/out/logs-app-previous.txt"
  grep -q 'previous terminated container' "$file"
  if grep -q 'TRUNCATED' "$file"; then
    echo "FAIL: a container that never restarted is reported as a truncated log"
    cat "$file"
    false
  fi
  if grep -q 'at most the last' "$file"; then
    echo "FAIL: described a refusal as a bounded log"
    false
  fi
  rm -rf "$tmp"
}

@test "a timeout before kubectl wrote anything says so, and keeps the preamble" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  # Our timeout fires with cold discovery: exit 124, no stdout, klog retry lines on
  # stderr. The accurate message for this case must survive, and the preamble that
  # explains the apiserver state must not be overwritten by it.
  PATH="$tmp:$PATH" STUB_LOG_MODE=timeout_noisy cozyreport_collect_pod ns slow-0 "$tmp/out"

  file="$tmp/out/logs-app.txt"
  grep -q 'before kubectl wrote anything' "$file"
  grep -q "couldn't get current server API group list" "$file"
  # It must not claim the container stopped writing: nothing was read at all.
  if grep -q 'not because the container stopped writing' "$file"; then
    echo "FAIL: claimed a log ended early when none was produced"
    cat "$file"
    false
  fi
  rm -rf "$tmp"
}

@test "collect pod names why the container list was unavailable" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  PATH="$tmp:$PATH" STUB_JSONPATH_FAIL=1 cozyreport_collect_pod ns gone-0 "$tmp/out"

  # Without this the pod directory carries a yaml and a describe and no hint
  # that per-container logs were ever attempted.
  [ -f "$tmp/out/logs-UNAVAILABLE.txt" ]
  grep -q 'container list came back empty' "$tmp/out/logs-UNAVAILABLE.txt"
  grep -q 'pods "gone-0" not found' "$tmp/out/logs-UNAVAILABLE.txt"
  # The scratch stderr file must not ship in the artifact.
  if [ -f "$tmp/out/.err" ]; then echo "FAIL: scratch stderr file left in the report tree"; false; fi
  rm -rf "$tmp"
}

@test "collect pod bounds every kubectl read with a wall clock timeout" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  PATH="$tmp:$PATH" cozyreport_collect_pod ns chatty-0 "$tmp/out"

  # The tarball is written only at the end of the run, so anything hanging
  # against a degraded apiserver costs the whole report rather than one file.
  # All five reads for a single-container pod: get -o yaml, describe, the
  # container list, and the current + previous log. Each carries the kill-after
  # bound too: plain `timeout` sends TERM and then waits forever on a process that
  # ignores it, which is the hang the budget exists to prevent.
  [ "$(grep -c '^timeout 30$' "$tmp/argv")" -eq 5 ]
  [ "$(grep -c '^kill-after 5$' "$tmp/argv")" -eq 5 ]
  # Size is bounded too, on the two reads that can be unboundedly large. The
  # widened selection reaches long-lived containers, which is exactly the class
  # a readiness failure produces.
  [ "$(grep -c -- '--tail=2000' "$tmp/argv")" -eq 2 ]
  rm -rf "$tmp"
}

@test "collect pod captures the current log of a container that never restarted" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  PATH="$tmp:$PATH" cozyreport_collect_pod ns unready-0 "$tmp/out"

  grep -q 'the decisive line' "$tmp/out/logs-app.txt"
  [ -f "$tmp/out/pod.yaml" ]
  [ -f "$tmp/out/describe.txt" ]
  rm -rf "$tmp"
}

# --------------------------------------------------------------------------- #
# hack/cozyreport-summary.sh, end to end against a stub kubectl. summary.txt is  #
# the first thing a triager opens, so a row that misreports itself there costs   #
# more than the same mistake buried in the per-pod tree.                        #
# --------------------------------------------------------------------------- #

summary_stub_dir() {
  _ss="$1"
  cat > "$_ss/kubectl" <<'STUB'
#!/bin/sh
# Only the pod list matters here; every other section is asked to be empty, and
# `get crd` fails so the CRD-gated sections skip.
# Every read killed by its own wrapper: the state a bounded read can reach and an
# unbounded one cannot, since an unbounded read can only hang.
[ -n "${STUB_ALL_TIMEOUT:-}" ] && exit 124
case "$*" in
  *"get pod -A --no-headers"*)
    case "${STUB_PODLIST_FAIL:-}" in
      '') ;;
      # Rows first, then a failure: a cluster-wide list can be answered in part
      # and then cut off, which is not the same event as a refusal.
      partial)
        printf '%s\n' "${STUB_POD_ROWS:-}"
        echo 'Error from server: unexpected EOF' >&2
        exit 1 ;;
      *) echo 'Error from server (Forbidden): pods is forbidden' >&2; exit 1 ;;
    esac
    printf '%s\n' "${STUB_POD_ROWS:-}" ;;
  *"get crd"*)
    # Three outcomes, not two: present, absent, and refused. The last exits
    # non-zero exactly like the absent one and only kubectl's message separates
    # them, which is the whole point of the case below.
    [ -n "${STUB_CRD_FORBIDDEN:-}" ] && { echo 'Error from server (Forbidden): customresourcedefinitions.apiextensions.k8s.io is forbidden' >&2; exit 1; }
    [ -n "${STUB_CRD_PRESENT:-}" ] && exit 0
    # Exits non-zero having said nothing. Not a shortcut for "absent": kubectl is
    # explicit about NotFound, so silence here is a fourth outcome and the one a
    # caller must not file under absence.
    [ -n "${STUB_CRD_SILENT:-}" ] && exit 1
    # Absence as kubectl actually reports it. The stub used to exit non-zero in
    # silence, which no real cluster does, and a fixture that cannot produce the
    # message is a fixture that cannot tell absence from anything else.
    echo 'Error from server (NotFound): customresourcedefinitions.apiextensions.k8s.io "'"$4"'" not found' >&2
    exit 1 ;;
  *"get hr -A"*)
    # Succeeds AND writes to stderr. kubectl does this for deprecation notices and
    # for partial-result warnings, so "wrote to stderr" and "failed" are not the
    # same event and the reader must not conflate them.
    [ -n "${STUB_HR_WARN:-}" ] && echo 'Warning: v1beta1 HelmRelease is deprecated in v2.1+, use v2' >&2
    case "${STUB_HR_FAIL:-}" in
      '') ;;
      # Rows first, then a failure: the read was answered in part.
      partial)
        printf '%s\n' "${STUB_HR_ROWS:-}"
        echo 'error: unexpected EOF' >&2
        exit 1 ;;
      # Two lines, and the actionable one LAST: a failing read is preceded by a
      # klog discovery-retry line per attempt, so a helper that quotes the first
      # line names the retry instead of the cause.
      *) echo 'E0728 22:14:03.112233 1 memcache.go:265] couldn'"'"'t get current server API group list: the server is currently unable to handle the request' >&2
         echo 'Error from server (Forbidden): helmreleases.helm.toolkit.fluxcd.io is forbidden' >&2
         exit 1 ;;
    esac
    printf '%s\n' "${STUB_HR_ROWS:-}" ;;
  # A real kubectl rejects a filter nested INSIDE another filter, which is what
  # `{range .items[?(...conditions[?(...)]...)]}` is. Matched on that prefix rather
  # than by counting `conditions[?(`, because several separate filters in one
  # --output=custom-columns are legal and kubectl accepts them -- the node section
  # below uses three.
  *"range .items[?("*) echo 'error: error parsing jsonpath, unterminated filter' >&2; exit 1 ;;
  *source.toolkit.fluxcd.io*) printf '%s\n' "${STUB_FLUX_ROWS:-}" ;;
  *"get pvc -A"*)   printf '%s\n' "${STUB_PVC_ROWS:-}" ;;
  *"get pv "*|*"get pv"*)
    printf '%s\n' "${STUB_PV_ROWS:-}" ;;
  *"get certificates"*)
    printf '%s\n' "${STUB_CERT_ROWS:-}" ;;
  *"get events"*)
    printf '%s\n' "${STUB_EVENT_ROWS:-}" ;;
  *"get nodes"*) printf '%s\n' "${STUB_NODE_ROWS:-}" ;;
esac
exit 0
STUB
  chmod +x "$_ss/kubectl"
}

@test "a leading-zero knob is refused rather than read as octal" {
  # `$(( ))` reads a leading zero as octal; `[` reads the same string as decimal.
  # So 010 is 8 to the deadline arithmetic and 10 to every comparison beside it,
  # and 008 is not valid octal at all -- under dash, which is /bin/sh on the CI
  # image, that aborts the script with "Illegal number: 008" at the deadline
  # calculation, before the tarball is written. Losing the whole artifact to a
  # typo in a knob is the one outcome every bound in this file exists to avoid.
  for bad in 008 09 010 00; do
    if cozyreport_is_count "$bad"; then
      echo "FAIL: accepted '$bad', which arithmetic will read as octal or reject"
      false
    fi
  done
  # Plain zero still passes: a cap of zero is a supported request (collect nothing,
  # and say so), and it is not ambiguous between the two bases.
  cozyreport_is_count 0
  cozyreport_is_count 40
  cozyreport_is_count 600
}

@test "a leading-zero budget falls back to the default instead of killing the run" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  # Run through `/bin/sh`, unconditionally and by that name, because `/bin/sh` is
  # what the collector is invoked as and what the claim is about. Selecting `dash`
  # when the host happens to ship it and falling back to `sh` otherwise made the
  # shell under test a property of the machine: the assertion still ran, but not
  # necessarily against the semantics it names.
  #
  # What is proven here is shell-independent and that is the point: a value the
  # validator rejects never reaches arithmetic, so nothing aborts and the walk
  # completes. The stakes are not shell-independent, which is why the value is
  # `008`: dash makes a rejected expansion fatal, so on the CI image this is the
  # difference between a fallback and losing the tarball. `010` is worse still and
  # not an error anywhere -- both shells read it as octal 8 while `[` reads the
  # same string as decimal 10.
  run_out=$(cd "$HACK_DIR" && PATH="$tmp:$PATH" STUB_POD_ROWS='ns  p-0  0/1  Running  0  5m' \
    /bin/sh -c 'COZYREPORT_LIB=1 . ./cozyreport.sh; COZY_REPORT_PODS_BUDGET=008 cozyreport_collect_broken_pods "$1"/out; echo "rc=$?"' _ "$tmp" 2>&1)

  if printf '%s\n' "$run_out" | grep -q 'Illegal number'; then
    echo "FAIL: a malformed knob aborted the collection instead of falling back"
    printf '%s\n' "$run_out"
    false
  fi
  printf '%s\n' "$run_out" | grep -q 'rc=0'
  # And the fallback is not silent: the operator who set it is told it was ignored.
  grep -rq 'COZY_REPORT_PODS_BUDGET' "$tmp/out/COLLECTION-NOTES.txt"
  rm -rf "$tmp"
}

@test "container logs name the container with -c, the documented form" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"
  export STUB_CONTAINERS='app'

  PATH="$tmp:$PATH" cozyreport_collect_pod ns pod-1 "$tmp/out"

  # kubectl accepts `logs POD CONTAINER`, but documents `(POD|TYPE/NAME) [-c NAME]`.
  # The undocumented form working today is not a promise about tomorrow.
  if ! grep -q -- '-c app' "$tmp/argv"; then
    echo "FAIL: container passed positionally instead of with -c"
    cat "$tmp/argv"
    false
  fi
  # Both instances, not just the current one.
  [ "$(grep -c -- '-c app' "$tmp/argv")" -eq 2 ]
  grep -q -- '--previous' "$tmp/argv"
  rm -rf "$tmp"
}

@test "the docs name the file the tail bound actually lands in" {
  # The note moved out of the logs and into a file beside them; the doc sentence
  # and the script header stayed behind saying "inside the log itself". A reader
  # told to look inside opens a file that looks complete, finds no marker, and
  # concludes the log is whole -- which is the exact failure this collector is
  # written against, produced by its own documentation.
  doc="$HACK_DIR/../docs/agents/e2e-testing.md"

  # A fixed window rather than "up to the next period": the destination filename
  # contains one, so a sentence-terminated match stops inside the very word the
  # assertion is looking for.
  sentence=$(grep -o 'the tail bound each kept log was written under.\{0,160\}' "$doc" | head -1)
  [ -n "$sentence" ] || { echo "FAIL: the doc no longer describes the tail bound"; false; }
  case "$sentence" in
    *"inside the log"*) echo "FAIL: the doc still says the bound is inside the log"; false ;;
  esac
  printf '%s\n' "$sentence" | grep -q 'capture-notes.txt' || {
    echo "FAIL: the doc does not name the file the bound lands in"
    printf '  %s\n' "$sentence"
    false
  }

  # And the script's own header, which a reader of the script hits first.
  hdr=$(sed -n '1,60p' "$HACK_DIR/e2e-capture-previous-logs.sh")
  case "$hdr" in
    *"names the bound it was written under"*)
      echo "FAIL: the capture script's header still claims the bound is in the log"; false ;;
  esac
  printf '%s\n' "$hdr" | grep -q 'capture-notes.txt' || {
    echo "FAIL: the capture script's header does not name capture-notes.txt"; false
  }
}

@test "the docs name the file a discarded knob note actually lands in" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  PATH="$tmp:$PATH" STUB_PODLIST_FAIL=1 COZY_REPORT_PODS_MAX=10m \
    cozyreport_collect_broken_pods "$tmp/out"

  # Which file the note lands in, read from the tree rather than assumed.
  landed=""
  for f in "$tmp/out"/COLLECTION-*.txt; do
    [ -f "$f" ] || continue
    grep -q "COZY_REPORT_PODS_MAX='10m'" "$f" && landed=$(basename "$f")
  done
  [ -n "$landed" ] || { echo "FAIL: the knob note landed nowhere"; ls "$tmp/out"; false; }

  # The doc promised two destinations and the note uses a third on this path. A
  # reader who mistyped a knob AND hit an unreachable apiserver follows the doc to
  # COLLECTION-NOTES.txt, finds nothing, and concludes the knob was accepted.
  # The marker-parity guard cannot catch this: it checks that each filename is
  # named somewhere in both doc and code, never which file a given note goes to.
  # Scoped to the SENTENCE about knob-note destinations, not the whole document.
  # COLLECTION-FAILED.txt is named elsewhere in the doc for an unrelated reason, so
  # a document-wide grep passes whether or not it is listed as a destination -- the
  # same weakness a document-wide match has anywhere.
  sentence=$(sed -n 's/.*a knob value the collector could not use is named too\(.*\)/\1/p' \
    "$HACK_DIR/../docs/agents/e2e-testing.md" | head -1)
  [ -n "$sentence" ] || { echo "FAIL: could not locate the knob-note sentence in the docs"; false; }
  if ! printf '%s\n' "$sentence" | grep -q "$landed"; then
    echo "FAIL: the note landed in $landed, which that sentence never names as a destination"
    printf '  sentence: %s\n' "$sentence"
    false
  fi
  rm -rf "$tmp"
}

@test "a pod list refusal is not attributed to a timeout that did not fire" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  # Exit 1, no stderr: kubectl refused and said nothing. The old wording offered
  # "at 124 or 137 the read was cut off by its own 30s timeout" to a reader who had
  # just read "exited 1" -- a mechanism that was not in play, plus a conditional
  # the code had already evaluated. Every other classification site in these three
  # scripts branches instead.
  PATH="$tmp:$PATH" STUB_PODLIST_FAIL=silent cozyreport_collect_broken_pods "$tmp/out"

  f="$tmp/out/COLLECTION-FAILED.txt"
  grep -q 'exited 1' "$f"
  if grep -qE 'at 124 or 137|cut off by' "$f"; then
    echo "FAIL: a plain refusal is described in terms of the read timeout"
    cat "$f"
    false
  fi
  rm -rf "$tmp"
}

@test "a pod list killed by the timeout says so without a disjunction" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  PATH="$tmp:$PATH" STUB_PODLIST_FAIL=timeout cozyreport_collect_broken_pods "$tmp/out"

  f="$tmp/out/COLLECTION-FAILED.txt"
  grep -q 'the read was cut off by' "$f"
  # The reader is told which mechanism applied, not asked to evaluate a condition.
  if grep -q 'at 124 or 137' "$f"; then
    echo "FAIL: made the reader evaluate the exit code the script already knew"
    cat "$f"
    false
  fi
  rm -rf "$tmp"
}

@test "a discarded knob is reported even when the pod list never returned" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  # The failed-list branch returns before the note block at the end of the
  # function, so an operator who both mistyped a bound and hit an unreachable
  # apiserver was told about the second and never about the first -- while the
  # comment on that block promises the value is named whether or not either bound
  # fired. Low damage, but the comment is the contract the rest of the file is
  # written against.
  PATH="$tmp:$PATH" STUB_PODLIST_FAIL=1 COZY_REPORT_PODS_MAX=10m \
    cozyreport_collect_broken_pods "$tmp/out"

  [ -f "$tmp/out/COLLECTION-FAILED.txt" ]
  if ! grep -q "COZY_REPORT_PODS_MAX='10m'" "$tmp/out/COLLECTION-FAILED.txt"; then
    echo "FAIL: the discarded knob left no trace when the pod list failed"
    cat "$tmp/out/COLLECTION-FAILED.txt"
    false
  fi
  rm -rf "$tmp"
}

@test "the shared cap fixes the starting number, not the resulting counts" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"
  rows="$(i=1; while [ "$i" -le 3 ]; do printf 'ns-%s  pod-%s  0/1  Running  0  5m\n' "$i" "$i"; i=$((i + 1)); done)"

  # Two Environment blocks said the shared default means the listing and the
  # evidence tree "cannot disagree" about how much was kept. They can: the
  # collector also stops on its time budget, so it holds fewer pods than the
  # summary lists, and that is a legitimate outcome rather than a contradiction.
  # What the shared constant actually prevents is the two STARTING from different
  # numbers. This pins the difference the header used to deny.
  PATH="$tmp:$PATH" STUB_POD_ROWS="$rows" COZY_REPORT_PODS_BUDGET=0 \
    cozyreport_collect_broken_pods "$tmp/out"

  collected=$(find "$tmp/out" -mindepth 2 -maxdepth 2 -type d 2>/dev/null | wc -l | tr -d ' ')
  # The budget stopped the walk before any pod, while all three remain listed in
  # the summary's source. Counts differ, and the artifact explains why.
  [ "$collected" -lt 3 ]
  grep -q 'collection budget elapsed' "$tmp/out/COLLECTION-TRUNCATED.txt"
  rm -rf "$tmp"
}

@test "the unbounded note does not claim completeness the caps contradict" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"
  rows="$(printf '%s\n' \
    'ns-1  p-1  0/1  Running  0  5m' \
    'ns-2  p-2  0/1  Running  0  5m' \
    'ns-3  p-3  0/1  Running  0  5m')"

  # Both notes in one directory. A missing `timeout` removes only the wall-clock
  # ceiling; the pod cap, the collection budget and --tail all still apply, so
  # "the evidence here is complete" was false whenever any of them fired. The
  # existing UNBOUNDED test feeds a single pod, so the cap never overflows and the
  # two files never meet -- the claim was unfalsifiable rather than checked.
  PATH="$tmp:$PATH" COZYREPORT_BOUND="" STUB_POD_ROWS="$rows" COZY_REPORT_PODS_MAX=1 \
    cozyreport_collect_broken_pods "$tmp/out"

  [ -f "$tmp/out/COLLECTION-UNBOUNDED.txt" ]
  [ -f "$tmp/out/COLLECTION-TRUNCATED.txt" ]
  grep -q 'collected 1 of 3' "$tmp/out/COLLECTION-TRUNCATED.txt"
  for note in "$tmp/out"/COLLECTION-*.txt; do
    if grep -qE 'evidence here is complete|is complete,' "$note"; then
      echo "FAIL: $(basename "$note") claims completeness while a cap was reported beside it"
      cat "$note"
      false
    fi
  done
  rm -rf "$tmp"
}

@test "no truncation note claims completeness while a truncated file sits beside it" {
  # Budget expires part way through a pod whose logs were ALSO being cut off by their own read timeout. That combination is not
  # exotic -- the budget runs out because reads are slow, and the ordinary way for
  # a read to be slow is to hit its ceiling -- so the two notes land in the same
  # directory and must not contradict each other.
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"
  export STUB_CONTAINERS='app sidecar'

  # Both halves have to be true at once for this to test anything: the note must
  # be written AND a file beside it must be truncated. An expired deadline writes
  # the note (and stops the walk before any container), while STUB_OBJ_MODE cuts
  # off the two object reads that happen before the loop. With only STUB_LOG_MODE
  # and no deadline, no note is written at all and the loop below iterates over
  # nothing, which is green regardless of the wording.
  PATH="$tmp:$PATH" STUB_OBJ_MODE=partial COZYREPORT_COLLECT_DEADLINE=1 \
    cozyreport_collect_pod ns wide-0 "$tmp/out"

  [ -f "$tmp/out/CONTAINER-LOGS-TRUNCATED.txt" ]
  grep -q '^# \[cozyreport\] TRUNCATED' "$tmp/out/pod.yaml"
  grep -q '^# \[cozyreport\] TRUNCATED' "$tmp/out/describe.txt"

  # Neither note may assert its neighbours are whole while those neighbours say
  # otherwise on their own last line.
  for note in "$tmp/out"/*TRUNCATED.txt "$tmp/out"/COLLECTION-*.txt; do
    [ -f "$note" ] || continue
    if grep -qE 'read in full|is complete|both of which were collected' "$note"; then
      echo "FAIL: $(basename "$note") claims completeness next to a truncated file"
      cat "$note"
      false
    fi
  done
  rm -rf "$tmp"
}

@test "collect pod marks pod.yaml and describe.txt when their read was cut off" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  PATH="$tmp:$PATH" STUB_OBJ_MODE=partial cozyreport_collect_pod ns pod-1 "$tmp/out"

  # Both are killed at the same ceiling as every other read, so a half-written
  # file is an outcome they can produce. A describe.txt that stops before its Events section is
  # byte-indistinguishable from a pod that had no events, and that reading is the
  # exact one this collector exists to prevent.
  for f in describe.txt pod.yaml; do
    if ! grep -q '^# \[cozyreport\] TRUNCATED' "$tmp/out/$f"; then
      echo "FAIL: $f was cut off mid-read and says nothing about it"
      cat "$tmp/out/$f"
      false
    fi
  done
  # What did land is kept, not discarded: a partial describe still names the pod.
  grep -q 'Status: Running' "$tmp/out/describe.txt"
  rm -rf "$tmp"
}

@test "collect pod leaves no truncation marker on a read that completed" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  PATH="$tmp:$PATH" cozyreport_collect_pod ns pod-1 "$tmp/out"

  # The other half of the contract. A marker on every file would be as useless as
  # a marker on none: the reader could not tell which reads actually stopped short.
  for f in describe.txt pod.yaml; do
    if grep -q 'TRUNCATED' "$tmp/out/$f"; then
      echo "FAIL: $f completed but claims it was truncated"
      false
    fi
  done
  grep -q 'Events:' "$tmp/out/describe.txt"
  rm -rf "$tmp"
}

@test "collect pod stops reading containers once the collection deadline passed" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"
  export STUB_CONTAINERS='app sidecar exporter'

  PATH="$tmp:$PATH" COZYREPORT_COLLECT_DEADLINE=1 cozyreport_collect_pod ns wide-0 "$tmp/out"

  # Without this the overshoot past the collection budget grows with the pod's
  # container count, so a ceiling stated in terms of one container is not one.
  [ -f "$tmp/out/CONTAINER-LOGS-TRUNCATED.txt" ]
  # The note says what was ATTEMPTED and points at each file's own last line for
  # whether that read finished. It must not claim the files beside it are whole:
  # this note is written because reads were slow, and the ordinary way to be slow
  # is to hit the per-read ceiling, so a neighbouring log carrying its own
  # TRUNCATED marker is the likely case, and a test pinning "read in full" would
  # hold that false claim in place.
  grep -q 'were attempted' "$tmp/out/CONTAINER-LOGS-TRUNCATED.txt"
  if grep -qE 'read in full|is complete' "$tmp/out/CONTAINER-LOGS-TRUNCATED.txt"; then
    echo "FAIL: the truncation note claims neighbouring files are complete"
    cat "$tmp/out/CONTAINER-LOGS-TRUNCATED.txt"
    false
  fi
  if [ -f "$tmp/out/logs-app.txt" ]; then echo "FAIL: read a container past the deadline"; false; fi
  # The two reads that are not per-container still happened: they are what makes
  # the note above answerable, since they name the containers that were skipped.
  [ -f "$tmp/out/pod.yaml" ]
  [ -f "$tmp/out/describe.txt" ]
  rm -rf "$tmp"
}

@test "collect pod reads every container when no deadline was handed to it" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"
  export STUB_CONTAINERS='app sidecar exporter'

  PATH="$tmp:$PATH" cozyreport_collect_pod ns wide-0 "$tmp/out"

  # A guard that fires when the caller set no budget would silently gut the
  # report for every direct caller of this helper.
  [ -f "$tmp/out/logs-app.txt" ]
  [ -f "$tmp/out/logs-sidecar.txt" ]
  [ -f "$tmp/out/logs-exporter.txt" ]
  if [ -f "$tmp/out/CONTAINER-LOGS-TRUNCATED.txt" ]; then echo "FAIL: claimed a truncation with no budget set"; false; fi
  rm -rf "$tmp"
}

@test "collect broken pods walks the selection into one directory per pod" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"
  export STUB_POD_ROWS="$(printf '%s\n' \
    'cozy-keycloak  keycloak-db-2  0/1  Running  0  6m' \
    'kube-system    coredns-abc    1/1  Running  0  2h' \
    'cozy-system    puller-z       0/1  ImagePullBackOff  0  4m')"

  PATH="$tmp:$PATH" cozyreport_collect_broken_pods "$tmp/pods"

  # Selection and collection are each pinned above; this pins the wiring, where
  # a `read` eating the wrong field would leave a report quietly holding fewer
  # pods than the cluster had, with no other symptom.
  [ -f "$tmp/pods/cozy-keycloak/keycloak-db-2/pod.yaml" ]
  [ -f "$tmp/pods/cozy-system/puller-z/pod.yaml" ]
  if [ -d "$tmp/pods/kube-system/coredns-abc" ]; then echo "FAIL: collected a healthy pod"; false; fi
  rm -rf "$tmp"
}

@test "collect broken pods spends its cap on the tenant namespaces first" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"
  # kubectl emits namespace-alphabetical rows, so cozy-* arrives first and the pod
  # the suite died on arrives last. The cap only bites on a broadly degraded
  # cluster, which is exactly when the platform contributes those cozy-* rows.
  export STUB_POD_ROWS="$(printf '%s\n' \
    'cozy-a  pod-a  0/1  Running  0  5m' \
    'cozy-b  pod-b  0/1  Running  0  5m' \
    'cozy-c  pod-c  0/1  Running  0  5m' \
    'tenant-test  the-pod-the-run-died-on  0/1  Running  0  5m')"

  PATH="$tmp:$PATH" COZY_REPORT_PODS_MAX=1 cozyreport_collect_broken_pods "$tmp/pods"

  [ -f "$tmp/pods/tenant-test/the-pod-the-run-died-on/pod.yaml" ]
  if [ -d "$tmp/pods/cozy-a" ]; then echo "FAIL: spent the cap on an unrelated namespace"; false; fi
  rm -rf "$tmp"
}

@test "collect broken pods collects a zero cap without aborting the report" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"
  export STUB_POD_ROWS='ns-1  pod-1  0/1  Running  0  5m'

  PATH="$tmp:$PATH" COZY_REPORT_PODS_MAX=0 cozyreport_collect_broken_pods "$tmp/pods"

  # GNU head takes `-n 0` as an empty result; BSD/macOS head rejects it with
  # "illegal line count -- 0", which under a local run aborts the collection. The
  # drop still has to be visible.
  [ "$(find "$tmp/pods" -name pod.yaml | grep -c . || true)" -eq 0 ]
  grep -q 'collected 0 of 1' "$tmp/pods/COLLECTION-TRUNCATED.txt"
  rm -rf "$tmp"
}

@test "collect broken pods reports a malformed cap instead of applying it silently" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"
  export STUB_POD_ROWS='ns-1  pod-1  0/1  Running  0  5m'

  PATH="$tmp:$PATH" COZY_REPORT_PODS_MAX=abc cozyreport_collect_broken_pods "$tmp/pods"

  # An operator who wrote a value and got the default with no trace reads the
  # result as the bound they asked for. Reported even though nothing was truncated:
  # the discarded value is the finding, not the truncation.
  grep -q "COZY_REPORT_PODS_MAX='abc'" "$tmp/pods/COLLECTION-NOTES.txt"
  grep -q 'used 40' "$tmp/pods/COLLECTION-NOTES.txt"
  rm -rf "$tmp"
}

@test "collect broken pods reports a malformed budget the same way" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"
  export STUB_POD_ROWS='ns-1  pod-1  0/1  Running  0  5m'

  PATH="$tmp:$PATH" COZY_REPORT_PODS_BUDGET=10m cozyreport_collect_broken_pods "$tmp/pods"

  grep -q "COZY_REPORT_PODS_BUDGET='10m'" "$tmp/pods/COLLECTION-NOTES.txt"
  grep -q 'used 600' "$tmp/pods/COLLECTION-NOTES.txt"
  rm -rf "$tmp"
}

@test "collect broken pods rejects a number too large for the shell to compute with" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"
  export STUB_POD_ROWS='ns-1  pod-1  0/1  Running  0  5m'

  PATH="$tmp:$PATH" COZY_REPORT_PODS_BUDGET=999999999999999999999999 \
    cozyreport_collect_broken_pods "$tmp/pods"

  # All-digits is not the same test as "this shell can compute with it".
  # `$(( now + 999999999999999999999999 ))` is fatal in dash, which is /bin/sh on
  # the CI image, so the report would end at the deadline calculation and never
  # reach the tarball -- the one outcome every bound here exists to avoid.
  grep -q "COZY_REPORT_PODS_BUDGET='999999999999999999999999'" "$tmp/pods/COLLECTION-NOTES.txt"
  [ -f "$tmp/pods/ns-1/pod-1/pod.yaml" ]
  rm -rf "$tmp"
}

@test "pod tail rejects a number too large for the shell as well" {
  [ "$(COZY_REPORT_POD_TAIL=999999999999999999999999 cozyreport_pod_tail)" = "2000" ]
  [ -n "$(COZY_REPORT_POD_TAIL=999999999999999999999999 cozyreport_pod_tail_note)" ]
}

@test "collect broken pods appends a knob note to a truncation it also reported" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"
  export STUB_POD_ROWS="$(i=1; while [ "$i" -le 5 ]; do printf 'ns-%s  pod-%s  0/1  Running  0  5m\n' "$i" "$i"; i=$((i + 1)); done)"

  PATH="$tmp:$PATH" COZY_REPORT_PODS_MAX=2 COZY_REPORT_POD_TAIL=abc \
    cozyreport_collect_broken_pods "$tmp/pods"

  # Only the standalone COLLECTION-NOTES.txt branch was covered; this is the other
  # one, where a truncation note already exists and the knob note has to join it
  # rather than land in a second file nobody reads.
  grep -q 'collected 2 of 5' "$tmp/pods/COLLECTION-TRUNCATED.txt"
  grep -q "COZY_REPORT_POD_TAIL='abc'" "$tmp/pods/COLLECTION-TRUNCATED.txt"
  if [ -f "$tmp/pods/COLLECTION-NOTES.txt" ]; then echo "FAIL: split the note across two files"; false; fi
  rm -rf "$tmp"
}

@test "collect broken pods says nothing extra when both knobs are well formed" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"
  export STUB_POD_ROWS='ns-1  pod-1  0/1  Running  0  5m'

  PATH="$tmp:$PATH" COZY_REPORT_PODS_MAX=5 COZY_REPORT_PODS_BUDGET=300 cozyreport_collect_broken_pods "$tmp/pods"

  if [ -f "$tmp/pods/COLLECTION-NOTES.txt" ]; then echo "FAIL: invented a note about a value it accepted"; false; fi
  rm -rf "$tmp"
}

@test "collect broken pods reports a malformed tail alongside the other knobs" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"
  export STUB_POD_ROWS='ns-1  pod-1  0/1  Running  0  5m'

  PATH="$tmp:$PATH" COZY_REPORT_POD_TAIL=1O00 cozyreport_collect_broken_pods "$tmp/pods"

  # The header claims every numeric knob reports its fallback. Two of the three
  # doing it is the documentation being wrong about the third.
  grep -q "COZY_REPORT_POD_TAIL='1O00'" "$tmp/pods/COLLECTION-NOTES.txt"
  rm -rf "$tmp"
}

@test "pod tail value and the note about it never disagree" {
  # Two `case` patterns written to mean the same thing drift: `00` is all-digits
  # and non-zero as a string, so a split validator applies `--tail=00` (which is
  # --tail=0, no lines at all, then described as a container that never logged)
  # while the note claims the value was ignored. `10m` is the mirror image.
  for v in '' 2000 -1 0 00 007 10m 1O00 2000x abc; do
    value=$(COZY_REPORT_POD_TAIL="$v" cozyreport_pod_tail)
    note=$(COZY_REPORT_POD_TAIL="$v" cozyreport_pod_tail_note)
    if [ "$value" = "2000" ] && [ "$v" != "2000" ] && [ -n "$v" ] && [ -z "$note" ]; then
      echo "FAIL: COZY_REPORT_POD_TAIL='$v' fell back to 2000 with no note"
      false
    fi
    if [ "$value" != "2000" ] && [ -n "$note" ]; then
      echo "FAIL: COZY_REPORT_POD_TAIL='$v' was applied as '$value' while the note called it malformed"
      false
    fi
  done
}

@test "pod tail refuses a padded zero the same as a plain one" {
  # `--tail=00` reaches kubectl as 0 and returns nothing, which this script would
  # then report as a container that never started or stayed quiet.
  [ "$(COZY_REPORT_POD_TAIL=00 cozyreport_pod_tail)" = "2000" ]
  [ "$(COZY_REPORT_POD_TAIL=0 cozyreport_pod_tail)" = "2000" ]
}

@test "collect broken pods survives a read only temp dir without blaming the cluster" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"
  export STUB_POD_ROWS='ns-1  pod-1  0/1  Running  0  5m'
  # A path that does not exist, not one that is unwritable: root holds
  # CAP_DAC_OVERRIDE, so mktemp succeeds inside a 0500 directory and the test would
  # exercise the happy path and pass without ever entering the fallback it exists to
  # pin. The e2e sandbox runs as root. ENOENT is refused for everyone.
  PATH="$tmp:$PATH" TMPDIR="$tmp/gone" cozyreport_collect_broken_pods "$tmp/pods"

  # With no fallback for the scratch file the redirect itself fails, the command
  # substitution returns non-zero, and the report states that the pod list did not
  # return: a false claim about the cluster caused by the local filesystem.
  if [ -f "$tmp/pods/COLLECTION-FAILED.txt" ]; then
    echo "FAIL: blamed the cluster for an unwritable TMPDIR"
    cat "$tmp/pods/COLLECTION-FAILED.txt"
    false
  fi
  [ -f "$tmp/pods/ns-1/pod-1/pod.yaml" ]
  rm -rf "$tmp"
}

@test "collect broken pods still collects when timeout is missing and says so" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"
  export STUB_POD_ROWS='ns-1  pod-1  0/1  Running  0  5m'

  # An empty bound is what sourcing this script on a host without `timeout`
  # produces, since the question is answered once at file scope. Note and proceed,
  # not refuse: an unbounded read still collects the tree, so refusing would turn a
  # missing local binary into a lost section.
  PATH="$tmp:$PATH" COZYREPORT_BOUND="" cozyreport_collect_broken_pods "$tmp/pods"

  grep -q 'timeout is not on PATH' "$tmp/pods/COLLECTION-UNBOUNDED.txt"
  grep -q 'not a statement about the cluster' "$tmp/pods/COLLECTION-UNBOUNDED.txt"
  [ -f "$tmp/pods/ns-1/pod-1/pod.yaml" ]
  [ -f "$tmp/pods/ns-1/pod-1/logs-app.txt" ]
  rm -rf "$tmp"
}

@test "the tail note states the bound applied, not the one that was asked for" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  # A value the script cannot use falls back to 2000. The note inside each log
  # used to render that as `COZY_REPORT_POD_TAIL=2000`, which is a statement about
  # the environment and was false: the variable held the rejected value. Two files
  # in the same tarball then disagreed about what one variable was set to, and the
  # one a reader is most likely to open was the wrong one.
  PATH="$tmp:$PATH" COZY_REPORT_POD_TAIL=10m cozyreport_collect_pod ns chatty-0 "$tmp/out"

  grep -q 'holds at most the last 2000 lines' "$tmp/out/CONTAINER-LOGS-BOUND.txt"
  # And the log itself is left alone: it was read in full, so it is a complete
  # artifact and a trailing prose line would break every parser that could read
  # it before. Same reason pod.yaml carries its marker as a YAML comment; a log
  # format has no comment, so the note lives beside the file instead.
  if grep -q 'holds at most the last' "$tmp/out/logs-app.txt"; then
    echo "FAIL: the bound note is inside a successfully read log"
    false
  fi
  if grep -q 'COZY_REPORT_POD_TAIL=2000' "$tmp/out/CONTAINER-LOGS-BOUND.txt"; then
    echo "FAIL: the note claims the environment holds a value it does not"
    grep 'COZY_REPORT_POD_TAIL' "$tmp/out/CONTAINER-LOGS-BOUND.txt"
    false
  fi
  rm -rf "$tmp"
}

@test "a container log refused without a message is not filed as silence" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  PATH="$tmp:$PATH" STUB_LOG_MODE=silent cozyreport_collect_pod ns quiet-0 "$tmp/out"

  # Exit non-zero, no log, and a scratch file that exists and is empty. kubectl
  # refused and said nothing, which is not the same fact as a container that ran
  # and logged nothing, and only one of the two is about the cluster.
  grep -q 'without a message' "$tmp/out/logs-app.txt"
  if grep -q 'started and logged nothing' "$tmp/out/logs-app.txt"; then
    echo "FAIL: a refusal is reported as a silent container"
    cat "$tmp/out/logs-app.txt"
    false
  fi
  rm -rf "$tmp"
}

@test "a container list that stopped part way says the rest is unaccounted for" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  PATH="$tmp:$PATH" STUB_JSONPATH_FAIL=partial cozyreport_collect_pod ns half-0 "$tmp/out"

  # Names arrived and the read still failed, so the list is a prefix. The
  # empty-list branch never fires -- the list is not empty, it is incomplete --
  # and without a marker the directory holds logs for the containers kubectl got
  # to and nothing for the rest, which a reader counts as the whole pod.
  grep -q 'INCOMPLETE' "$tmp/out/CONTAINER-LIST-INCOMPLETE.txt" || {
    echo "FAIL: a partial container list is not marked"
    ls "$tmp/out"
    false
  }
  grep -q 'unexpected EOF' "$tmp/out/CONTAINER-LIST-INCOMPLETE.txt"
  # And the containers it did name are still collected: partial evidence is kept.
  [ -f "$tmp/out/logs-app.txt" ] || { echo "FAIL: the named container was not read"; false; }
  rm -rf "$tmp"
}

@test "a complete container list leaves no incompleteness marker" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  PATH="$tmp:$PATH" cozyreport_collect_pod ns whole-0 "$tmp/out"

  if [ -f "$tmp/out/CONTAINER-LIST-INCOMPLETE.txt" ]; then
    echo "FAIL: a clean container list was marked incomplete"
    cat "$tmp/out/CONTAINER-LIST-INCOMPLETE.txt"
    false
  fi
  rm -rf "$tmp"
}

@test "an empty container list refused without a message says which happened" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  PATH="$tmp:$PATH" STUB_JSONPATH_FAIL=silent cozyreport_collect_pod ns gone-0 "$tmp/out"

  # Four ways the container list comes back empty, and this is the one with
  # nothing to quote and no clock involved. Reported as its own outcome rather
  # than folded into the timeout branch above it or the no-temp-dir branch below.
  grep -q 'said nothing and exited 1' "$tmp/out/logs-UNAVAILABLE.txt"
  rm -rf "$tmp"
}

@test "an empty container list with nowhere to write its reason says that instead" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  # A refusal that is NOT a timeout, with no writable temp dir: the branch the
  # sibling test could not reach, because it paired an unwritable TMPDIR with a
  # timeout and the timeout branch is checked first.
  PATH="$tmp:$PATH" TMPDIR=/nonexistent-cozyreport-probe STUB_JSONPATH_FAIL=silent \
    cozyreport_collect_pod ns gone-0 "$tmp/out"

  grep -q 'could not be captured' "$tmp/out/logs-UNAVAILABLE.txt"
  if grep -q 'said nothing and exited' "$tmp/out/logs-UNAVAILABLE.txt"; then
    echo "FAIL: a lost message is reported as kubectl having been silent"
    cat "$tmp/out/logs-UNAVAILABLE.txt"
    false
  fi
  rm -rf "$tmp"
}

@test "a container list that names nothing is blank, not empty, and still reports" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  # The shape real kubectl produces, which the other tests in this group do not:
  # the jsonpath holds two expressions with a literal space between them, and that
  # space is emitted whatever the fields contain. A pod that names no container
  # therefore comes back as " ", never as "". Command substitution strips trailing
  # NEWLINES and not spaces, so a check written as `[ -z ]` is FALSE here and the
  # read falls through every branch meant to catch it: no logs-UNAVAILABLE.txt, no
  # CONTAINER-LIST-INCOMPLETE.txt, and a pod directory carrying pod.yaml and
  # describe.txt with no log file and nothing saying why -- which reads as a pod
  # whose containers were silent, the exact confusion this tree exists to remove.
  #
  # Exits 0 deliberately. A failing read writes no stdout, so it lands in the
  # branch correctly even with the bug; only a SUCCESSFUL read that names nothing
  # reaches this, which is why it survived the other tests here.
  PATH="$tmp:$PATH" STUB_CONTAINERS=' ' cozyreport_collect_pod ns quiet-0 "$tmp/out"

  if [ ! -f "$tmp/out/logs-UNAVAILABLE.txt" ]; then
    echo "FAIL: a blank container list left the pod directory with no logs and no explanation"
    ls -la "$tmp/out"
    false
  fi
  grep -q 'container list came back empty' "$tmp/out/logs-UNAVAILABLE.txt"
  # Not the prefix branch: nothing was named, so claiming kubectl named some
  # containers and stopped would invent a partial read that never happened.
  if [ -f "$tmp/out/CONTAINER-LIST-INCOMPLETE.txt" ]; then
    echo "FAIL: a blank list reported as a truncated one"
    cat "$tmp/out/CONTAINER-LIST-INCOMPLETE.txt"
    false
  fi
  rm -rf "$tmp"
}

@test "an object cut off by a signal does not name the deadline as the cause" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  PATH="$tmp:$PATH" STUB_OBJ_MODE=partial9 cozyreport_collect_pod ns hung-0 "$tmp/out"

  # 137 reaches the object files by the same path it reaches the logs, and has to
  # hedge there too: the kill grace produces it and so does anything else that
  # SIGKILLs the read. Proven for logs already; the object reader shares the
  # helper but not the test, and a helper shared by two callers is only covered
  # where it is called.
  grep -q 'SIGKILL' "$tmp/out/pod.yaml"
  grep -q 'does not tell apart' "$tmp/out/pod.yaml"
  rm -rf "$tmp"
}

@test "an object read killed before its first byte does not claim the object ended" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  PATH="$tmp:$PATH" STUB_OBJ_MODE=killed cozyreport_collect_pod ns hung-0 "$tmp/out"

  # The file never started, so "ends here" is the whole content. It still has to
  # say the read was cut off rather than leave a zero-byte pod.yaml, which is what
  # a pod that genuinely has no object would also leave.
  [ -s "$tmp/out/pod.yaml" ] || { echo "FAIL: nothing written for a killed object read"; false; }
  grep -q '^# \[cozyreport\] TRUNCATED' "$tmp/out/pod.yaml"
  rm -rf "$tmp"
}

@test "the read bound is resolved from the host at load time" {
  tmp=$(mktemp -d)
  # Only the binaries sourcing the script needs, and deliberately no `timeout`.
  for b in sh dirname; do
    for d in /bin /usr/bin; do
      [ -x "$d/$b" ] && { ln -sf "$d/$b" "$tmp/$b"; break; }
    done
  done

  # Resolved once at file scope so a caller that drives cozyreport_collect_pod
  # directly gets the same answer as the walk does. Defaulting to the bounded form
  # and correcting it inside the walk would hand such a caller five reads that exit
  # 127, and every log file would then blame the cluster for a missing binary.
  bound=$(PATH="$tmp" sh -c 'COZYREPORT_LIB=1; . "$1"; printf "%s" "$COZYREPORT_BOUND"' _ "$SCRIPT")
  if [ -n "$bound" ]; then echo "FAIL: claimed a bound of '$bound' with no timeout on PATH"; false; fi

  # The other direction, and it supplies its own `timeout` rather than asking the
  # host for one. Reading the ambient PATH here would make the verdict depend on
  # what the machine happens to have installed, which is the same defect this pair
  # of assertions exists to describe.
  printf '#!/bin/sh\nshift 2\nexec "$@"\n' > "$tmp/timeout"
  chmod +x "$tmp/timeout"
  bound=$(PATH="$tmp" sh -c 'COZYREPORT_LIB=1; . "$1"; printf "%s" "$COZYREPORT_BOUND"' _ "$SCRIPT")
  if [ -z "$bound" ]; then echo "FAIL: no bound with a timeout on PATH"; false; fi
  case "$bound" in
    timeout*) ;;
    *) echo "FAIL: the bound does not start with timeout: $bound"; false ;;
  esac
  rm -rf "$tmp"
}

@test "the cap never evicts a pod the uncapped collector always kept" {
  # The regression this ordering exists to prevent. A selector of
  # `$4 !~ /Running|Succeeded|Completed/` with no cap collects a CrashLoopBackOff
  # pod always. Widening it to include `0/1 Running` and capping the walk by count
  # are safe apart and only safe together if the wide class cannot displace the
  # narrow one.
  #
  # Fixture is the shape a failed install actually produces: more not-Ready pods
  # than the cap, nearly all of them newly-in-scope tenant pods, and one crash loop
  # in a platform namespace. Namespace-first ranking puts that crash loop after
  # every tenant row and the cap then drops it -- which is why the split is by
  # evidence class first.
  rows=$(i=1; while [ "$i" -le 60 ]; do
    printf 'tenant-test  unready-%02d  0/1  Running  0  5m\n' "$i"
    i=$((i + 1))
  done
  printf 'cozy-system  the-crashing-one  0/1  CrashLoopBackOff  7  9m\n')

  out=$(printf '%s\n' "$rows" | COZY_REPORT_PREFER_NS=tenant- cozyreport_pods_prioritize)

  # First, not merely present: the cap is a `head -n`, so surviving it means being
  # ordered ahead of the rows that would fill it.
  first=$(printf '%s\n' "$out" | sed -n '1p' | awk '{print $2}')
  if [ "$first" != "the-crashing-one" ]; then
    echo "FAIL: a crash-looping pod ranked behind newly-in-scope unready pods, so any cap drops it"
    printf '%s\n' "$out" | head -n 3
    false
  fi
  # It really would have been dropped: the competing rows outnumber the default cap.
  [ "$(printf '%s\n' "$out" | grep -c .)" -eq 61 ]
  # Tenant-first still holds WITHIN the newly-in-scope class -- the preference is
  # narrowed to its own class, not abandoned.
  after=$(printf '%s\n' "$out" | sed -n '2p' | awk '{print $1}')
  [ "$after" = "tenant-test" ]
  # No cleanup line here, deliberately: this test creates no directory. An
  # `rm -rf "$tmp"` added out of habit does NOT no-op -- `tmp` is hack/cozytest.sh's
  # OWN variable for the per-test scratch dir holding the captured log, and it is in
  # scope inside the test body. Removing it takes the runner's log with it, and the
  # test then fails after every assertion in it has already passed.
}

@test "an empty prefix disables the namespace order but not the class order" {
  rows="$(printf '%s\n' \
    'cozy-a  pod-a  0/1  Running  0  5m' \
    'tenant-test  pod-t  0/1  Running  0  5m')"

  out="$(printf '%s\n' "$rows" | COZY_REPORT_PREFER_NS='' cozyreport_pods_prioritize)"

  # The documented disable switch, within one evidence class: input order is kept
  # and no row is dropped. Without the guard the empty prefix matches every row in
  # the first emission and nothing in the second, which happens to look right here
  # and stops looking right the moment a caller relies on input order.
  [ "$(printf '%s\n' "$out" | grep -c .)" -eq 2 ]
  [ "$(printf '%s\n' "$out" | head -n 1 | awk '{print $2}')" = "pod-a" ]
}

@test "an empty prefix does not switch off the eviction guard" {
  # The switch is documented as disabling the ordering, and a reader setting it is
  # asking not to prefer a namespace. They are not asking for a crash-looping pod
  # to lose its container logs to a cluster full of unready ones -- but if the
  # class split sits behind the same guard as the namespace preference, that is
  # what one env value quietly buys.
  rows=$(i=1; while [ "$i" -le 60 ]; do
    printf 'aaa-ns  unready-%02d  0/1  Running  0  5m\n' "$i"
    i=$((i + 1))
  done
  printf 'zzz-system  the-crashing-one  0/1  CrashLoopBackOff  7  9m\n')

  out=$(printf '%s\n' "$rows" | COZY_REPORT_PREFER_NS='' cozyreport_pods_prioritize)

  # Namespace order is off, so `aaa-ns` would win any alphabetical or input-order
  # ranking; the crash loop is first only because the class split still applies.
  first=$(printf '%s\n' "$out" | sed -n '1p' | awk '{print $2}')
  if [ "$first" != "the-crashing-one" ]; then
    echo "FAIL: the empty prefix switched off the class ordering, so the cap drops the crash loop"
    printf '%s\n' "$out" | head -n 3
    false
  fi
  [ "$(printf '%s\n' "$out" | grep -c .)" -eq 61 ]
}

@test "prioritize treats the prefix as text and not as a pattern" {
  # `tenantXtest` sorts first and matches `tenant.test` only if the prefix is read
  # as a regular expression. The namespace the operator actually named is the one
  # that must come first.
  rows="$(printf '%s\n' \
    'tenantXtest    pod-regex    0/1  Running  0  5m' \
    'tenant.test-a  pod-literal  0/1  Running  0  5m' \
    'cozy-a         pod-a        0/1  Running  0  5m')"

  out="$(printf '%s\n' "$rows" | COZY_REPORT_PREFER_NS='tenant.test' cozyreport_pods_prioritize)"

  first=$(printf '%s\n' "$out" | head -n 1 | awk '{print $2}')
  if [ "$first" != "pod-literal" ]; then
    echo "FAIL: expected pod-literal first, got '$first' (prefix matched as a pattern)"
    false
  fi
}

@test "collect broken pods leaves no deadline behind for the next caller" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"
  export STUB_POD_ROWS='ns-1  pod-1  0/1  Running  0  5m'
  unset COZYREPORT_COLLECT_DEADLINE

  PATH="$tmp:$PATH" COZY_REPORT_PODS_BUDGET=900 cozyreport_collect_broken_pods "$tmp/pods"

  # Left set, it would silently bound whatever a later caller collects, against a
  # deadline computed for a walk that has already finished. That the deadline is
  # handed down in the first place is pinned by the budget-truncation test above,
  # which can only pass if the per-container check was in effect.
  if [ -n "${COZYREPORT_COLLECT_DEADLINE:-}" ]; then
    echo "FAIL: left COZYREPORT_COLLECT_DEADLINE='$COZYREPORT_COLLECT_DEADLINE' in the caller's environment"
    false
  fi
  rm -rf "$tmp"
}

@test "collect broken pods records that it stopped at the pod cap" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"
  export STUB_POD_ROWS="$(i=1; while [ "$i" -le 5 ]; do printf 'ns-%s  pod-%s  0/1  Running  0  5m\n' "$i" "$i"; i=$((i + 1)); done)"

  PATH="$tmp:$PATH" COZY_REPORT_PODS_MAX=2 cozyreport_collect_broken_pods "$tmp/pods"

  [ "$(find "$tmp/pods" -name pod.yaml | grep -c .)" -eq 2 ]
  grep -q 'collected 2 of 5 not-Ready pods' "$tmp/pods/COLLECTION-TRUNCATED.txt"
  rm -rf "$tmp"
}

@test "collect broken pods leaves no truncation marker when everything fits" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"
  export STUB_POD_ROWS='ns-1  pod-1  0/1  Running  0  5m'

  PATH="$tmp:$PATH" cozyreport_collect_broken_pods "$tmp/pods"

  if [ -f "$tmp/pods/COLLECTION-TRUNCATED.txt" ]; then echo "FAIL: claimed a truncation that did not happen"; false; fi
  rm -rf "$tmp"
}

@test "collect broken pods records that it ran out of collection budget" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"
  export STUB_POD_ROWS="$(i=1; while [ "$i" -le 5 ]; do printf 'ns-%s  pod-%s  0/1  Running  0  5m\n' "$i" "$i"; i=$((i + 1)); done)"

  PATH="$tmp:$PATH" COZY_REPORT_PODS_MAX=4 COZY_REPORT_PODS_BUDGET=0 cozyreport_collect_broken_pods "$tmp/pods"

  # The per-read timeouts are individually bounded but accumulate, so the pod cap
  # alone does not bound the walk. An overrun here is not a truncated pod section:
  # the tarball is written after this, so the job's own limit takes the entire
  # artifact, including everything collected before this point.
  grep -q 'collection budget elapsed' "$tmp/pods/COLLECTION-TRUNCATED.txt"
  # Denominator is what the walk could have reached (the cap already removed the
  # fifth pod before the loop began), not the full selection. "0 of 5" claimed a
  # reach the collector never had -- and this test pinned that claim, which is how
  # it survived. The full total is still reported, on the cap line below.
  grep -q 'stopped after 0 of 4 not-Ready pods it could reach' "$tmp/pods/COLLECTION-TRUNCATED.txt"
  # Both bounds applied, so both are named. An operator who reads only the budget
  # line and raises the budget gets 4 pods and no explanation for the fifth --
  # a bound hiding its own effect, which is what these markers exist to stop.
  grep -q 'COZY_REPORT_PODS_MAX=4 also applied' "$tmp/pods/COLLECTION-TRUNCATED.txt"
  grep -q 'only 4 of 5 not-Ready pods were eligible' "$tmp/pods/COLLECTION-TRUNCATED.txt"
  # The budget note still leads: it is the more proximate cause of stopping, and
  # "reached the cap" alone would send a reader looking for pods never attempted.
  head -n 1 "$tmp/pods/COLLECTION-TRUNCATED.txt" | grep -q 'collection budget elapsed'
  rm -rf "$tmp"
}

@test "the cap is not named when it did not apply" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"
  export STUB_POD_ROWS="$(i=1; while [ "$i" -le 3 ]; do printf 'ns-%s  pod-%s  0/1  Running  0  5m\n' "$i" "$i"; i=$((i + 1)); done)"

  # Cap above the selection, so only the budget bounded this walk. Naming a bound
  # that did not fire is the same defect in the other direction: it sends an
  # operator to raise a limit that was never reached.
  PATH="$tmp:$PATH" COZY_REPORT_PODS_MAX=40 COZY_REPORT_PODS_BUDGET=0 cozyreport_collect_broken_pods "$tmp/pods"

  grep -q 'stopped after 0 of 3 not-Ready pods it could reach' "$tmp/pods/COLLECTION-TRUNCATED.txt"
  if grep -q 'also applied' "$tmp/pods/COLLECTION-TRUNCATED.txt"; then
    echo "FAIL: named the pod cap although it never bit"
    cat "$tmp/pods/COLLECTION-TRUNCATED.txt"
    false
  fi
  rm -rf "$tmp"
}

@test "collect broken pods names the budget when it ran out inside the last pod" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"
  export STUB_POD_ROWS="$(i=1; while [ "$i" -le 5 ]; do printf 'ns-%s  pod-%s  0/1  Running  0  5m\n' "$i" "$i"; i=$((i + 1)); done)"

  # Two containers, and the first read outlasts the budget, so the second is
  # skipped and the pod really is short a log. The budget has headroom over the
  # per-pod setup on purpose: `date +%s` is whole-second, so a budget of 1 leaves
  # somewhere between 0 and 1 second for an mkdir plus three reads, and under load
  # the deadline fires before any container is read. Then this test passes for the
  # wrong reason and its sibling below fails on a loaded machine. The cap ends the loop, so no later
  # iteration notices: a cap-only note would date the stop to the wrong bound and
  # call that pod collected.
  export STUB_CONTAINERS='app sidecar'
  PATH="$tmp:$PATH" COZY_REPORT_PODS_MAX=1 COZY_REPORT_PODS_BUDGET=3 \
    STUB_LOG_MODE=slow STUB_LOG_SLEEP=4 cozyreport_collect_broken_pods "$tmp/pods"

  [ -f "$tmp/pods/ns-1/pod-1/CONTAINER-LOGS-TRUNCATED.txt" ]
  grep -q 'COZY_REPORT_PODS_MAX=1' "$tmp/pods/COLLECTION-TRUNCATED.txt"
  grep -q 'collection budget' "$tmp/pods/COLLECTION-TRUNCATED.txt"
  rm -rf "$tmp"
}

@test "collect broken pods claims no budget truncation when nothing was skipped" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"
  export STUB_POD_ROWS="$(i=1; while [ "$i" -le 5 ]; do printf 'ns-%s  pod-%s  0/1  Running  0  5m\n' "$i" "$i"; i=$((i + 1)); done)"

  # One container and a budget large enough that nothing is ever skipped. The point
  # is that "the clock is past the deadline" and "collection was cut short" are
  # different questions: answering the first files a truncation that did not happen,
  # pointing at a marker that does not exist. Driven without a sleep, so the
  # assertion does not rest on a whole-second clock beating a stub.
  export STUB_CONTAINERS='app'
  PATH="$tmp:$PATH" COZY_REPORT_PODS_MAX=1 COZY_REPORT_PODS_BUDGET=600 \
    cozyreport_collect_broken_pods "$tmp/pods"

  grep -q 'COZY_REPORT_PODS_MAX=1' "$tmp/pods/COLLECTION-TRUNCATED.txt"
  if grep -q 'collection budget' "$tmp/pods/COLLECTION-TRUNCATED.txt"; then
    echo "FAIL: reported a budget truncation with nothing skipped"
    cat "$tmp/pods/COLLECTION-TRUNCATED.txt"
    false
  fi
  rm -rf "$tmp"
}

@test "collect broken pods files no truncation for a healthy cluster on a spent budget" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"
  export STUB_POD_ROWS='kube-system  coredns-abc  1/1  Running  0  2h'

  PATH="$tmp:$PATH" COZY_REPORT_PODS_BUDGET=0 cozyreport_collect_broken_pods "$tmp/pods"

  # Nothing was selected, so nothing was skipped, however long the clock says the
  # run has been going.
  if [ -d "$tmp/pods" ]; then
    echo "FAIL: filed a truncation for a collection that had nothing to collect"
    ls -la "$tmp/pods"
    false
  fi
  rm -rf "$tmp"
}

@test "collect broken pods says so when the pod list never returned" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  PATH="$tmp:$PATH" STUB_PODLIST_FAIL=refuse cozyreport_collect_broken_pods "$tmp/pods"

  # A failed list and a healthy cluster both end with no pod directories. Without
  # this file the artifact cannot tell them apart, which is the same defect one
  # level up from the pod tree's own.
  [ -f "$tmp/pods/COLLECTION-FAILED.txt" ]
  grep -q 'NOT a report that every pod was Ready' "$tmp/pods/COLLECTION-FAILED.txt"
  # kubectl's actionable line comes last, after one discovery-retry line per
  # attempt. Quoting the first one and trimming it to 200 characters ships a
  # truncated klog message whose cause has been cut off the end.
  grep -q 'pods is forbidden' "$tmp/pods/COLLECTION-FAILED.txt"
  if grep -q 'Unhandled Error' "$tmp/pods/COLLECTION-FAILED.txt"; then echo "FAIL: quoted the discovery noise instead of the reason"; false; fi
  rm -rf "$tmp"
}

@test "collect broken pods tells a timed out pod list from a refused one" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"

  PATH="$tmp:$PATH" STUB_PODLIST_FAIL=timeout cozyreport_collect_broken_pods "$tmp/pods"

  # A read killed by its own timeout never got to write a reason, so quoting its
  # empty stderr yields a bare `kubectl said:` that names nothing. An apiserver
  # too slow to answer and one answering "forbidden" are different problems.
  #
  # The timeout branch names the mechanism rather than the exit code: this test
  # asserted `exited 124` while the message combined both cases in one sentence,
  # and that spelling survived the defect the sibling test now covers.
  grep -q 'the read was cut off by' "$tmp/pods/COLLECTION-FAILED.txt"
  if grep -q 'kubectl said: *$' "$tmp/pods/COLLECTION-FAILED.txt"; then echo "FAIL: quoted an empty reason"; false; fi
  rm -rf "$tmp"
}

@test "collect broken pods writes nothing at all when every pod is healthy" {
  tmp=$(mktemp -d)
  collect_stub_dir "$tmp"
  export STUB_ARGV="$tmp/argv"
  export STUB_POD_ROWS='kube-system  coredns-abc  1/1  Running  0  2h'

  PATH="$tmp:$PATH" cozyreport_collect_broken_pods "$tmp/pods"

  # An empty selection reaches the walk as a single blank line; a pod directory
  # named after nothing would be a broken pod that does not exist.
  if [ -d "$tmp/pods" ]; then echo "FAIL: created a pod tree for a healthy cluster"; false; fi
  # No pod was collected, so the only read that happened is the pod list, and it
  # is bounded like the per-pod ones. It is the read that decides whether the
  # report has a pod section at all, and it runs against the apiserver the run
  # has just finished degrading.
  [ "$(grep -c '^timeout 30$' "$tmp/argv")" -eq 1 ]
  rm -rf "$tmp"
}

@test "summary reads the age column correctly for a pod that has restarted" {
  tmp=$(mktemp -d)
  summary_stub_dir "$tmp"
  # kubectl prints RESTARTS as `7 (4m12s ago)` on a restarted pod -- three awk
  # fields --
  # which shifts AGE off $6. A restarted pod is the normal case in a failed run.
  rows='cozy-keycloak  keycloak-db-2  0/1  Running  7 (4m12s ago)  6m'

  out="$(PATH="$tmp:$PATH" STUB_POD_ROWS="$rows" sh "$HACK_DIR/cozyreport-summary.sh" 2>/dev/null)"

  printf '%s\n' "$out" | grep -q 'age=6m)'
  if printf '%s\n' "$out" | grep -q 'age=(4m12s'; then echo "FAIL: age read from the shifted column"; false; fi
  rm -rf "$tmp"
}

@test "summary still reads the age column for a pod that never restarted" {
  tmp=$(mktemp -d)
  summary_stub_dir "$tmp"
  rows='cozy-system  puller-z  0/1  ImagePullBackOff  0  4m'

  out="$(PATH="$tmp:$PATH" STUB_POD_ROWS="$rows" sh "$HACK_DIR/cozyreport-summary.sh" 2>/dev/null)"

  printf '%s\n' "$out" | grep -q 'restarts=0, age=4m)'
  rm -rf "$tmp"
}

@test "summary says how many not ready pods its listing left out" {
  tmp=$(mktemp -d)
  summary_stub_dir "$tmp"
  rows="$(i=1; while [ "$i" -le 45 ]; do printf 'ns-%s  pod-%s  0/1  Running  0  5m\n' "$i" "$i"; i=$((i + 1)); done)"

  out="$(PATH="$tmp:$PATH" STUB_POD_ROWS="$rows" sh "$HACK_DIR/cozyreport-summary.sh" 2>/dev/null)"

  # 45 broken pods, 40 listed. A cap that hides the rest reads like there is no
  # rest -- the same defect the per-pod capture reports rather than swallows.
  printf '%s\n' "$out" | grep -q '5 more not-Ready pod(s) not listed'
  rm -rf "$tmp"
}

@test "summary says so in every section whose read was killed" {
  tmp=$(mktemp -d)
  summary_stub_dir "$tmp"

  out="$(PATH="$tmp:$PATH" STUB_ALL_TIMEOUT=1 sh "$HACK_DIR/cozyreport-summary.sh" 2>/dev/null)"

  # Wrapping these reads in a timeout without keeping the status is worse than
  # leaving them unbounded: an unbounded read that hangs loses the tarball loudly,
  # a bounded one killed at 30s renders the section byte-identical to a healthy
  # cluster. An empty "HelmReleases not Ready" is the most misleading line this
  # file can carry, and it is the first file a triager opens.
  for section in 'HelmReleases not Ready' 'Recent OOMKilled' 'Recent Warning' 'Storage' 'Node Conditions'; do
    if ! printf '%s\n' "$out" | sed -n "/## $section/,/^## /p" | grep -q 'did not return'; then
      echo "FAIL: section '$section' rendered as a healthy cluster on a killed read"
      printf '%s\n' "$out" | sed -n "/## $section/,/^## /p"
      false
    fi
  done
  # And the status it names is the read's, not 0. A compound `if` whose condition
  # fails and which has no `else` leaves `$?` at 0, so a status read after the `if`
  # reports "exit 0" for every failure -- a line that says the read did not return
  # and then names a clean exit.
  if printf '%s\n' "$out" | grep -q 'did not return (exit 0)'; then
    echo "FAIL: reported a failed read as exit 0"
    false
  fi
  printf '%s\n' "$out" | grep -q 'did not return (exit 124)'
  rm -rf "$tmp"
}

@test "summary names kubectl's reason when a section read is refused" {
  tmp=$(mktemp -d)
  summary_stub_dir "$tmp"

  out="$(PATH="$tmp:$PATH" STUB_CRD_PRESENT=1 STUB_HR_FAIL=1 sh "$HACK_DIR/cozyreport-summary.sh" 2>/dev/null)"

  section="$(printf '%s\n' "$out" | sed -n '/## HelmReleases not Ready/,/^## /p')"
  # "did not return (exit 1)" names a symptom and no cause. An apiserver too slow
  # to answer and one answering "forbidden" are different problems with different
  # fixes, and this is the first file a triager opens.
  printf '%s\n' "$section" | grep -q 'did not return'
  if ! printf '%s\n' "$section" | grep -qi 'forbidden'; then
    echo "FAIL: dropped kubectl's reason for a refused read"
    printf '%s\n' "$section"
    false
  fi
  # The LAST stderr line, not the first: the stub emits a klog discovery-retry
  # line ahead of the real message, which is what a failing cluster-wide read
  # actually looks like. Quoting the first line names the retry, not the cause.
  if printf '%s\n' "$section" | grep -q "couldn't get current server API group list"; then
    echo "FAIL: quoted the klog retry line instead of kubectl's message"
    false
  fi
  rm -rf "$tmp"
}

@test "summary keeps a warning off a read that succeeded" {
  tmp=$(mktemp -d)
  summary_stub_dir "$tmp"

  out="$(PATH="$tmp:$PATH" STUB_CRD_PRESENT=1 STUB_HR_WARN=1 \
    STUB_HR_ROWS='tenant-a  app-1  5m  False  upgrade retries exhausted' \
    sh "$HACK_DIR/cozyreport-summary.sh" 2>/dev/null)"

  section="$(printf '%s\n' "$out" | sed -n '/## HelmReleases not Ready/,/^## /p')"
  # kubectl writes deprecation and partial-result warnings to stderr while exiting
  # 0. Capturing the read as `2>&1` would fold those into READ_OUT, and the awk
  # below would then parse the warning as a HelmRelease row -- a broken release
  # invented out of a notice, in the section a triager reads first. The reason is
  # captured to a file and read only on the failure path, so this must stay clean.
  if printf '%s\n' "$section" | grep -qi 'deprecated'; then
    echo "FAIL: a stderr warning leaked into the section body"
    printf '%s\n' "$section"
    false
  fi
  # And the read still succeeded, so its rows are there and no failure is claimed.
  printf '%s\n' "$section" | grep -q 'upgrade retries exhausted'
  if printf '%s\n' "$section" | grep -q 'did not return'; then
    echo "FAIL: reported a successful read as failed because it warned"
    false
  fi
  rm -rf "$tmp"
}

@test "a CRD probe whose message was lost is not filed as an absent CRD" {
  tmp=$(mktemp -d)
  summary_stub_dir "$tmp"
  # mktemp fails, so the scratch file is never created and kubectl's stderr goes
  # to /dev/null. The gate then has a non-zero status and nothing else.
  printf '#!/bin/sh\nexit 1\n' > "$tmp/mktemp"; chmod +x "$tmp/mktemp"

  out="$(PATH="$tmp:$PATH" STUB_CRD_FORBIDDEN=1 sh "$HACK_DIR/cozyreport-summary.sh" 2>/dev/null)"

  # Without a branch for this, the gate falls off the end of its if/elif and
  # returns silently, which is byte-identical to the CRD being absent. Every
  # section behind the gate then disappears from the summary, starting with
  # "## HelmReleases not Ready", on a report whose only real problem is that the
  # machine writing it had nowhere to put a temp file.
  printf '%s\n' "$out" | grep -q 'could not be captured' || {
    echo "FAIL: a lost message is rendered as an absent CRD"
    printf '%s\n' "$out"
    false
  }
  rm -rf "$tmp"
}

@test "a CRD probe that failed without saying anything says that much" {
  tmp=$(mktemp -d)
  summary_stub_dir "$tmp"

  out="$(PATH="$tmp:$PATH" STUB_CRD_SILENT=1 sh "$HACK_DIR/cozyreport-summary.sh" 2>/dev/null)"

  # kubectl is explicit about NotFound, so a non-zero exit with no message is not
  # absence. Filing it as absence is the same error in the other direction from
  # the test above: there, a local condition became a cluster fact; here, an
  # unexplained refusal would.
  printf '%s\n' "$out" | grep -q 'without a message' || {
    echo "FAIL: an unexplained refusal is rendered as an absent CRD"
    printf '%s\n' "$out"
    false
  }
  rm -rf "$tmp"
}

@test "summary names an OOMKilled event rather than leaving the heading bare" {
  tmp=$(mktemp -d)
  summary_stub_dir "$tmp"
  rows="$(printf '%s\n' \
    'tenant-a  3m  Normal  OOMKilling  Node/worker-1  Memory cgroup out of memory: Killed process 4711 (postgres)')"

  out="$(PATH="$tmp:$PATH" STUB_EVENT_ROWS="$rows" sh "$HACK_DIR/cozyreport-summary.sh" 2>/dev/null)"

  # The heading is printed unconditionally, so an empty section and a section
  # nobody could read look the same from outside. Nothing exercised the listing at
  # all, which is how a section stays empty for years without anyone noticing.
  section="$(printf '%s\n' "$out" | awk '/^## Recent OOMKilled/{f=1;next} f&&/^## /{exit} f')"
  printf '%s\n' "$section" | grep -q 'Killed process 4711' || {
    echo "FAIL: an OOMKilling event is not carried into the summary"
    printf '%s\n' "$out"
    false
  }
  rm -rf "$tmp"
}

@test "summary falls back on a malformed pod cap and says which value it used" {
  tmp=$(mktemp -d)
  summary_stub_dir "$tmp"
  rows='tenant-a  broken-0  0/1  Running  0  5m'

  out="$(PATH="$tmp:$PATH" STUB_POD_ROWS="$rows" COZY_REPORT_PODS_MAX=abc \
    sh "$HACK_DIR/cozyreport-summary.sh" 2>/dev/null)"

  # The collector reports a rejected knob and the summary has the same branch, but
  # only the collector's was covered. An operator who set the value is entitled to
  # know it was not the bound that applied, in whichever of the two files they are
  # reading.
  printf '%s\n' "$out" | grep -q "ignored a malformed COZY_REPORT_PODS_MAX" || {
    echo "FAIL: a malformed cap is discarded in silence"
    printf '%s\n' "$out"
    false
  }
  printf '%s\n' "$out" | grep -q 'broken-0' || {
    echo "FAIL: the fallback did not list anything"
    false
  }
  rm -rf "$tmp"
}

@test "a zero pod cap lists nothing and still reports what it left out" {
  tmp=$(mktemp -d)
  summary_stub_dir "$tmp"
  rows="$(printf '%s\n' \
    'tenant-a  broken-0  0/1  Running  0  5m' \
    'tenant-a  broken-1  0/1  Running  0  6m')"

  out="$(PATH="$tmp:$PATH" STUB_POD_ROWS="$rows" COZY_REPORT_PODS_MAX=0 \
    sh "$HACK_DIR/cozyreport-summary.sh" 2>/dev/null)"

  # Zero is a supported request, not a malformed one, and it is branched on rather
  # than passed to `head -n 0`, which GNU treats as empty and BSD rejects outright.
  # What must not happen is a silent zero: the overflow line is the only thing
  # standing between "asked for nothing" and "found nothing".
  if printf '%s\n' "$out" | grep -q 'broken-0'; then
    echo "FAIL: a zero cap still listed a pod"; false
  fi
  printf '%s\n' "$out" | grep -q '2 more not-Ready pod(s) not listed' || {
    echo "FAIL: a zero cap dropped two pods without saying so"
    printf '%s\n' "$out"
    false
  }
  rm -rf "$tmp"
}

@test "summary leaves a Ready HelmRelease out of the not-Ready listing" {
  tmp=$(mktemp -d)
  summary_stub_dir "$tmp"
  rows="$(printf '%s\n' \
    'tenant-a  broken   4m  False  Helm install failed for release tenant-a/broken' \
    'tenant-a  settled  9d  True   Release reconciliation succeeded')"

  out="$(PATH="$tmp:$PATH" STUB_CRD_PRESENT=1 STUB_HR_ROWS="$rows" \
    sh "$HACK_DIR/cozyreport-summary.sh" 2>/dev/null)"

  # Every HelmRelease fixture in this file was already not-Ready, so the selection
  # was only ever exercised in the direction that keeps rows. A section heading
  # that says "not Ready" and lists everything is the same defect as one that
  # lists nothing, and neither shows up without a row of the other kind.
  printf '%s\n' "$out" | grep -q 'tenant-a/broken' || { echo "FAIL: a failed release is not listed"; false; }
  if printf '%s\n' "$out" | grep -q 'tenant-a/settled'; then
    echo "FAIL: a Ready release is listed as not Ready"
    printf '%s\n' "$out"
    false
  fi
  rm -rf "$tmp"
}

@test "the pod cap does not silently bound the HelmRelease listing beside it" {
  tmp=$(mktemp -d)
  summary_stub_dir "$tmp"
  rows="$(printf '%s\n' \
    'tenant-a  hr-1  4m  False  Helm install failed one' \
    'tenant-a  hr-2  4m  False  Helm install failed two' \
    'tenant-a  hr-3  4m  False  Helm install failed three')"

  out="$(PATH="$tmp:$PATH" STUB_CRD_PRESENT=1 STUB_HR_ROWS="$rows" COZY_REPORT_PODS_MAX=1 \
    sh "$HACK_DIR/cozyreport-summary.sh" 2>/dev/null)"

  # The header says this knob does not move the HelmRelease cap, which has its own
  # at the same default. Sharing a default is not sharing a bound, and nothing
  # checked that the two had not been wired together.
  for hr in hr-1 hr-2 hr-3; do
    printf '%s\n' "$out" | grep -q "tenant-a/$hr" || {
      echo "FAIL: $hr was dropped by the pod cap"
      printf '%s\n' "$out"
      false
    }
  done
  rm -rf "$tmp"
}

@test "a pod list cut off part way through is not called an empty section" {
  tmp=$(mktemp -d)
  summary_stub_dir "$tmp"

  out="$(PATH="$tmp:$PATH" STUB_PODLIST_FAIL=partial \
    STUB_POD_ROWS='tenant-a  broken-0  0/1  Running  0  5m' \
    sh "$HACK_DIR/cozyreport-summary.sh" 2>/dev/null)"

  # kubectl printed a row and then failed. The rows are kept, as everywhere else
  # here, because partial evidence is the only evidence there is -- but the
  # sentence above them said the section was empty because nothing could be read,
  # while the row sat three lines below it. One read, two contradictory statements,
  # in the file a triager opens first.
  printf '%s\n' "$out" | grep -q 'broken-0' || {
    echo "FAIL: the rows that did arrive were discarded"; printf '%s\n' "$out"; false
  }
  printf '%s\n' "$out" | grep -q 'before it stopped, and there may be others' || {
    echo "FAIL: a partial list is not described as partial"
    printf '%s\n' "$out"
    false
  }
  if printf '%s\n' "$out" | grep -q 'this section is empty because nothing could be read'; then
    echo "FAIL: a section holding rows is called empty"
    false
  fi
  rm -rf "$tmp"
}

@test "summary lists a PVC that is not Bound and leaves the bound ones out" {
  tmp=$(mktemp -d)
  summary_stub_dir "$tmp"
  rows="$(printf '%s\n' \
    'tenant-a  data-0    Pending  ''  ''  ''  standard  4m' \
    'tenant-a  data-1    Bound    pvc-aaa  10Gi  RWO  standard  9d')"

  out="$(PATH="$tmp:$PATH" STUB_PVC_ROWS="$rows" sh "$HACK_DIR/cozyreport-summary.sh" 2>/dev/null)"

  # STATUS is the third column of `kubectl get pvc -A`. An index picked by eye and
  # never run against a row is how the services selector matched nothing for its
  # whole life; this section has the same shape and had no fixture at all.
  printf '%s\n' "$out" | grep -q 'PVC tenant-a/data-0' || {
    echo "FAIL: an unbound PVC is not listed"; printf '%s\n' "$out"; false
  }
  if printf '%s\n' "$out" | grep -q 'data-1'; then
    echo "FAIL: a Bound PVC is listed as a problem"; false
  fi
  rm -rf "$tmp"
}

@test "summary lists a PV that is not Bound and leaves the bound ones out" {
  tmp=$(mktemp -d)
  summary_stub_dir "$tmp"
  # `kubectl get pv` has no NAMESPACE column, so STATUS lands at $5 rather than
  # $3. Two sections, two different indexes, one line apart in the source.
  rows="$(printf '%s\n' \
    'pvc-aaa  10Gi  RWO  Delete  Released  tenant-a/data-9  standard  3d' \
    'pvc-bbb  10Gi  RWO  Delete  Bound     tenant-a/data-1  standard  9d')"

  out="$(PATH="$tmp:$PATH" STUB_PV_ROWS="$rows" sh "$HACK_DIR/cozyreport-summary.sh" 2>/dev/null)"

  printf '%s\n' "$out" | grep -q 'PV pvc-aaa' || {
    echo "FAIL: a released PV is not listed"; printf '%s\n' "$out"; false
  }
  if printf '%s\n' "$out" | grep -q 'PV pvc-bbb'; then
    echo "FAIL: a Bound PV is listed as a problem"; false
  fi
  rm -rf "$tmp"
}

@test "summary lists a Certificate that is not Ready and leaves ready ones out" {
  tmp=$(mktemp -d)
  summary_stub_dir "$tmp"
  rows="$(printf '%s\n' \
    'tenant-a  wildcard  False  wildcard-tls  12m' \
    'tenant-a  api       True   api-tls       6d')"

  out="$(PATH="$tmp:$PATH" STUB_CRD_PRESENT=1 STUB_CERT_ROWS="$rows" \
    sh "$HACK_DIR/cozyreport-summary.sh" 2>/dev/null)"

  printf '%s\n' "$out" | grep -q 'tenant-a/wildcard' || {
    echo "FAIL: a Certificate that is not Ready is not listed"; printf '%s\n' "$out"; false
  }
  if printf '%s\n' "$out" | grep -q 'tenant-a/api'; then
    echo "FAIL: a Ready Certificate is listed as a problem"; false
  fi
  rm -rf "$tmp"
}

@test "summary names a pod that cannot pull its image and skips the healthy ones" {
  tmp=$(mktemp -d)
  summary_stub_dir "$tmp"
  rows="$(printf '%s\n' \
    'tenant-a  puller-0  0/1  ImagePullBackOff  0  4m' \
    'tenant-a  puller-1  0/1  ErrImagePull      0  4m' \
    'tenant-a  fine-0    1/1  Running           0  4m')"

  out="$(PATH="$tmp:$PATH" STUB_POD_ROWS="$rows" sh "$HACK_DIR/cozyreport-summary.sh" 2>/dev/null)"

  # Scoped to the section, not to the whole document. Both of these pods are also
  # not-Ready, so they appear under the pod listing above regardless, and an
  # unscoped grep passes with this section deleted entirely.
  section="$(printf '%s\n' "$out" | awk '/^## ImagePullBackOff/{f=1;next} f&&/^## /{exit} f')"

  # Both spellings, because the section matches an alternation and dropping either
  # half leaves the other passing. ErrImagePull is the one a cluster shows first.
  printf '%s\n' "$section" | grep -q 'puller-0' || { echo "FAIL: ImagePullBackOff not named"; printf '%s\n' "$section"; false; }
  printf '%s\n' "$section" | grep -q 'puller-1' || { echo "FAIL: ErrImagePull not named"; printf '%s\n' "$section"; false; }
  if printf '%s\n' "$section" | grep -q 'fine-0'; then
    echo "FAIL: a running pod appears under the image-pull heading"; false
  fi
  rm -rf "$tmp"
}

@test "summary separates a CRD it may not read from one that is absent" {
  tmp=$(mktemp -d)
  summary_stub_dir "$tmp"

  absent="$(PATH="$tmp:$PATH" sh "$HACK_DIR/cozyreport-summary.sh" 2>/dev/null)"
  refused="$(PATH="$tmp:$PATH" STUB_CRD_FORBIDDEN=1 sh "$HACK_DIR/cozyreport-summary.sh" 2>/dev/null)"

  # An absent CRD is not a finding: most clusters do not run every optional
  # component, and a line about each one would be noise that trains the reader to
  # skip the section.
  if printf '%s\n' "$absent" | grep -q 'presence check failed'; then
    echo "FAIL: reported an absent CRD as a failure"
    false
  fi
  # A refusal skips the section identically, and only kubectl's message separates
  # "not installed" from "not allowed to look" -- the difference between a cluster
  # without cert-manager and a report taken with a token that cannot see it.
  if ! printf '%s\n' "$refused" | grep -q 'presence check failed'; then
    echo "FAIL: a refused CRD probe was indistinguishable from an absent CRD"
    false
  fi
  printf '%s\n' "$refused" | grep -qi 'forbidden'
  rm -rf "$tmp"
}

@test "summary keeps the whole helmrelease failure message" {
  tmp=$(mktemp -d)
  summary_stub_dir "$tmp"
  export STUB_CRD_PRESENT=1
  # Padded to kubectl's own column widths. With single spaces the row survives an
  # awk that mishandles the field skip, so the fixture has to look like real output.
  export STUB_HR_ROWS='cozy-keycloak   keycloak                 9d    False  Helm upgrade failed for release cozy-keycloak/keycloak: timeout waiting for the condition'

  out="$(PATH="$tmp:$PATH" sh "$HACK_DIR/cozyreport-summary.sh" 2>/dev/null)"

  # STATUS is the Ready condition's message, a whole sentence -- see the printer
  # columns on the CRD this repo ships. Taking one field renders every failed
  # release as "Helm" and drops the part that says what broke, in the first section
  # of the first file a triager opens.
  #
  # The whole rendered line is pinned, not just a substring of the message: an awk
  # that skips the wrong number of fields leaves the namespace and name in the
  # message too, and every substring assertion still passes in that state.
  rendered=$(printf '%s\n' "$out" | grep -F 'cozy-keycloak/keycloak —')
  [ "$rendered" = "  cozy-keycloak/keycloak — Helm upgrade failed for release cozy-keycloak/keycloak: timeout waiting for the condition" ] || {
    echo "FAIL: rendered line is not the namespace, name and message alone:"
    printf '%s\n' "[$rendered]"
    false
  }
  rm -rf "$tmp"
}

@test "summary ends with a newline" {
  tmp=$(mktemp -d)
  summary_stub_dir "$tmp"

  # The node section is the last thing the file prints, so it has to produce output
  # for this to mean anything.
  export STUB_NODE_ROWS='node0   True    False   False'

  PATH="$tmp:$PATH" sh "$HACK_DIR/cozyreport-summary.sh" > "$tmp/summary.txt" 2>/dev/null

  grep -q 'node0' "$tmp/summary.txt"

  # Command substitution strips the trailing newline, so a section that prints
  # $READ_OUT with a bare %s leaves the file ending mid-line.
  [ -z "$(tail -c 1 "$tmp/summary.txt")" ]
  rm -rf "$tmp"
}

@test "summary says how many not ready helmreleases its listing left out" {
  tmp=$(mktemp -d)
  summary_stub_dir "$tmp"
  export STUB_CRD_PRESENT=1
  export STUB_HR_ROWS="$(i=1; while [ "$i" -le 45 ]; do printf 'ns-%s  hr-%s  1  False  Helm upgrade failed for release ns-%s/hr-%s: timeout waiting for the condition\n' "$i" "$i" "$i" "$i"; i=$((i + 1)); done)"

  out="$(PATH="$tmp:$PATH" sh "$HACK_DIR/cozyreport-summary.sh" 2>/dev/null)"

  # An install that fails early leaves well over 40 HelmReleases not Ready, and a
  # listing that stops at 40 in silence reads as the complete set. Same defect the
  # pod section one heading below was already fixed for.
  printf '%s\n' "$out" | grep -q '5 more not-Ready HelmRelease(s) not listed'
  rm -rf "$tmp"
}

@test "summary distinguishes an absent crd from a check that was killed" {
  tmp=$(mktemp -d)
  summary_stub_dir "$tmp"

  absent="$(PATH="$tmp:$PATH" sh "$HACK_DIR/cozyreport-summary.sh" 2>/dev/null)"
  killed="$(PATH="$tmp:$PATH" STUB_ALL_TIMEOUT=1 sh "$HACK_DIR/cozyreport-summary.sh" 2>/dev/null)"

  # `if $BOUND kubectl get crd X` collapses "not installed" and "could not ask"
  # into one skip, and a slow aggregated discovery layer -- the state that made the
  # bound necessary -- then hides the section as though the CRD were absent.
  if printf '%s\n' "$absent" | grep -q 'presence check did not return'; then
    echo "FAIL: called an absent CRD a failed check"
    false
  fi
  printf '%s\n' "$killed" | grep -q 'presence check did not return'
  rm -rf "$tmp"
}

@test "summary lists a flux source that is not ready" {
  tmp=$(mktemp -d)
  summary_stub_dir "$tmp"
  export STUB_CRD_PRESENT=1
  export STUB_FLUX_ROWS="$(printf '%s\n' \
    'cozy-system  cozystack-packages  True' \
    'cozy-system  broken-repo         False')"

  out="$(PATH="$tmp:$PATH" sh "$HACK_DIR/cozyreport-summary.sh" 2>/dev/null)"

  # This section printed nothing on every report ever taken: its jsonpath nested a
  # filter inside a filter, which kubectl rejects with "unterminated filter", and
  # discarding stderr and the exit status hides it completely. An empty section
  # reads as "all Flux sources are Ready", inside the file whose job is to say what
  # is broken.
  printf '%s\n' "$out" | sed -n '/## Flux Sources/,/^## /p' | grep -q 'broken-repo'
  if printf '%s\n' "$out" | sed -n '/## Flux Sources/,/^## /p' | grep -q 'cozystack-packages'; then
    echo "FAIL: listed a Ready source"
    false
  fi
  rm -rf "$tmp"
}

@test "summary says the pod list failed rather than showing an empty section" {
  tmp=$(mktemp -d)
  summary_stub_dir "$tmp"

  out="$(PATH="$tmp:$PATH" STUB_PODLIST_FAIL=refuse sh "$HACK_DIR/cozyreport-summary.sh" 2>/dev/null)"

  # summary.txt is the file a triager opens first. A refused pod list renders
  # byte-identical to a healthy cluster there, which is the one run on which this
  # section must not be the thing that says nothing is wrong.
  printf '%s\n' "$out" | grep -q 'NOT because every pod was Ready'
  printf '%s\n' "$out" | grep -q 'pods is forbidden'
  # Every section derived from that same read has to inherit its failure. The
  # ImagePullBackOff section is byte-identical between a healthy cluster and a
  # refused list otherwise, and it is a heading a triager jumps straight to.
  printf '%s\n' "$out" | sed -n '/## ImagePullBackOff/,/^## /p' | grep -q 'did not return'
  rm -rf "$tmp"
}

@test "summary lists the tenant pods before the platform ones" {
  tmp=$(mktemp -d)
  summary_stub_dir "$tmp"
  rows="$(printf '%s\n' \
    'cozy-a  pod-a  0/1  Running  0  5m' \
    'tenant-test  the-pod-the-run-died-on  0/1  Running  0  5m')"

  out="$(PATH="$tmp:$PATH" STUB_POD_ROWS="$rows" sh "$HACK_DIR/cozyreport-summary.sh" 2>/dev/null)"

  # Shares the collector's ordering, so the rows the summary's cap keeps are the
  # rows whose evidence was actually collected.
  # Both line numbers are captured before they are compared: a missing row makes
  # `grep -n` empty, and `[ "" -lt "" ]` aborts the test body rather than failing
  # it, which bats reports as a test that never ran instead of one that broke. A
  # guard whose failure mode is to disappear is the same silent green this whole
  # change is about.
  pos_tenant=$(printf '%s\n' "$out" | grep -n 'the-pod-the-run-died-on' | head -n 1 | cut -d: -f1)
  pos_cozy=$(printf '%s\n' "$out" | grep -n 'pod-a' | head -n 1 | cut -d: -f1)
  if [ -z "$pos_tenant" ] || [ -z "$pos_cozy" ]; then
    echo "FAIL: expected both rows listed, got tenant='$pos_tenant' cozy='$pos_cozy'"
    false
  fi
  [ "$pos_tenant" -lt "$pos_cozy" ]
  rm -rf "$tmp"
}

@test "summary works without timeout and does not call that a cluster failure" {
  tmp=$(mktemp -d)
  summary_stub_dir "$tmp"
  # Only the binaries the summary genuinely needs, and deliberately no `timeout`.
  # Resolved from absolute candidates rather than through `command -v`, which in an
  # interactive shell can answer with a function name and leave a broken symlink.
  for b in sh date awk grep head sed tail cut mktemp rm cat dirname; do
    for d in /bin /usr/bin; do
      [ -x "$d/$b" ] && { ln -sf "$d/$b" "$tmp/$b"; break; }
    done
  done
  if [ -e "$tmp/timeout" ]; then echo "FAIL: the stub PATH must not provide timeout"; false; fi

  out="$(PATH="$tmp" STUB_POD_ROWS='cozy-system  broken-0  0/1  Running  0  5m' sh "$HACK_DIR/cozyreport-summary.sh" 2>/dev/null)"

  # Every read exits 127 without the fallback, and the section then reports that
  # the cluster did not answer on a host whose only problem is a missing local
  # binary. The collector says COLLECTION-UNBOUNDED.txt for that case, and the
  # two must not contradict each other inside one tarball.
  printf '%s\n' "$out" | grep -q 'broken-0'
  if printf '%s\n' "$out" | grep -q 'NOT because every pod was Ready'; then echo "FAIL: blamed the cluster for a missing local timeout"; false; fi
  rm -rf "$tmp"
}

@test "summary lists no pod at all when nothing is broken" {
  tmp=$(mktemp -d)
  summary_stub_dir "$tmp"

  out="$(PATH="$tmp:$PATH" STUB_POD_ROWS='' sh "$HACK_DIR/cozyreport-summary.sh" 2>/dev/null)"

  # An empty selection still reaches awk as one blank line. Printing it yields a
  # nameless broken pod — on every green run, and on the run where the pod list
  # itself failed, which is exactly when the summary must not invent findings.
  if printf '%s\n' "$out" | grep -q '^  / —'; then echo "FAIL: invented a nameless not-Ready pod"; false; fi
  if printf '%s\n' "$out" | grep -q 'ready= '; then echo "FAIL: printed a row of blank fields"; false; fi
  rm -rf "$tmp"
}

@test "summary prints no overflow line when everything fits" {
  tmp=$(mktemp -d)
  summary_stub_dir "$tmp"
  rows='cozy-system  puller-z  0/1  ImagePullBackOff  0  4m'

  out="$(PATH="$tmp:$PATH" STUB_POD_ROWS="$rows" sh "$HACK_DIR/cozyreport-summary.sh" 2>/dev/null)"

  if printf '%s\n' "$out" | grep -q 'more not-Ready pod(s) not listed'; then echo "FAIL: claimed an overflow that did not happen"; false; fi
  rm -rf "$tmp"
}

@test "pods not ready reads readiness from the ready column not the status text" {
  # The split has to read $3. Proving that needs rows the split actually reaches,
  # which means STATUS=Running: anything else is selected by the phase rule above
  # it and never gets there, so a fixture like `Init:0/2` pins nothing at all no
  # matter what its other columns say.
  #
  # `1/1 Running` with a slash-bearing STATUS is the row that decides it. Point
  # the split at $4 and `Running` has no slash, so `split()` returns 1, the
  # condition is false, and a healthy pod is dropped -- which is what the first
  # assertion catches. The `0/1 Running` row on its own would survive that swap.
  ready='tenant-test  settled-0  1/1  Running  0  9m'
  unready='tenant-test  probing-0  0/1  Running  0  3m'

  out="$(printf '%s\n' "$unready" | cozyreport_pods_not_ready)"
  printf '%s\n' "$out" | grep -q 'probing-0' || {
    echo "FAIL: a pod whose readiness probe never passed was dropped"
    false
  }

  out="$(printf '%s\n' "$ready" | cozyreport_pods_not_ready)"
  if printf '%s\n' "$out" | grep -q 'settled-0'; then
    echo "FAIL: a fully ready running pod was selected"
    false
  fi
}
