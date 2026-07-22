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
FROM golang:1.26@sha256:3aff6657219a4d9c14e27fb1d8976c49c29fddb70ba835014f477e1c70636647 AS builder

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

FROM gcr.io/distroless/static:nonroot@sha256:f7f8f729987ad0fdf6b05eeeae94b26e6a0f613bdf46feea7fc40f7bd72953e6
WORKDIR /
COPY --from=builder /workspace/manager /manager
USER 65532:65532

ENTRYPOINT ["/manager"]
