# Built from an upstream commit after v0.3.1 to pick up driver fixes no release
# carries yet. patches/ grants the dedicated Object Lock actions SeaweedFS 4.48
# checks. Switch back to the upstream image once a release carries both.
FROM --platform=$BUILDPLATFORM golang:1.27@sha256:3680233e3204827fbdc66088528ae6d4b3d034f51d03a99d454f6de034888244 AS builder
ARG GOPROXY

ARG VERSION=0df543f5c9b5cbf770f0cf19932ea186ea8bcc2a
ARG SOURCE_SHA256=8a3f26f0fe19f521614dda574846663a918cf1ad6c572f26269c451447e08601

WORKDIR /src

RUN curl -fsSL -o /tmp/source.tar.gz https://github.com/seaweedfs/seaweedfs-cosi-driver/archive/${VERSION}.tar.gz && \
    echo "${SOURCE_SHA256}  /tmp/source.tar.gz" | sha256sum -c - && \
    tar -xzf /tmp/source.tar.gz --strip-components=1 && rm /tmp/source.tar.gz

COPY patches /patches
RUN git apply /patches/*.diff

RUN for attempt in 1 2 3; do go mod download && break; [ "$attempt" = 3 ] && exit 1; sleep $((attempt * 10)); done

RUN go test ./pkg/...

# Declared after the tests: an ARG in scope is part of every later RUN's cache
# key, so declaring these earlier reruns the tests once per target platform.
ARG TARGETARCH
ARG TARGETOS

RUN GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 \
    go build -trimpath -ldflags '-s -w' -o /seaweedfs-cosi-driver ./cmd/seaweedfs-cosi-driver

RUN mkdir -p /var/lib/cosi

FROM gcr.io/distroless/static:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3

COPY --from=builder /seaweedfs-cosi-driver /usr/bin/seaweedfs-cosi-driver
COPY --from=builder --chown=65532:65532 /var/lib/cosi /var/lib/cosi

USER 65532:65532

ENTRYPOINT ["/usr/bin/seaweedfs-cosi-driver"]
