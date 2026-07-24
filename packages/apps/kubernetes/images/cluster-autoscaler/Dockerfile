# Source: https://raw.githubusercontent.com/kubernetes/autoscaler/refs/heads/master/cluster-autoscaler/Dockerfile.amd64
ARG builder_image=docker.io/library/golang:1.26@sha256:3aff6657219a4d9c14e27fb1d8976c49c29fddb70ba835014f477e1c70636647
ARG BASEIMAGE=gcr.io/distroless/static:nonroot-${TARGETARCH}

FROM ${builder_image} AS builder

ARG TARGETOS
ARG TARGETARCH
ENV GOOS=$TARGETOS
ENV GOARCH=$TARGETARCH

RUN git clone https://github.com/kubernetes/autoscaler /src/autoscaler \
 && cd /src/autoscaler/cluster-autoscaler \
 && git checkout cluster-autoscaler-1.32.0

WORKDIR /src/autoscaler/cluster-autoscaler
COPY fix-downscale.diff /fix-downscale.diff
RUN git apply /fix-downscale.diff
RUN make build

FROM $BASEIMAGE
LABEL maintainer="Marcin Wielgus <mwielgus@google.com>"

ARG TARGETARCH

COPY --from=builder /src/autoscaler/cluster-autoscaler/cluster-autoscaler-${TARGETARCH} /cluster-autoscaler
WORKDIR /
CMD ["/cluster-autoscaler"]
