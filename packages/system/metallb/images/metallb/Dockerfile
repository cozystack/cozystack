# syntax=docker/dockerfile:1.2

FROM --platform=$BUILDPLATFORM docker.io/golang:1.22.7 AS builder

ARG VERSION
ARG GIT_COMMIT=dev
ARG GIT_BRANCH=dev
ARG TARGETARCH
ARG TARGETOS
ARG TARGETPLATFORM

WORKDIR /go/go.universe.tf/metallb

RUN --mount=type=cache,target=/go/pkg/mod \
    curl -sSL https://github.com/metallb/metallb/archive/refs/tags/${VERSION}.tar.gz \
    | tar -xzvf- --strip=1 

RUN curl -sSLO https://github.com/metallb/metallb/pull/2726.diff && \
    git apply 2726.diff

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download -x

RUN case ${TARGETPLATFORM} in \
    "linux/arm/v6") export VARIANT="6" ;; \
    "linux/arm/v7") export VARIANT="7" ;; \
    *) export VARIANT="" ;; \
    esac && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=$VARIANT \
    go build -v -o /build/controller \
    -ldflags "-X 'go.universe.tf/metallb/internal/version.gitCommit=${GIT_COMMIT}' \
      -X 'go.universe.tf/metallb/internal/version.gitBranch=${GIT_BRANCH}'" \
    ./controller \
    && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=$VARIANT \
    go build -v -o /build/frr-metrics \
    -ldflags "-X 'go.universe.tf/metallb/internal/version.gitCommit=${GIT_COMMIT}' \
      -X 'go.universe.tf/metallb/internal/version.gitBranch=${GIT_BRANCH}'" \
    frr-tools/metrics/exporter.go \
    && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=$VARIANT \
    go build -v -o /build/cp-tool \
    -ldflags "-X 'go.universe.tf/metallb/internal/version.gitCommit=${GIT_COMMIT}' \
      -X 'go.universe.tf/metallb/internal/version.gitBranch=${GIT_BRANCH}'" \
    frr-tools/cp-tool/cp-tool.go \
    && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=$VARIANT \
    go build -v -o /build/speaker \
    -ldflags "-X 'go.universe.tf/metallb/internal/version.gitCommit=${GIT_COMMIT}' \
      -X 'go.universe.tf/metallb/internal/version.gitBranch=${GIT_BRANCH}'" \
    ./speaker

FROM gcr.io/distroless/static:latest as controller

COPY --from=builder /build/controller /controller

LABEL org.opencontainers.image.authors="metallb" \
    org.opencontainers.image.url="https://github.com/metallb/metallb" \
    org.opencontainers.image.documentation="https://metallb.universe.tf" \
    org.opencontainers.image.source="https://github.com/cozystack/cozystack" \
    org.opencontainers.image.vendor="metallb" \
    org.opencontainers.image.licenses="Apache-2.0" \
    org.opencontainers.image.description="Metallb Controller" \
    org.opencontainers.image.title="controller" \
    org.opencontainers.image.base.name="gcr.io/distroless/static:latest"

ENTRYPOINT ["/controller"]

FROM gcr.io/distroless/static:latest as speaker

COPY --from=builder /build/cp-tool /cp-tool
COPY --from=builder /build/speaker /speaker
COPY --from=builder /build/frr-metrics /frr-metrics
COPY --from=builder /go/go.universe.tf/metallb/frr-tools/reloader/frr-reloader.sh /frr-reloader.sh

LABEL org.opencontainers.image.authors="metallb" \
  org.opencontainers.image.url="https://github.com/metallb/metallb" \
  org.opencontainers.image.documentation="https://metallb.universe.tf" \
  org.opencontainers.image.source="https://github.com/cozystack/cozystack" \
  org.opencontainers.image.vendor="metallb" \
  org.opencontainers.image.licenses="Apache-2.0" \
  org.opencontainers.image.description="Metallb speaker" \
  org.opencontainers.image.title="speaker" \
  org.opencontainers.image.base.name="gcr.io/distroless/static:latest"

ENTRYPOINT ["/speaker"]
