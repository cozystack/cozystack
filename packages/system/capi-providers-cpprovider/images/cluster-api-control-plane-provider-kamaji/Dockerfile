# Builds the clastix/cluster-api-control-plane-provider-kamaji manager
# binary from a tagged upstream release with a small backport patch.
#
# We pin to v0.19.0 because everything after it (upstream master, the
# unreleased v0.20.0) renames api/v1alpha1 -> api/v1alpha2 wholesale and
# ships a CRD that serves only v1alpha2 with no conversion webhook -
# upgrading would orphan every existing KamajiControlPlane in the wild
# until a manual rewrite. patches/0001-backport-additional-service-ports-pr347.diff
# brings PR #347 (AdditionalServicePorts) back to the v1alpha1 schema,
# which our chart relies on for the talos-trustd sidecar port. Once
# upstream ships a real migration path, drop both this Dockerfile and the
# patch and consume vanilla upstream images directly.
FROM golang:1.26@sha256:3c3e25a4da13fd0478eed2df1eb35a0e667094a7124d3993a6a1d30f71c17e79 AS builder

# Upstream release tag. Bump together with patches/ when refreshing.
ARG VERSION=v0.19.0
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

RUN curl -sSL "https://github.com/clastix/cluster-api-control-plane-provider-kamaji/archive/refs/tags/${VERSION}.tar.gz" \
    | tar -xzf - --strip=1

COPY patches /patches
RUN git apply --recount /patches/*.diff

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -a -o manager main.go

FROM gcr.io/distroless/static:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
WORKDIR /
COPY --from=builder /workspace/manager /manager
USER 65532:65532

ENTRYPOINT ["/manager"]
