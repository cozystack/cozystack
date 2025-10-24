FROM golang:1.24-alpine as builder

ARG VERSION=3.97
ARG TARGETOS
ARG TARGETARCH

RUN apk add --no-cache git g++ fuse

WORKDIR /workspace

RUN git clone --depth 1 --branch ${VERSION} https://github.com/seaweedfs/seaweedfs.git .

COPY patches /patches
RUN git apply /patches/*.diff

RUN cd weed && \
    export LDFLAGS="-X github.com/seaweedfs/seaweedfs/weed/util/version.COMMIT=$(git rev-parse --short HEAD)" && \
    GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 go build \
    -tags "full" \
    -ldflags "-extldflags -static ${LDFLAGS}" \
    -o /usr/bin/weed

FROM alpine AS final

LABEL author="Chris Lu"

COPY --from=builder /usr/bin/weed /usr/bin/
RUN mkdir -p /etc/seaweedfs
COPY --from=builder /workspace/docker/filer.toml /etc/seaweedfs/filer.toml
COPY --from=builder /workspace/docker/entrypoint.sh /entrypoint.sh
RUN apk add --no-cache fuse

# volume server gprc port
EXPOSE 18080
# volume server http port
EXPOSE 8080
# filer server gprc port
EXPOSE 18888
# filer server http port
EXPOSE 8888
# master server shared gprc port
EXPOSE 19333
# master server shared http port
EXPOSE 9333
# s3 server http port
EXPOSE 8333
# webdav server http port
EXPOSE 7333

RUN mkdir -p /data/filerldb2

VOLUME /data
WORKDIR /data

RUN chmod +x /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]

