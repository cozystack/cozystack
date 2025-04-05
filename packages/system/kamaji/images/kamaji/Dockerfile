# Build the manager binary
FROM golang:1.23 as builder

ARG VERSION=edge-25.3.2
ARG TARGETOS TARGETARCH

WORKDIR /workspace

RUN curl -sSL https://github.com/clastix/kamaji/archive/refs/tags/${VERSION}.tar.gz | tar -xzvf- --strip=1

COPY patches /patches
RUN git apply /patches/*.diff

RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build \
    -ldflags "-X github.com/clastix/kamaji/internal.GitRepo=$GIT_REPO -X github.com/clastix/kamaji/internal.GitTag=$GIT_LAST_TAG -X github.com/clastix/kamaji/internal.GitCommit=$GIT_HEAD_COMMIT -X github.com/clastix/kamaji/internal.GitDirty=$GIT_MODIFIED -X github.com/clastix/kamaji/internal.BuildTime=$BUILD_DATE" \
    -a -o kamaji main.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/kamaji .
USER 65532:65532

ENTRYPOINT ["/kamaji"]
