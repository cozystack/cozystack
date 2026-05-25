FROM golang:1.26.3-alpine3.23 AS builder

ARG VERSION=v1.4.0

ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

RUN apk --no-cache add bash curl tar

RUN curl -sSL https://github.com/cozystack/redis-operator/archive/refs/tags/${VERSION}.tar.gz | tar -xzvf- --strip=1

RUN GOOS=$TARGETOS GOARCH=$TARGETARCH VERSION=$VERSION ./scripts/build.sh

FROM alpine:3.23
RUN apk --no-cache add \
  ca-certificates
COPY --from=builder /workspace/bin/redis-operator /usr/local/bin
RUN addgroup -g 1000 rf && \
  adduser -D -u 1000 -G rf rf && \
  chown rf:rf /usr/local/bin/redis-operator
USER rf

ENTRYPOINT ["/usr/local/bin/redis-operator"]
