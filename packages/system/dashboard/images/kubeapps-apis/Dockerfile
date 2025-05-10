# Copyright 2021-2024 the Kubeapps contributors.
# SPDX-License-Identifier: Apache-2.0

# syntax = docker/dockerfile:1

FROM alpine AS source
ARG COMMIT_REF=dd02680d796c962b8dcc4e5ea70960a846c1acdc
RUN apk add --no-cache patch
WORKDIR /source
RUN wget -O- https://github.com/cozystack/kubeapps/archive/${COMMIT_REF}.tar.gz | tar xzf - --strip-components=1

FROM bitnami/golang:1.23.4 AS builder
WORKDIR /go/src/github.com/vmware-tanzu/kubeapps
COPY --from=source /source/go.mod /source/go.sum ./
ARG TARGETOS
ARG TARGETARCH
ARG VERSION="devel"

# If true, run golangci-lint to detect issues
ARG lint

# https://github.com/bufbuild/buf/releases/
ARG BUF_VERSION="1.45.0"

# https://github.com/golangci/golangci-lint/releases
ARG GOLANGCILINT_VERSION="1.61.0"

# https://github.com/grpc-ecosystem/grpc-health-probe/releases/
ARG GRPC_HEALTH_PROBE_VERSION="0.4.34"

# Install lint tools
RUN if [ ! -z ${lint:-} ]; then \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go install github.com/golangci/golangci-lint/cmd/golangci-lint@v$GOLANGCILINT_VERSION; \
    fi

RUN if [ $TARGETARCH = 'amd64' ]; then BUF_ARCH='x86_64'; elif [ $TARGETARCH = 'arm64' ]; then BUF_ARCH='aarch64'; fi && \
    if [ $TARGETOS = 'linux' ]; then BUF_PLATFORM='Linux'; fi && \
    curl -sSL "https://github.com/bufbuild/buf/releases/download/v${BUF_VERSION}/buf-${BUF_PLATFORM}-${BUF_ARCH}" -o "/tmp/buf" && chmod +x "/tmp/buf"

# TODO: Remove and instead use built-in gRPC container probes once we're supporting >= 1.24 only. https://kubernetes.io/blog/2022/05/13/grpc-probes-now-in-beta/
RUN curl -sSL "https://github.com/grpc-ecosystem/grpc-health-probe/releases/download/v${GRPC_HEALTH_PROBE_VERSION}/grpc_health_probe-linux-${TARGETARCH}" -o "/bin/grpc_health_probe" && chmod +x "/bin/grpc_health_probe"

# With the trick below, Go's build cache is kept between builds.
# https://github.com/golang/go/issues/27719#issuecomment-514747274
RUN --mount=type=cache,target=/go/pkg/mod  \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=$TARGETOS GOARCH=$TARGETARCH GOPROXY="https://proxy.golang.org,direct" go mod download

# We don't copy the pkg and cmd directories until here so the above layers can
# be reused.
COPY --from=source /source/pkg pkg
COPY --from=source /source/cmd cmd

RUN if [ ! -z ${lint:-} ]; then \
    # Run golangci-lint to detect issues
    golangci-lint run --timeout=10m ./cmd/kubeapps-apis/... && \
    golangci-lint run --timeout=10m ./pkg/...; \
    fi

# Lint the proto files to detect errors at build time
RUN /tmp/buf lint ./cmd/kubeapps-apis

# Build the main grpc server
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=$TARGETOS GOARCH=$TARGETARCH GOPROXY="https://proxy.golang.org,direct" \
    go build \
    -ldflags "-X github.com/vmware-tanzu/kubeapps/cmd/kubeapps-apis/cmd.version=$VERSION" \
    ./cmd/kubeapps-apis

## Build 'fluxv2' plugin, version 'v1alpha1'
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=$TARGETOS GOARCH=$TARGETARCH GOPROXY="https://proxy.golang.org,direct" \
    go build \
    -ldflags "-X github.com/vmware-tanzu/kubeapps/cmd/kubeapps-apis/cmd.version=$VERSION" \
    -o /fluxv2-packages-v1alpha1-plugin.so -buildmode=plugin \
    ./cmd/kubeapps-apis/plugins/fluxv2/packages/v1alpha1/*.go

## Build 'helm' plugin, version 'v1alpha1'
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=$TARGETOS GOARCH=$TARGETARCH GOPROXY="https://proxy.golang.org,direct" \
    go build \
    -ldflags "-X github.com/vmware-tanzu/kubeapps/cmd/kubeapps-apis/cmd.version=$VERSION" \
    -o /helm-packages-v1alpha1-plugin.so -buildmode=plugin \
    ./cmd/kubeapps-apis/plugins/helm/packages/v1alpha1/*.go

## Build 'resources' plugin, version 'v1alpha1'
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=$TARGETOS GOARCH=$TARGETARCH GOPROXY="https://proxy.golang.org,direct" \
    go build \
    -ldflags "-X github.com/vmware-tanzu/kubeapps/cmd/kubeapps-apis/cmd.version=$VERSION" \
    -o /resources-v1alpha1-plugin.so -buildmode=plugin \
    ./cmd/kubeapps-apis/plugins/resources/v1alpha1/*.go

# Note: unlike the other docker images for go, we cannot use scratch as the plugins
# are loaded using the dynamic linker.
FROM bitnami/minideb:bookworm
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /go/src/github.com/vmware-tanzu/kubeapps/kubeapps-apis /kubeapps-apis
COPY --from=builder /fluxv2-packages-v1alpha1-plugin.so /plugins/fluxv2-packages/
COPY --from=builder /helm-packages-v1alpha1-plugin.so /plugins/helm-packages/
COPY --from=builder /resources-v1alpha1-plugin.so /plugins/resources/
COPY --from=builder /bin/grpc_health_probe /bin/

# Ensure the container user will be able to write to the k8s discovery client cache.
RUN mkdir -p /.kube/cache && chown 1001:1001 /.kube/cache

EXPOSE 50051
USER 1001
ENTRYPOINT [ "/kubeapps-apis" ]
CMD [ "--help" ]
