# Build the manager binary from the upstream sources with the Cozystack patches
# applied.
#
# patches/bootstrap-voting-exclusion.diff keeps a single-replica cluster
# electable when the operator retires its bootstrap pod; see
# https://github.com/opensearch-project/opensearch-k8s-operator/issues/1448.
#
# patches/restart-on-permanently-yellow.diff carries
# https://github.com/opensearch-project/opensearch-k8s-operator/pull/1569 back to
# this version: a cluster whose replicas can never be assigned stays yellow for
# good, and the restart gate held every rolling restart and upgrade on it. The
# part of that change that counts tiered data roles (data_hot and the like) as
# data nodes is left out, because packages/apps/opensearch renders the plain
# data role only; carry it as well if that ever changes. Drop the patch on the
# first operator release that includes the change.
#
# patches/skip-unusable-drain-exclusion.diff is the other half of that change,
# for a release with three or more nodes, where packages/apps/opensearch turns
# drainDataNodes on. The gate above now lets a permanently yellow cluster reach
# PreparePodForDelete, which excludes the candidate from allocation before it can
# know whether the drain will finish. On the cluster that made the gate wait, it
# never does, and the exclusion is only withdrawn after a pod is really deleted,
# so a data node stays out of allocation for as long as the restart is stuck. The
# patch asks the allocator first, and only while nothing is moving. Tracked
# upstream as opensearch-project/opensearch-k8s-operator#1585, fixed by #1586;
# drop it on the first release carrying that.
#
# patches/timeout-cluster-api-calls.diff bounds the calls the operator makes to
# a cluster's HTTP API. Upstream builds its own http.Transport, which carries no
# dial, handshake or response-header timeout; it passes context.Background() to
# every call it makes on its own behalf; the calls that take a context from the
# caller are handed one with no deadline on it; and no http.Client is
# configured, so there is no timeout there either. A cluster that accepts a
# connection and then says nothing therefore holds the call for as long as the
# process lives, and with one worker per controller that one call stops every
# OpenSearchCluster from being reconciled. The deletion that then waits out its
# whole termination timeout is the visible end of it. The patch bounds a request
# on the RoundTripper, the one level that also reaches the product check
# opensearch-go runs for itself before a client's first request, and bounds the
# call again where the call site can be reached, because the library retries a
# request up to three more times on 502, 503 and 504 and the transport's
# deadline starts over on each. Tracked as cozystack/cozystack#4337;
# it belongs upstream, there is no upstream issue for it yet, and the patch goes
# away with the first release that carries a fix.
#
# patches/trace-stalled-component-reconcilers.diff names the component a
# reconcile is stuck in. The cluster controller runs nine component reconcilers
# in sequence and none of them logs unconditionally when it is entered, so a
# call that never returns leaves the operator log ending on whatever ran before
# it, and an object that is silently no longer reconciled. Twice in e2e the
# operator went quiet at the moment the chart rewrote the CR and reconciled
# nothing for the rest of the test, with no line saying where it was:
# https://github.com/cozystack/cozystack/issues/4332. The watchdog writes
# nothing while a component behaves, so it costs nothing on a healthy cluster.
# It reports rather than repairs: drop it once the stall itself is understood
# and fixed.
#
# The patches are applied in the order the glob expands, which is alphabetical.
# skip-unusable-drain-exclusion builds on restart-on-permanently-yellow, and
# timeout-cluster-api-calls rewrites os_client.go call sites that
# skip-unusable-drain-exclusion both adds and quotes as context, so it has to
# come after it. A rename that reorders them fails the build rather than
# producing a wrong binary.
FROM golang:1.26@sha256:079e59808d2d252516e27e3f3a9c003740dee7f75e55aa71528766d52bcfc16a AS builder

# Keep in step with the chart version pulled by `make update`: the chart's
# appVersion is what picks the operator image upstream, so a mismatch here would
# run one version's binary against another's CRDs.
ARG VERSION=v2.8.0

ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

RUN curl -sSL https://github.com/opensearch-project/opensearch-k8s-operator/archive/refs/tags/${VERSION}.tar.gz | tar -xzvf- --strip=1

COPY patches /patches
RUN git apply /patches/*.diff

WORKDIR /workspace/opensearch-operator

# The patches carry their own tests and nothing else in this repository runs
# them: the build below compiles main.go, so a test file that had stopped
# compiling, or stopped asserting, would still produce an image. These two
# package trees are the ones the patches touch, and both run without a cluster.
RUN go test ./opensearch-gateway/... ./pkg/reconcilers/...

RUN GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 go build -a -o manager main.go

FROM gcr.io/distroless/static:nonroot@sha256:d29e660cc75a5b6b1334e03c5c81ccf9bc0884a002c6000dbf0fb96034814478
WORKDIR /
COPY --from=builder /workspace/opensearch-operator/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
