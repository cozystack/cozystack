FROM golang:1.26@sha256:6c2a5538f964f1c82f97ad14988bf05de100d922d159d0e398b54c7b0ca0c6c9 AS builder

ARG VERSION=v3.3.5

ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

RUN curl -sSL https://github.com/freshworks-oss/redis-operator/archive/refs/tags/${VERSION}.tar.gz | tar -xzvf- --strip=1

# The carried TLS patch is source only: its CRD half ships as the chart
# patch, so the tarball's CRD and chart copies stay at v3.3.5 and the tests
# that read them would fail instead of skipping.
RUN rm -rf manifests charts example .github

COPY patches /patches
RUN git apply /patches/*.diff

RUN GOOS=$TARGETOS GOARCH=$TARGETARCH VERSION=$VERSION ./scripts/build.sh

FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
RUN apk --no-cache add \
  ca-certificates
COPY --from=builder /workspace/bin/redis-operator /usr/local/bin
RUN addgroup -g 1000 rf && \
  adduser -D -u 1000 -G rf rf && \
  chown rf:rf /usr/local/bin/redis-operator
USER rf

ENTRYPOINT ["/usr/local/bin/redis-operator"]

