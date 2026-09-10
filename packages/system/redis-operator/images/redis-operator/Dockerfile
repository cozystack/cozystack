FROM golang:1.26@sha256:3c3e25a4da13fd0478eed2df1eb35a0e667094a7124d3993a6a1d30f71c17e79 AS builder

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

FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
RUN apk --no-cache add \
  ca-certificates
COPY --from=builder /workspace/bin/redis-operator /usr/local/bin
RUN addgroup -g 1000 rf && \
  adduser -D -u 1000 -G rf rf && \
  chown rf:rf /usr/local/bin/redis-operator
USER rf

ENTRYPOINT ["/usr/local/bin/redis-operator"]

