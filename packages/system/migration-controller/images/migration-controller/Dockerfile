# Pinned to the build host's own architecture, not the target's: the Go
# toolchain cross-compiles perfectly well, and running it under QEMU emulation
# instead makes its garbage collector die with "found bad pointer in Go heap"
# on an arm64 host building for amd64. The output binary is identical either
# way, and a native build is far faster.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

COPY go.mod go.sum ./
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go mod download

COPY api api/
COPY pkg pkg/
COPY cmd cmd/
COPY internal internal/

RUN GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 go build -ldflags="-extldflags=-static" -o /migration-controller cmd/migration-controller/main.go

FROM scratch

COPY --from=builder /migration-controller /migration-controller
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

USER 65532:65532
ENTRYPOINT ["/migration-controller"]
