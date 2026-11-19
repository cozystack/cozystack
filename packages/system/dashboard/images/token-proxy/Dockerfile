FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d AS builder
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
