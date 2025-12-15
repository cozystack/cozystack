# Build the velero binary
FROM golang:1.24-bookworm AS builder

ARG VERSION=v1.17.1
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
ARG GOPROXY=https://proxy.golang.org

ENV CGO_ENABLED=0 \
    GO111MODULE=on \
    GOPROXY=${GOPROXY} \
    GOOS=${TARGETOS} \
    GOARCH=${TARGETARCH} \
    GOARM=${TARGETVARIANT}

WORKDIR /workspace

RUN curl -sSL https://github.com/vmware-tanzu/velero/archive/refs/tags/${VERSION}.tar.gz | tar -xzvf- --strip=1

COPY patches /patches
RUN git apply /patches/*.diff || true

RUN mkdir -p /output && \
    export GOARM=$( echo "${GOARM}" | cut -c2-) && \
    GIT_SHA=$(git rev-parse HEAD 2>/dev/null || echo "unknown") && \
    go build -o /output/velero \
    -ldflags "-X github.com/vmware-tanzu/velero/pkg/buildinfo.Version=${VERSION}" \
    ./cmd/velero && \
    go build -o /output/velero-restore-helper \
    -ldflags "-X github.com/vmware-tanzu/velero/pkg/buildinfo.Version=${VERSION}" \
    ./cmd/velero-restore-helper && \
    go build -o /output/velero-helper \
    -ldflags "-X github.com/vmware-tanzu/velero/pkg/buildinfo.Version=${VERSION}" \
    ./cmd/velero-helper && \
    go clean -modcache -cache

# Velero image packing section
# Use paketobuildpacks/run-jammy-tiny which includes shell for update-crds job
FROM paketobuildpacks/run-jammy-tiny:latest

COPY --from=builder /output/velero /velero
COPY --from=builder /output/velero-restore-helper /velero-restore-helper
COPY --from=builder /output/velero-helper /velero-helper

USER cnb:cnb

ENTRYPOINT ["/velero"]

