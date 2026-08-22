# shellcheck shell=bash
# Shared helper: point tenant Kubernetes e2e worker nodes at the in-sandbox
# ghcr.io pull-through registry (hack/e2e-ghcr-mirror.yaml) when it is up, via the
# chart's `talos.registryMirrors` knob, otherwise emit nothing so workers fall back
# to pulling ghcr.io directly.
#
# Why: tenant worker Talos nodes pull `ghcr.io/siderolabs/kubelet` directly; that
# egress is flaky/rate-limited from the CI runner and the pull times out with a TLS
# handshake timeout, so the kubelet service never starts and no tenant node joins.
# Diagnosed in cozystack/cozystack#3548 (in-guest Talos dmesg), tracked by #3513.
# Same flaky-public-egress class the talos-image-cache (#3231) fixed for the OS image.
# One variant of that flake, not the whole of it: #3513 also records failures that
# lose the budget before Talos ever reaches the kubelet pull, and those this cannot
# touch.
#
# WHAT THE GATE BELOW ESTABLISHES, precisely, because it is easy to overstate: the
# Deployment went Available, and the API accepted the egress allow. Neither observes
# the tenant-side datapath. If the allow selects an identity the worker's pull does
# not actually carry (see assumption 2 in hack/e2e-ghcr-mirror.yaml), workers are
# pointed at a ClusterIP whose SYNs are dropped, and Talos burns a TCP connect
# timeout per pull before falling back to ghcr.io -- slower than not mirroring at
# all, on exactly the runs this exists to protect. So: the fallback paths cannot
# make CI worse, the committed path is only as safe as the assumptions listed
# there.
#
# talos-image-cache.sh gates the equivalent decision on a byte-level 206 from a Pod
# carrying the consumer's own label, which is what turns that assumption into a
# measurement. Doing the same here is the hardening follow-up, and it is also what
# would let the suite assert the mirror served the pull rather than the worker
# quietly falling back.

GHCR_MIRROR_SVC_URL="${GHCR_MIRROR_SVC_URL:-http://ghcr-mirror.kube-system.svc}"
_GHCR_MIRROR_DECISION_FILE="${_GHCR_MIRROR_DECISION_FILE:-/tmp/e2e-ghcr-mirror-endpoint}"
GHCR_MIRROR_MANIFEST="${GHCR_MIRROR_MANIFEST:-hack/e2e-ghcr-mirror.yaml}"
GHCR_WARM_VERSIONS_FILE="${GHCR_WARM_VERSIONS_FILE:-packages/apps/kubernetes/files/versions.yaml}"
GHCR_WARM_JOB_NAME="${GHCR_WARM_JOB_NAME:-ghcr-mirror-warm}"
GHCR_WARM_REPO="${GHCR_WARM_REPO:-siderolabs/kubelet}"
GHCR_WARM_READY_TIMEOUT="${GHCR_WARM_READY_TIMEOUT:-600}"
# How many minors to warm. Two, because the kubernetes-latest and
# kubernetes-previous suites resolve the top two keys and no other suite spins a
# tenant on a third. Raise it if a suite starts selecting deeper; the cost is
# ~61 MiB of upstream fetch per extra tag on the leg this feature exists for
# (one platform's config plus layers, measured for v1.35.6 against ghcr.io).
GHCR_WARM_TAG_COUNT="${GHCR_WARM_TAG_COUNT:-2}"

# ghcr_warm_tags: print the kubelet image tags worth pre-fetching, one per line,
# newest minor first.
#
# Read from the chart's own version map rather than a list kept here. The suites
# pick their Kubernetes version out of that same file at runtime, so a copy would
# drift silently on the next version bump: the tenant would boot on a tag nothing
# warmed and the first worker would pay the cold upstream fetch this exists to
# remove, with no signal that the warm-up had missed.
#
# Ordered by minor, highest first, which is the order the suites resolve: they pass
# `keys | sort_by(.) | .[-1]` and `.[-2]` to run_kubernetes_test. Sorting here rather
# than reading the file's own order is not a guard against the file being written
# some other way -- packages/apps/kubernetes/hack/update-versions.sh regenerates it
# through `sort -V -r`, so highest-first is what the generator guarantees and a new
# minor lands at the top, not the bottom. It is so that this list and the suites
# agree by construction instead of by both happening to read the same file the same
# way. The caller warms a prefix of this, so the order decides what gets warmed.
ghcr_warm_tags() {
  if ! command -v yq >/dev/null 2>&1; then
    echo "yq is not installed, so the kubelet version map could not be read" >&2
    return 1
  fi
  yq 'to_entries | sort_by(.key) | reverse | .[].value' \
    "$GHCR_WARM_VERSIONS_FILE"
}

# ghcr_warm_job_manifest <image> <tag>...: print the warm-up Job, or fail.
#
# Pure string builder, kept separate so it can be unit-tested, same reason
# talos-image-cache.sh keeps its probe overrides out of the caller.
#
# The Job asks the mirror for each tag's index, every manifest the index lists, and
# every blob those manifests reference, discarding the bytes. distribution's proxy
# populates its blobstore on the way past, so the tenant worker's later pull is a
# local cache hit instead of a live fetch across the leg that stalls.
#
# "On the way past" is not free, and the cost is worth stating exactly. In
# distribution 2.8 a local miss reads the blob from upstream TWICE: ServeBlob starts
# `go storeLocal(...)`, which copies remote -> local, and separately runs
# copyContent(ctx, dgst, w) to serve the client; both open remoteStore. There is no
# tee. So a warm-up blob GET crosses the throttled leg twice.
#
# What keeps that from being a net addition is warming ONE platform per tag -- the
# one the workers run. Then the Job fetches the same blobs the first worker would
# have fetched, and the 2x is paid once either way: by the Job now, or by the worker
# later inside its node-Ready budget. Walking both platforms broke exactly this: the
# arm64 half is bytes nobody in the sandbox pulls, so it was a true addition to the
# leg, and the claim that the total was unchanged was false while it was there.
#
# The byte figures below are image sizes, not measurements of the mirror's upstream
# side, so read them as a lower bound on install-time egress.
#
# Selecting linux/amd64 out of the index stays a grep because digest and platform
# share a record: normalise the whitespace, split on record boundaries, take the
# record that names the architecture. Pulling every sha256 out of a document is a grep,
# and a digest that turns out not to be a blob answers 404. That is free on the
# manifest leg, where the walk just moves on; on the blob leg a 404 counts as a miss
# and fails the Job, which is what a single-arch tag would do -- its config and layer
# digests are not manifests, so the walk finds no index to descend.
ghcr_warm_job_manifest() {
  local image="$1" tag tags=""
  shift
  [ "$#" -gt 0 ] || return 1
  # An empty endpoint or repo does not fail anything downstream, which is why it has
  # to fail here. The Job would still build, run, exit 0, and print "index
  # unavailable" for every tag while the cache stayed cold -- and from inside the
  # Pod that is indistinguishable from a mirror that is simply unreachable.
  [ -n "$GHCR_MIRROR_SVC_URL" ] || return 1
  [ -n "$GHCR_WARM_REPO" ] || return 1
  for tag in "$@"; do
    # Refuse anything that is not a bare version. These tags land in a `for tag in
    # ...` inside the container's shell, where a substitution would run as the Job.
    # The version map is in-tree today, which is why the guard is cheap to add now
    # and expensive to retrofit later. No real kubelet tag is rejected by this.
    # Character class first, regex second. `grep -q` succeeds when ANY line of its
    # input matches and `$` anchors end-of-LINE, so a value whose first line is a
    # bare version passes the pattern while everything after the newline is pasted
    # into the heredoc as shell. The earlier list of rejected inputs enumerated ways
    # to inject -- $(), backticks, semicolon, space -- and missed this one, because
    # it was reasoned about as "more than one token" rather than as what the anchor
    # treats as a boundary. This case does not depend on that.
    case "$tag" in
      *[!v0-9.]*) return 1 ;;
    esac
    printf '%s' "$tag" | grep -qE '^v[0-9]+\.[0-9]+\.[0-9]+$' || return 1
    tags="${tags}${tags:+ }${tag}"
  done
  cat <<EOF
apiVersion: batch/v1
kind: Job
metadata:
  name: ${GHCR_WARM_JOB_NAME}
  namespace: kube-system
  labels:
    app.kubernetes.io/name: ${GHCR_WARM_JOB_NAME}
spec:
  # Bounded two ways, because retries alone are not a bound here. The fetches are
  # slow by nature on the leg this exists for, so a Job with only backoffLimit can
  # sit pulling on a sandbox node for hours after the suites it was meant to help
  # have finished, competing for the CPU the tenant guests are already short of.
  backoffLimit: 3
  activeDeadlineSeconds: 3600
  # Outlives the suites on purpose. The node-join failure block is the only reader
  # of this Job's outcome and it fires at the 29m node-join deadline, in a suite
  # that starts tens of minutes after install; a shorter TTL deletes the Job and
  # its log before the one moment anybody asks whether the warm-up worked.
  ttlSecondsAfterFinished: 14400
  template:
    metadata:
      labels:
        app.kubernetes.io/name: ${GHCR_WARM_JOB_NAME}
    spec:
      restartPolicy: Never
      automountServiceAccountToken: false
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        runAsGroup: 65532
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: warm
          image: ${image}
          command: ["/bin/sh", "-ec"]
          args:
            - |
              root="${GHCR_MIRROR_SVC_URL}"
              base="\$root/v2/${GHCR_WARM_REPO}"
              acc='Accept: application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json'
              # -U names this traffic in the registry's access log. The node-join
              # diagnostic counts GETs for the kubelet image to answer "did a worker
              # pull through the mirror"; without a way to tell the two apart, this
              # Job's own 50-odd requests would make that answer yes on every run.
              get() { wget -q -U ${GHCR_WARM_JOB_NAME} "\$@"; }
              digests() { grep -o 'sha256:[0-9a-f]\{64\}' | sort -u; }
              # Wait for the mirror to serve before concluding anything. This Job is
              # created seconds after the mirror Deployment, the registry image is
              # deliberately not in the pre-pull set, and the readiness probe adds
              # its own delay -- so at submit time the Service usually has no
              # endpoints at all. Treating that as "nothing to warm" is how a
              # warm-up silently no-ops on the run it was needed for.
              # The bound is the loop condition, not a branch inside the loop. A shape
              # that loops until the fetch succeeds and exits from an "over budget"
              # arm inside the body puts termination in something removable, and
              # removing it does not fail anything: it hangs, which is worse than
              # either outcome and takes whatever is watching down with it.
              #
              # No backticks anywhere in this heredoc. It is unquoted, so a backtick
              # in a COMMENT is still command substitution at render time, and the
              # first version of this note contained a shell snippet that expanded
              # into an endless loop -- the builder hung, and every structural test
              # passed because the YAML it never finished printing was still valid.
              # A real clock, not a per-attempt constant. How long an attempt
              # costs is not knowable here: Cilium answers a Service with no
              # ready endpoints by rejecting (serviceNoBackendResponse defaults
              # to reject), so the connect fails instantly and the -T budget is
              # never spent, while a mirror that is up but slow spends all of
              # it. A constant is wrong in both regimes, and it was wrong in
              # both directions across two attempts at guessing it.
              ready=0
              start=\$(date +%s)
              while [ \$(( \$(date +%s) - start )) -lt ${GHCR_WARM_READY_TIMEOUT} ]; do
                if get -T 10 -O /dev/null "\$root/v2/"; then
                  ready=1
                  break
                fi
                sleep 5
              done
              if [ "\$ready" -ne 1 ]; then
                echo "warm: mirror did not start serving within ${GHCR_WARM_READY_TIMEOUT}s"
                exit 1
              fi
              # Pick the one platform the workers run. The index lists digest and
              # platform inside the SAME record, so normalising whitespace and
              # splitting on record boundaries keeps this a grep -- no JSON parser,
              # which the mirror's image does not carry.
              #
              # Walking both platforms instead is what the earlier version did, and
              # it was wrong twice over: arm64 is bytes nobody in the sandbox ever
              # pulls, and the digest sort orders manifests by hex, which put arm64 first
              # for both warmed tags. On the 40 KB/s floor this feature exists for,
              # that spent the first half of the Job's deadline on an architecture
              # the workers do not use and left the second tag cold.
              select_amd64() {
                tr -d ' \t\n' |
                  sed 's/},{/}\n{/g' |
                  grep -E '"architecture":"amd64"' |
                  grep -o 'sha256:[0-9a-f]\{64\}' |
                  head -1
              }
              failed=0
              got=0
              warmed=""
              missed=""
              for tag in ${tags}; do
                idx=\$(get -T 120 -O - --header "\$acc" "\$base/manifests/\$tag") || {
                  echo "warm: index for \$tag unavailable"
                  failed=\$((failed + 1))
                  missed="\$missed \$tag"
                  continue
                }
                before=\$got
                tag_failed=0
                # A single-arch tag has no platform record: the response IS the
                # manifest, so walk it directly rather than looking for an index
                # entry that does not exist.
                sel=\$(printf '%s' "\$idx" | select_amd64)
                if [ -n "\$sel" ]; then
                  bodies=\$(get -T 120 -O - --header "\$acc" "\$base/manifests/\$sel") || bodies=""
                else
                  bodies="\$idx"
                fi
                if [ -n "\$bodies" ]; then
                  for blob in \$(printf '%s' "\$bodies" | digests); do
                    # "fetched", not "cached": a successful GET says the mirror
                    # answered, which is equally true of a cache hit and of a live
                    # upstream fetch. The blobstore write is distribution's business
                    # and this Job cannot observe it.
                    if get -T 600 -O /dev/null "\$base/blobs/\$blob"; then
                      got=\$((got + 1))
                      echo "warm: \$tag \$blob fetched"
                    else
                      failed=\$((failed + 1))
                      tag_failed=\$((tag_failed + 1))
                      echo "warm: \$tag \$blob failed"
                    fi
                  done
                fi
                # "warmed" has to mean every blob of the tag landed. A live run
                # showed why: three of four blobs failed on an upstream reset and the
                # tag still reported warmed, because the counter asked whether ANY
                # blob arrived. A partially cached tag still makes the worker fetch
                # the rest across the slow leg, which is the thing being measured.
                if [ "\$got" -gt "\$before" ] && [ "\$tag_failed" -eq 0 ]; then
                  warmed="\$warmed \$tag"
                else
                  missed="\$missed \$tag"
                fi
              done
              # Per-tag, not just a total: a Job that hits its deadline halfway warms
              # one suite and not the other, and a bare count cannot say which.
              echo "warm: warmed:\$warmed"
              echo "warm: not warmed:\$missed"
              echo "warm: fetched \$got, failed \$failed"
              # One question, about the subject: is every selected tag fully warm.
              # The counters are proxies for it and both turned out to be strictly
              # weaker -- a failed blob or a failed index already puts its tag in the
              # missed list, and a zero fetch count with nothing missed would need zero
              # tags, which the builder refuses. Keeping them as extra conjuncts made
              # their own mutations survive: anything they would have caught, this
              # catches first. A proxy also misses whatever path forgets to update it,
              # and that is how a cold tag rode out on a Complete Job.
              [ -z "\$missed" ]
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            runAsNonRoot: true
            capabilities:
              drop:
                - ALL
          resources:
            requests:
              cpu: 10m
              memory: 32Mi
            limits:
              cpu: 500m
              memory: 128Mi
              ephemeral-storage: 64Mi
EOF
}

# warm_ghcr_mirror: start the warm-up Job. Best-effort and never fails the caller:
# it runs during install, and the run has to survive without it exactly as it did
# before the warm-up existed.
warm_ghcr_mirror() {
  local out image tags manifest
  if ! out=$(kubectl -n kube-system get deploy ghcr-mirror 2>&1); then
    # Only the API's own NotFound means the mirror was never applied here. A missing
    # kubectl, a bad context and an apiserver blip all fail this read too, and the
    # captured message is the only place their cause exists -- the same distinction
    # resolve_ghcr_mirror_endpoint documents below.
    case "$out" in
      *NotFound*)
        echo "ghcr-mirror not deployed -- nothing to warm" >&2 ;;
      *)
        echo "WARNING: could not query the ghcr-mirror Deployment; skipping the warm-up. Reason: ${out:-none reported}" >&2 ;;
    esac
    return 0
  fi
  # Warm only the minors a suite can actually select. The suites resolve `.[-1]` and
  # `.[-2]` of the sorted keys, ghcr_warm_tags emits that same order, so a prefix of
  # it is exactly their set without restating their expression here.
  #
  # The whole map would be ~300 MiB at the walk this file ships: five tags at ~61 MiB
  # each for the single platform the Job fetches. (634 MiB was the measured figure
  # while the walk still covered both platforms of every tag; quoting it against
  # one-platform code was the same accounting slip the paragraph above pins on the
  # platform walk itself.) It crosses the one leg
  # this feature exists because of, the one the access log measures at 40-165 KB/s and
  # which answers 500 mid-stream. Warming
  # tags nothing selects spends that leg on nothing while the install and the
  # pre-pull step are still using it.
  # No 2>/dev/null. Command substitution captures stdout only, so ghcr_warm_tags'
  # complaint about a missing yq cannot reach $tags in the first place -- redirecting
  # it merely deleted the one line that names the cause, leaving the caller to blame
  # versions.yaml for a tool that is not installed.
  tags=$(ghcr_warm_tags | head -n "$GHCR_WARM_TAG_COUNT") || tags=""
  if [ -z "$tags" ]; then
    echo "WARNING: could not read kubelet tags from ${GHCR_WARM_VERSIONS_FILE}; skipping the warm-up" >&2
    return 0
  fi
  # Reuse the mirror's own image so there is one place to bump. It is NOT already
  # cached on the nodes: hack/e2e-ghcr-mirror.yaml keeps it out of the pre-pull set
  # on purpose, and the pre-pull step runs after this one anyway. The Job's own wait
  # for the mirror to serve is what absorbs that.
  if ! image=$(kubectl -n kube-system get deploy ghcr-mirror \
    -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null); then
    image=""
  fi
  if [ -z "$image" ]; then
    echo "WARNING: could not read the ghcr-mirror image; skipping the warm-up" >&2
    return 0
  fi
  # The image is the only builder input that arrives from the cluster, and it is
  # interpolated into the Job YAML. Writing it needs Deployment write access, so this
  # is not a trust boundary. The point is narrower: the endpoint and the repository
  # are checked and the tags are pattern-matched, so leaving the one input that comes
  # from outside unchecked reads as an oversight. Other locally-set values
  # (GHCR_WARM_JOB_NAME, GHCR_WARM_READY_TIMEOUT) also reach the script unquoted and
  # are equally unchecked -- they are set in this file and nowhere else.
  # Reference characters only.
  case "$image" in
    *[!A-Za-z0-9./:@_-]*)
      echo "WARNING: the ghcr-mirror image reads as [$image], which is not an image reference; skipping the warm-up" >&2
      return 0 ;;
  esac
  # Capture the manifest with stderr NOT folded in. `2>&1` here would put anything
  # written to stderr inside the YAML, and under a runner that traces the body (the
  # bats runner does) that is the trace itself: the Job then fails to parse for a
  # reason that has nothing to do with the Job. The builder communicates through its
  # exit status, so there is nothing on stderr worth keeping.
  #
  # shellcheck disable=SC2086 # tags is a builder-validated whitespace-separated list
  if ! manifest=$(ghcr_warm_job_manifest "$image" $tags 2>/dev/null); then
    echo "WARNING: refused to build the warm-up Job (a tag is not a plain version); skipping" >&2
    return 0
  fi
  if ! out=$(printf '%s\n' "$manifest" | kubectl apply -f - 2>&1); then
    echo "WARNING: could not start the ghcr-mirror warm-up; tenant workers may pay a cold fetch. Reason: ${out:-none reported}" >&2
    return 0
  fi
  echo "ghcr-mirror warm-up started for: $(printf '%s' "$tags" | tr '\n' ' ')" >&2
  return 0
}

# ghcr_registry_mirrors_block: pure string builder. Given a mirror endpoint URL
# ($1, may be empty), print the `registryMirrors:` sub-block for a tenant Kubernetes
# CR `spec.talos` (4-space indented, i.e. nested under `  talos:`), or nothing when
# the endpoint is empty. Kept separate so it can be unit-tested: an indentation or
# quoting slip here would emit an invalid CR and every kubernetes-* test would fail.
ghcr_registry_mirrors_block() {
  local endpoint="$1"
  [ -n "$endpoint" ] || return 0
  printf '    registryMirrors:\n      ghcr.io:\n        endpoints:\n          - %s\n' "$endpoint"
}

# _apply_ghcr_mirror_egress_policy: install the CiliumClusterwideNetworkPolicy that
# lets tenant worker VM (virt-launcher) Pods egress to the mirror. It ships in the
# manifest but is applied here, after Cilium's CRDs exist. Idempotent, best-effort.
#
# On failure it PRINTS the reason to stdout for the caller to surface. The causes
# are not all alike -- a missing yq, an apiVersion the cluster does not serve, an
# RBAC denial are permanent, while an apiserver blip is not -- and the exit code
# cannot tell them apart. Discarding the message would leave the caller announcing
# a cause it does not know, in the subsystem this exists to make legible.
_apply_ghcr_mirror_egress_policy() {
  if ! command -v yq >/dev/null 2>&1; then
    echo "yq is not installed, so the policy could not be extracted from ${GHCR_MIRROR_MANIFEST}"
    return 1
  fi
  # Braces, not a bare `2>&1 >/dev/null`: the goal is to keep stderr (the reason)
  # on stdout for the caller and drop kubectl's success chatter, and the braced
  # form says that unambiguously.
  { yq 'select(.kind == "CiliumClusterwideNetworkPolicy")' "$GHCR_MIRROR_MANIFEST" \
    | kubectl apply -f - >/dev/null; } 2>&1
}

# resolve_ghcr_mirror_endpoint: print the mirror endpoint URL to use, or an empty
# string to signal "pull ghcr.io directly". Resolved once and cached in a /tmp file
# that persists across bats files (same sandbox container), so only the first tenant
# test pays the readiness wait.
resolve_ghcr_mirror_endpoint() {
  if [ -f "$_GHCR_MIRROR_DECISION_FILE" ]; then
    cat "$_GHCR_MIRROR_DECISION_FILE"
    return 0
  fi
  local endpoint="" cache=1 out rc avail ready apply_err
  # One `get deploy` serves both the existence check and the not-deployed-vs-error
  # split: a genuine NotFound is a stable decision worth caching, but any other
  # failure (transient apiserver blip) must NOT be cached, or one blip would
  # disable the mirror for every later test in the shared sandbox.
  #
  # Capture inside `if` (not a bare `out=$(...); rc=$?`): the chainsaw suites run
  # this script under `sh -c` with `set -eu`, and dash inherits errexit into a
  # command substitution, so a non-zero kubectl in a bare assignment would kill
  # the script before either fallback branch runs. bash does not inherit it,
  # which is why a bare assignment survives locally but not in CI.
  if out=$(kubectl -n kube-system get deploy ghcr-mirror 2>&1); then
    rc=0
  else
    rc=$?
  fi
  if [ "$rc" -ne 0 ]; then
    case "$out" in
      # The API's own NotFound only. A bare "not found" also matches a shell
      # reporting a missing kubectl and a bad --context, neither of which says
      # anything about whether the mirror was applied -- and both would then be
      # cached as the stable answer for every later suite in the sandbox.
      *NotFound*)
        echo "ghcr-mirror not deployed -- tenant workers pull ghcr.io directly" >&2 ;;
      *)
        echo "WARNING: could not query ghcr-mirror Deployment (transient); not caching, will retry" >&2
        cache=0 ;;
    esac
  else
    # Readiness is decided once, then acted on once. The watch is the cheap way to
    # learn it, but `rollout status` also exits non-zero on a broken or aborted
    # watch, and its output goes to /dev/null -- so when it fails, ask the API what
    # the Deployment's own condition says instead of reading the watch's exit code
    # as the answer.
    #
    # 2m, not longer: the wait is serial with the rest of the 67m e2e step, whose
    # budget inventory has to stay above the sum of its legs, and spending more here
    # eats the margin the failure-path diagnostics need in order to be collected at
    # all. It is already generous -- the Deployment pulls one small digest-pinned
    # image and was applied at install time, tens of minutes earlier.
    if kubectl -n kube-system rollout status deploy/ghcr-mirror --timeout=2m >/dev/null 2>&1; then
      ready=yes
    else
      if avail=$(kubectl -n kube-system get deploy ghcr-mirror \
        -o jsonpath='{.status.conditions[?(@.type=="Available")].status}' 2>/dev/null); then
        :
      else
        avail=""
      fi
      case "$avail" in
        True)  ready=yes ;;
        False) ready=no ;;
        *)     ready=unknown ;;
      esac
    fi
    case "$ready" in
      yes)
        # Only commit the endpoint once the tenant egress allow is actually in
        # place. The mirror is a kube-system ClusterIP that the tenant default-deny
        # egress drops without this allow; pointing workers at it when the policy
        # did not apply just adds dial-timeout latency before Talos falls back.
        # Leaving the endpoint empty is what keeps that cost off the run -- it
        # removes the one failure mode this function can see, not every one of them
        # (see the header on what the gate does and does not establish).
        if apply_err=$(_apply_ghcr_mirror_egress_policy); then
          endpoint="$GHCR_MIRROR_SVC_URL"
          echo "ghcr-mirror ready and egress allow applied -- tenant workers mirror ghcr.io via ${endpoint}" >&2
        else
          # Not cached, for the same reason as the failed query above: a failed
          # apply says nothing about whether the next attempt would succeed. The
          # reason is printed rather than characterised -- some causes here are
          # permanent and retrying will not help, and only the message says which.
          echo "WARNING: ghcr-mirror ready but its egress allow failed to apply; not caching, will retry. Reason: ${apply_err:-none reported}" >&2
          cache=0
        fi
        ;;
      no)
        # Cached, and this is a trade rather than an invariant: a Deployment can
        # still go Available later on its own -- a kubelet image-pull retry or a
        # Pending Pod finally scheduling would both do it, with nothing re-applying
        # the manifest. What caching buys is not re-paying the wait in every later
        # suite, serial with the rest of the step; what it costs is missing a mirror
        # that recovers mid-run. For a best-effort accelerator that trade is worth
        # taking, and re-reading the condition cheaply on a cached miss would take
        # it back if it ever bites.
        echo "WARNING: ghcr-mirror not Available -- tenant workers pull ghcr.io directly" >&2
        ;;
      *)
        echo "WARNING: could not confirm ghcr-mirror readiness (transient); not caching, will retry" >&2
        cache=0
        ;;
    esac
  fi
  # Cache atomically (temp + mv) so a concurrent reader in the shared sandbox never
  # sees a half-written decision file.
  if [ "$cache" -eq 1 ]; then
    printf '%s' "$endpoint" > "${_GHCR_MIRROR_DECISION_FILE}.tmp" \
      && mv "${_GHCR_MIRROR_DECISION_FILE}.tmp" "$_GHCR_MIRROR_DECISION_FILE"
  fi
  printf '%s' "$endpoint"
}

# _ghcr_mirror_bounded_read <label> <command...>: run one diagnostic read under a
# wall-clock bound and name the outcome when it does not finish, so a read cut off
# mid-flight is not misread as the cluster being silent. Always returns 0: the only
# caller sits before the node-join block's `exit 1`, and a non-zero here would
# replace that exit under `set -e`.
#
# Bounded by the caller's own COZY_DIAG_READ_TIMEOUT/COZY_DIAG_READ_GRACE, the same
# knobs cozy_diag_read uses, so lowering the node-join block's budget lowers these
# too -- a private knob here would sit at its own default while the caller's budget
# dropped. The block validates those values before this runs; standalone use
# (hack/ghcr-mirror_test.bats exercises this without run-kubernetes.sh) falls back
# to the block's own 20s/5s defaults. The bound lives here rather than in a call to
# cozy_diag_read because that function is defined in run-kubernetes.sh and this file
# is sourced on its own, the same reason talos-image-cache.sh carries its own copy.
_ghcr_mirror_bounded_read() {
  local label="$1"
  shift
  local to="${COZY_DIAG_READ_TIMEOUT:-20}" grace="${COZY_DIAG_READ_GRACE:-5}" rc=0
  if command -v timeout >/dev/null 2>&1; then
    timeout -k "$grace" "$to" "$@" || rc=$?
  else
    "$@" || rc=$?
  fi
  case "$rc" in
    0) ;;
    124|137)
      echo "=== ${label}: read did not finish within ${to}s and was cut off; what it had not printed is absent from this log, not from the cluster ===" >&2 ;;
    *)
      echo "=== ${label}: read failed (exit ${rc}); what it would have shown was not observed ===" >&2 ;;
  esac
  return 0
}

# ghcr_mirror_diagnose: on-demand diagnostic for the node-join failure path.
#
# Pointing workers at the mirror and their nodes joining does not establish that
# the mirror served anything -- Talos falls back to public ghcr.io on its own, so
# a pass is consistent with the mirror never being touched, and a failure is
# consistent with it being unreachable. registry:2's access log is what tells the
# two apart: a `GET /v2/siderolabs/kubelet/...` line means a worker really came
# through here. Without it #3513 gains another run that has to be reasoned about
# from the outside, which is how it survived four attempts.
#
# All output to stderr; never fails the caller. Every read is wall-clock bounded
# by the caller's COZY_DIAG_READ_TIMEOUT/COZY_DIAG_READ_GRACE.
ghcr_mirror_diagnose() {
  local hits log gate_rc=0
  local to="${COZY_DIAG_READ_TIMEOUT:-20}" grace="${COZY_DIAG_READ_GRACE:-5}"
  # The existence gate is bounded too: a wedged apiserver hangs `get deploy` just
  # as it hangs the reads below.
  if command -v timeout >/dev/null 2>&1; then
    timeout -k "$grace" "$to" \
      kubectl -n kube-system get deploy ghcr-mirror >/dev/null 2>&1 || gate_rc=$?
  else
    kubectl -n kube-system get deploy ghcr-mirror >/dev/null 2>&1 || gate_rc=$?
  fi
  if [ "$gate_rc" -ne 0 ]; then
    echo "ghcr-mirror not deployed -- tenant workers pulled ghcr.io directly; nothing to diagnose" >&2
    return 0
  fi
  {
    echo "--- ghcr-mirror deploy/pod/svc/endpointslice ---"
    _ghcr_mirror_bounded_read 'ghcr-mirror deploy/pod/svc/endpointslice' \
      kubectl -n kube-system get deploy,pod,svc,endpointslice \
      -l app.kubernetes.io/name=ghcr-mirror -o wide
    echo "--- worker egress allow policy ---"
    _ghcr_mirror_bounded_read 'worker egress allow policy' \
      kubectl get ciliumclusterwidenetworkpolicy e2e-ghcr-mirror-worker-egress -o wide
    # The answer to "did a worker actually pull through the mirror".
    #
    # Filter the whole log rather than tailing it. The readinessProbe requests /v2/
    # every 5s and registry:2 writes an access line per request, so ~12 lines a
    # minute pile up after the worker's pull; by the time this runs, past a
    # 29m node-join budget, that is 200+ probe lines on top of the one request
    # worth finding, and any fixed tail window reports "no kubelet pull" for a
    # mirror that served it. --tail=-1 is explicit because kubectl defaults a label-selected
    # read to the last 10 lines, not to the whole log. It is the wall clock, not the
    # line count, that bounds this read: --tail=-1 stays, timeout caps the time.
    echo "--- did a worker pull ${GHCR_WARM_REPO} through the mirror? ---"
    # Read and filter as separate steps. Piping them would make an unreadable log
    # and a log with no kubelet request produce the same 0, and the second is a
    # finding about the workers while the first is a statement about nothing.
    local log_rc=0 durations=''
    if command -v timeout >/dev/null 2>&1; then
      log=$(timeout -k "$grace" "$to" \
        kubectl -n kube-system logs -l app.kubernetes.io/name=ghcr-mirror \
        --tail=-1 2>/dev/null) || log_rc=$?
    else
      log=$(kubectl -n kube-system logs -l app.kubernetes.io/name=ghcr-mirror \
        --tail=-1 2>/dev/null) || log_rc=$?
    fi
    if [ "$log_rc" -eq 0 ]; then
      # Exclude the warm-up's own requests. It asks for the same paths from
      # kube-system before any tenant exists, so counting them would make "workers
      # did reach it" true of every run and retire the only signal that tells a
      # served pull from a worker that fell back to public ghcr.io. The warm-up
      # names itself with -U; see ghcr_warm_job_manifest.
      #
      # Count access lines, not every line carrying the path. registry:2.8.3 writes
      # three lines per GET against the pinned image -- a logrus "response completed",
      # an upstream-challenge line, and one nginx-combined access line -- so matching
      # the path alone counted one request as three. The combined line is the only
      # shape that appears exactly once per request, which makes this a request count
      # rather than a line count, and it is also the shape whose user-agent field is
      # positional and always present.
      #
      # GET and HEAD both count. containerd resolves a manifest with a HEAD before it
      # GETs anything, and the combined access line records HEAD in the same shape
      # (measured on the pinned image with a containerd user-agent), so a filter
      # anchored on GET alone drops the first request of every pull -- and on a log
      # holding only that HEAD it declares the worker absent while the duration
      # section below prints that same request's timing. The warm-up issues only GETs,
      # so counting HEAD reintroduces none of its traffic.
      hits=$(printf '%s\n' "$log" \
        | grep -F -e "\"GET /v2/${GHCR_WARM_REPO}" -e "\"HEAD /v2/${GHCR_WARM_REPO}" \
        | grep -vcF "$GHCR_WARM_JOB_NAME") || hits=0
      if [ "${hits:-0}" -gt 0 ]; then
        echo "a worker pulled ${GHCR_WARM_REPO} through the mirror (${hits} request(s), excluding the warm-up's own)"
      else
        echo "no ${GHCR_WARM_REPO} request appears in the mirror's access log; whether workers fell back to public ghcr.io or never got as far as the pull, this log cannot tell apart"
      fi
      # How long the mirror took to answer the worker. This is the only signal that
      # separates a warm cache from a cold one after the fact: distribution serves a
      # cached blob from disk in milliseconds and streams a missed one from ghcr.io for
      # minutes, and nothing else in the artifacts distinguishes them. A warm-up that
      # reports success is not proof the blob reached the blobstore -- the proxy stores
      # in the background, so a completed GET and a stored blob are different events.
      # The warm-up's own requests are excluded for the same reason as above.
      #
      # Filter here rather than handing the log to `sh -c` as an argument. Linux caps
      # a single argv element at MAX_ARG_STRLEN, 131072 bytes, and that cap is
      # independent of ARG_MAX and of how much room the rest of the command line
      # leaves: measured on 6.8, an argument of 131000 bytes execve()s and one of
      # 131072 fails with E2BIG. The readiness probe writes an access line every 5s,
      # so this log passes that after roughly twenty minutes of mirror uptime -- which
      # is always true here, since the mirror is created during install and this runs
      # at the 29m node-join deadline, in a suite that starts long after it. As an
      # argument the read would have failed on every real run and succeeded only
      # against a fixture small enough to fit, which is the one case the tests
      # exercise.
      echo "--- how long did the mirror take to answer the worker? ---"
      durations=$(printf '%s\n' "$log" | grep -F "/v2/${GHCR_WARM_REPO}" \
        | grep -F 'http.response.duration' \
        | grep -vF "$GHCR_WARM_JOB_NAME" \
        | grep -oE 'http\.response\.duration=[^ ]+' \
        | tail -5) || durations=''
      if [ -n "$durations" ]; then
        printf '%s\n' "$durations"
      else
        echo "no timed request for ${GHCR_WARM_REPO} outside the warm-up's own; the durations that separate a warm cache from a cold one were not observed"
      fi
    else
      echo "could not read the mirror's log; this says nothing either way about whether workers used it"
    fi

    # Whether the warm-up itself got anywhere. Without this the exclusion above makes
    # the run unfalsifiable: a red suite looks identical whether the cache was warm
    # and the guest still starved, or the warm-up never came up at all.
    echo "--- did the warm-up populate the cache? ---"
    _ghcr_mirror_bounded_read 'warm-up Job status' \
      kubectl -n kube-system get job "$GHCR_WARM_JOB_NAME" -o wide
    _ghcr_mirror_bounded_read 'warm-up Job result' \
      kubectl -n kube-system logs "job/${GHCR_WARM_JOB_NAME}" --tail=5
    echo "--- registry access log (last 50 lines, for context) ---"
    _ghcr_mirror_bounded_read 'registry access log (last 50 lines)' \
      kubectl -n kube-system logs -l app.kubernetes.io/name=ghcr-mirror \
      --tail=50 --prefix
  } >&2
  return 0
}
