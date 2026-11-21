FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS builder
ARG GOPROXY

ARG TARGETOS
ARG TARGETARCH

COPY go.mod go.sum /src/
WORKDIR /src
RUN for attempt in 1 2 3; do go mod download && break; [ "$attempt" = 3 ] && exit 1; sleep $((attempt * 10)); done
COPY *.go /src/

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /console-proxy -ldflags '-w -s' .

FROM scratch

COPY --from=builder /console-proxy /console-proxy

USER 65532:65532
ENTRYPOINT ["/console-proxy"]
