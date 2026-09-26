# Upstream publishes this image for amd64 only, so it is rebuilt here from the
# release tag for every platform the velero server runs on.
FROM --platform=$BUILDPLATFORM golang:1.27@sha256:3680233e3204827fbdc66088528ae6d4b3d034f51d03a99d454f6de034888244 AS builder

ARG VERSION=v0.9.0
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

RUN curl -sSfL https://github.com/kubevirt/kubevirt-velero-plugin/archive/refs/tags/${VERSION}.tar.gz | tar -xzf- --strip=1

RUN GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 go build -installsuffix static -o kubevirt-velero-plugin .

# The velero server runs this as an init container whose entrypoint copies the
# plugin into the plugins volume mounted at /target, so the image needs a shell
# and cp, as upstream's alpine base provides.
FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
COPY --from=builder /workspace/kubevirt-velero-plugin /plugins/
USER nobody:nogroup
ENTRYPOINT ["/bin/sh", "-c", "cp /plugins/* /target/."]
