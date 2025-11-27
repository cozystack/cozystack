FROM golang:1.25-alpine AS builder

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
