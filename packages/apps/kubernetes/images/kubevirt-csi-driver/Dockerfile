ARG builder_image=docker.io/library/golang:1.27@sha256:e0174e51e81218523251d85d248a90d24c3d5e81543b4f07a5d66229397db190
FROM --platform=$BUILDPLATFORM ${builder_image} AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY *.go ./
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
    -ldflags "-X kubevirt.io/csi-driver/pkg/service.VendorVersion=0.2.0" \
    -o kubevirt-csi-driver .

FROM docker.io/library/almalinux:9@sha256:9819dc675b67b595c2b59e42be7763fca1a8bb217fa5944e04daa22e9a64db16

RUN dnf install -y e2fsprogs xfsprogs nfs-utils && dnf clean all

COPY --from=builder /src/kubevirt-csi-driver .

ENTRYPOINT ["./kubevirt-csi-driver"]
