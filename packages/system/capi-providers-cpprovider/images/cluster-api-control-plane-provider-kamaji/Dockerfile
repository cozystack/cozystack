# Builds the upstream clastix/cluster-api-control-plane-provider-kamaji
# manager binary from a pinned master commit. We track master HEAD (rather
# than a tagged release) because the AdditionalServicePorts feature
# (clastix/cluster-api-control-plane-provider-kamaji#347) that this chart
# relies on landed after v0.19.0; pin to the v0.20.0 tag hash once that
# release ships, then drop this Dockerfile in favour of consuming upstream
# OCI images directly.
FROM golang:1.26 AS builder

# Pinned to upstream master HEAD. Bump COMMIT and run `make update` in the
# capi-providers-cpprovider package to refresh files/control-plane-components.yaml
# and files/components.gz.
ARG COMMIT=e52ee00a9f5fcb516314ab04da0c668a3dfaca54
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

RUN curl -sSL "https://github.com/clastix/cluster-api-control-plane-provider-kamaji/archive/${COMMIT}.tar.gz" \
    | tar -xzf - --strip=1

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -a -o manager main.go

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/manager /manager
USER 65532:65532

ENTRYPOINT ["/manager"]
