# Build the manager binary from the upstream sources with the Cozystack patch
# applied. The patch keeps a single-replica cluster electable when the operator
# retires its bootstrap pod; see patches/bootstrap-voting-exclusion.diff and
# https://github.com/opensearch-project/opensearch-k8s-operator/issues/1448.
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

RUN GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 go build -a -o manager main.go

FROM gcr.io/distroless/static:nonroot@sha256:d29e660cc75a5b6b1334e03c5c81ccf9bc0884a002c6000dbf0fb96034814478
WORKDIR /
COPY --from=builder /workspace/opensearch-operator/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
