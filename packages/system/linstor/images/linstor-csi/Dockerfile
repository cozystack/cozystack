FROM golang:1.26@sha256:9d2f36f06329b2a141b9db99ffa32765cf695ee57b813ca29e245e8670bcbfff AS builder

ARG VERSION=v1.11.2
ARG LINSTOR_WAIT_UNTIL_VERSION=v0.3.1
ARG TARGETARCH
ARG TARGETOS

WORKDIR /src

RUN curl -sSL https://github.com/piraeusdatastore/linstor-csi/archive/refs/tags/${VERSION}.tar.gz | tar -xzvf- --strip=1

COPY patches /patches
RUN git apply /patches/*.diff

RUN go mod download

RUN GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 \
    go build \
    -a \
    -ldflags "-X github.com/piraeusdatastore/linstor-csi/pkg/driver.Version=$VERSION -extldflags -static" \
    -o /linstor-csi \
    ./cmd/linstor-csi/linstor-csi.go

RUN curl -fsSL https://github.com/LINBIT/linstor-wait-until/releases/download/$LINSTOR_WAIT_UNTIL_VERSION/linstor-wait-until-$LINSTOR_WAIT_UNTIL_VERSION-$TARGETOS-$TARGETARCH.tar.gz | tar xvzC /

FROM debian:trixie-slim@sha256:d7e12182ce18b85b93007c1dedf31f2d29e01ccf3182cc4017c709b6259bc132

RUN apt-get update && apt-get install -y --no-install-recommends \
      xfsprogs e2fsprogs nfs-common \
      && apt-get clean && rm -rf /var/lib/apt/lists/* \
      && ln -sf /proc/mounts /etc/mtab

COPY --from=builder /linstor-csi /
COPY --from=builder /linstor-wait-until /linstor-wait-until

ENTRYPOINT ["/linstor-csi"]
