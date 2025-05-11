# Source: https://github.com/kubevirt/csi-driver/blob/main/Dockerfile
ARG builder_image=docker.io/library/golang:1.22.5
FROM ${builder_image} AS builder
RUN git clone https://github.com/kubevirt/csi-driver /src/kubevirt-csi-driver \
 && cd /src/kubevirt-csi-driver \
 && git checkout 35836e0c8b68d9916d29a838ea60cdd3fc6199cf

ARG TARGETOS
ARG TARGETARCH
ENV GOOS=$TARGETOS
ENV GOARCH=$TARGETARCH

WORKDIR /src/kubevirt-csi-driver
RUN make build

FROM quay.io/centos/centos:stream9
ARG git_url=https://github.com/kubevirt/csi-driver.git

LABEL maintainers="The KubeVirt Project <kubevirt-dev@googlegroups.com>" \
      description="KubeVirt CSI Driver" \
      multi.GIT_URL=${git_url}

ENTRYPOINT ["./kubevirt-csi-driver"]

RUN dnf install -y e2fsprogs xfsprogs && dnf clean all

ARG git_sha=NONE
LABEL multi.GIT_SHA=${git_sha}

COPY --from=builder /src/kubevirt-csi-driver/kubevirt-csi-driver .
