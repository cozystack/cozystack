FROM golang:1.27-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

COPY go.mod go.sum ./
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go mod download

COPY api api/
COPY pkg pkg/
COPY cmd cmd/
COPY internal internal/

RUN GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 go build -ldflags="-extldflags=-static" -o /lineage-controller-webhook cmd/lineage-controller-webhook/main.go

FROM scratch

COPY --from=builder /lineage-controller-webhook /lineage-controller-webhook
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

ENTRYPOINT ["/lineage-controller-webhook"]
