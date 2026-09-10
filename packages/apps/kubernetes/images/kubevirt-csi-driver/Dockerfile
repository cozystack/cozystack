ARG builder_image=docker.io/library/golang:1.26@sha256:3c3e25a4da13fd0478eed2df1eb35a0e667094a7124d3993a6a1d30f71c17e79
FROM ${builder_image} AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY *.go ./
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
    -ldflags "-X kubevirt.io/csi-driver/pkg/service.VendorVersion=0.2.0" \
    -o kubevirt-csi-driver .

FROM docker.io/library/almalinux:9@sha256:3a3fa7f043b142bc8008c8b308d39b47d2c84008addcd52f9f9a7a82d2a90474

RUN dnf install -y e2fsprogs xfsprogs nfs-utils && dnf clean all

COPY --from=builder /src/kubevirt-csi-driver .

ENTRYPOINT ["./kubevirt-csi-driver"]
