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

# go build never compiles a _test.go, so nothing else runs the tests of the
# packages the patches touch. Each package is tested from its own directory:
# that finds the module it belongs to, and an empty list runs nothing.
RUN for dir in $(git apply --numstat /patches/*.diff | cut -f3 | grep '\.go$' | xargs -n1 dirname | sort -u); do \
      (cd "$dir" && go test .) || exit 1; \
    done

WORKDIR /workspace/opensearch-operator

RUN GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 go build -a -o manager main.go

FROM gcr.io/distroless/static:nonroot@sha256:d29e660cc75a5b6b1334e03c5c81ccf9bc0884a002c6000dbf0fb96034814478
WORKDIR /
COPY --from=builder /workspace/opensearch-operator/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
