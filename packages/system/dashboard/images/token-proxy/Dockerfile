FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS builder
ARG GOPROXY

ARG TARGETOS
ARG TARGETARCH

COPY go.mod go.sum /src/
WORKDIR /src
RUN for attempt in 1 2 3; do go mod download && break; [ "$attempt" = 3 ] && exit 1; sleep $((attempt * 10)); done
COPY *.go /src/

# Build the whole package, not main.go alone, so a future split into
# helper files does not silently drop them from the binary. go build
# excludes _test.go files automatically.
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /token-proxy -ldflags '-extldflags "-static" -w -s' .

FROM scratch

COPY --from=builder /token-proxy /token-proxy

# UID 1001 matches the pod-level runAsUser in gatekeeper.yaml so the
# image runs identically inside the cluster and standalone (e.g. for
# local debugging via `docker run`). The previous 65532 worked only
# because the pod SecurityContext overrides it.
USER 1001:1001
ENTRYPOINT ["/token-proxy"]
