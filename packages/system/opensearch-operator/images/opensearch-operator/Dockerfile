# Build the manager binary from the upstream sources with the Cozystack patch
# applied. The patch keeps a single-replica cluster electable when the operator
# retires its bootstrap pod; see patches/bootstrap-voting-exclusion.diff and
# https://github.com/opensearch-project/opensearch-k8s-operator/issues/1448.
FROM golang:1.26@sha256:3c3e25a4da13fd0478eed2df1eb35a0e667094a7124d3993a6a1d30f71c17e79 AS builder

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

FROM gcr.io/distroless/static:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
WORKDIR /
COPY --from=builder /workspace/opensearch-operator/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
