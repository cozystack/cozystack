# Source: https://raw.githubusercontent.com/kubernetes/autoscaler/refs/heads/master/cluster-autoscaler/Dockerfile.amd64
ARG builder_image=docker.io/library/golang:1.26@sha256:079e59808d2d252516e27e3f3a9c003740dee7f75e55aa71528766d52bcfc16a
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

# go build never compiles a _test.go, so nothing else runs the tests of the
# packages the patch touches. Each package is tested from its own directory,
# below the clone root because the patch names paths from there: that finds
# the module it belongs to, and an empty list runs nothing.
RUN cd /src/autoscaler && for dir in $(git apply --numstat /fix-downscale.diff | cut -f3 | grep '\.go$' | xargs -n1 dirname | sort -u); do \
      (cd "$dir" && go test .) || exit 1; \
    done

RUN make build

FROM $BASEIMAGE
LABEL maintainer="Marcin Wielgus <mwielgus@google.com>"

ARG TARGETARCH

COPY --from=builder /src/autoscaler/cluster-autoscaler/cluster-autoscaler-${TARGETARCH} /cluster-autoscaler
WORKDIR /
CMD ["/cluster-autoscaler"]
