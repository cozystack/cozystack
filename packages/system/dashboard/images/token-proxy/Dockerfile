FROM golang:1.26-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

COPY go.mod go.sum /src/
COPY *.go /src/
WORKDIR /src

# Build the whole package, not main.go alone, so a future split into
# helper files does not silently drop them from the binary. go build
# excludes _test.go files automatically.
RUN go build -o /token-proxy -ldflags '-extldflags "-static" -w -s' .

FROM scratch

COPY --from=builder /token-proxy /token-proxy

# UID 1001 matches the pod-level runAsUser in gatekeeper.yaml so the
# image runs identically inside the cluster and standalone (e.g. for
# local debugging via `docker run`). The previous 65532 worked only
# because the pod SecurityContext overrides it.
USER 1001:1001
ENTRYPOINT ["/token-proxy"]
