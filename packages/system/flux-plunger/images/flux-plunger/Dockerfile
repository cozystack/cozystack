FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d AS builder
ARG GOPROXY

ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

COPY go.mod go.sum ./
RUN for attempt in 1 2 3; do GOOS=$TARGETOS GOARCH=$TARGETARCH go mod download && break; [ "$attempt" = 3 ] && exit 1; sleep $((attempt * 10)); done

COPY pkg pkg/
COPY cmd cmd/
COPY internal internal/
COPY api api/

RUN GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 go build -ldflags="-extldflags=-static" -o /flux-plunger cmd/flux-plunger/main.go

FROM scratch

COPY --from=builder /flux-plunger /flux-plunger
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

USER 65532:65532
ENTRYPOINT ["/flux-plunger"]
