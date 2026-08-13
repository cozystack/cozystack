FROM golang:1.26-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

COPY go.mod go.sum ./
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go mod download

COPY api api/
COPY pkg pkg/
COPY cmd cmd/
COPY internal internal/

RUN GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 go build -ldflags="-extldflags=-static" -o /site-router-controller cmd/site-router-controller/main.go

FROM scratch

COPY --from=builder /site-router-controller /site-router-controller
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

USER 65532:65532
ENTRYPOINT ["/site-router-controller"]
