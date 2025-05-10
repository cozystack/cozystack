FROM golang:1.23-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

COPY go.mod go.sum ./
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go mod download

COPY api api/
COPY pkg pkg/
COPY cmd cmd/
COPY internal internal/

RUN GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 go build -ldflags="-extldflags=-static" -o /cozystack-api cmd/cozystack-api/main.go

FROM scratch

COPY --from=builder /cozystack-api /cozystack-api

ENTRYPOINT ["/cozystack-api"]
