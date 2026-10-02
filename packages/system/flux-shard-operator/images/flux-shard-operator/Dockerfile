FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS builder
ARG GOPROXY

ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

COPY go.mod go.sum ./
RUN for attempt in 1 2 3; do GOOS=$TARGETOS GOARCH=$TARGETARCH go mod download && break; [ "$attempt" = 3 ] && exit 1; sleep $((attempt * 10)); done

COPY api api/
COPY pkg pkg/
COPY cmd cmd/
COPY internal internal/

RUN GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 go build -ldflags="-extldflags=-static" -o /flux-shard-operator cmd/flux-shard-operator/main.go

FROM scratch

COPY --from=builder /flux-shard-operator /flux-shard-operator
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

ENTRYPOINT ["/flux-shard-operator"]
