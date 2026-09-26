# Build the manager binary
FROM golang:1.27@sha256:3680233e3204827fbdc66088528ae6d4b3d034f51d03a99d454f6de034888244 AS builder

ARG VERSION=26.9.4-edge
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

RUN curl -sSL https://github.com/clastix/kamaji/archive/refs/tags/${VERSION}.tar.gz | tar -xzvf- --strip=1

COPY patches /patches
RUN git apply /patches/*.diff

RUN GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 go build \
    -ldflags "-X github.com/clastix/kamaji/internal.GitRepo=$GIT_REPO -X github.com/clastix/kamaji/internal.GitTag=$GIT_LAST_TAG -X github.com/clastix/kamaji/internal.GitCommit=$GIT_HEAD_COMMIT -X github.com/clastix/kamaji/internal.GitDirty=$GIT_MODIFIED -X github.com/clastix/kamaji/internal.BuildTime=$BUILD_DATE" \
    -a -o kamaji main.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
WORKDIR /
COPY --from=builder /workspace/kamaji .
USER 65532:65532

ENTRYPOINT ["/kamaji"]
