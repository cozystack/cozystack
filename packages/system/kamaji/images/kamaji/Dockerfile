# Build the manager binary
FROM golang:1.26@sha256:079e59808d2d252516e27e3f3a9c003740dee7f75e55aa71528766d52bcfc16a AS builder

ARG VERSION=26.3.6-edge
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

RUN curl -sSL https://github.com/clastix/kamaji/archive/refs/tags/${VERSION}.tar.gz | tar -xzvf- --strip=1

COPY patches /patches
RUN git apply /patches/*.diff

# Overlay patched Go modules. The kamaji edge tags up to 26.6.4 still pin
# vulnerable golang.org/x/net (< 0.55.0), golang.org/x/crypto (< 0.52.0) and
# go.opentelemetry.io/otel/sdk (< 1.43.0). These advisories ship inside the
# compiled binary, so force the fixed versions before building.
RUN go get golang.org/x/net@v0.55.0 golang.org/x/crypto@v0.52.0 go.opentelemetry.io/otel/sdk@v1.43.0 && \
    go mod tidy

RUN GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 go build \
    -ldflags "-X github.com/clastix/kamaji/internal.GitRepo=$GIT_REPO -X github.com/clastix/kamaji/internal.GitTag=$GIT_LAST_TAG -X github.com/clastix/kamaji/internal.GitCommit=$GIT_HEAD_COMMIT -X github.com/clastix/kamaji/internal.GitDirty=$GIT_MODIFIED -X github.com/clastix/kamaji/internal.BuildTime=$BUILD_DATE" \
    -a -o kamaji main.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot@sha256:d29e660cc75a5b6b1334e03c5c81ccf9bc0884a002c6000dbf0fb96034814478
WORKDIR /
COPY --from=builder /workspace/kamaji .
USER 65532:65532

ENTRYPOINT ["/kamaji"]
