# syntax = docker/dockerfile:experimental@sha256:600e5c62eedff338b3f7a0850beb7c05866e0ef27b2d2e8c02aa468e78496ff5
ARG VERSION="1.15.10"
ARG BASE_TAG=v$VERSION

FROM golang:1.26-bookworm@sha256:9fdc884aacc3bec89b20ffc69f4bb369c78210e3e4f600387b5128b12c199f81 as builder

ARG TAG="v1.15.10"
RUN git clone --branch ${TAG} --depth 1 https://github.com/kubeovn/kube-ovn /source

WORKDIR /source

COPY patches /patches
RUN git apply /patches/*.diff
RUN make build-go

WORKDIR /source/dist/images

# imported from https://github.com/kubeovn/kube-ovn/blob/master/dist/images/Dockerfile
FROM kubeovn/kube-ovn-base:$BASE_TAG AS setcap

COPY --from=builder /source/dist/images/*.sh /kube-ovn/
COPY --from=builder /source/dist/images/kubectl-ko /kube-ovn/kubectl-ko
COPY --from=builder /source/dist/images/01-kube-ovn.conflist /kube-ovn/01-kube-ovn.conflist

COPY --from=builder /source/dist/images/kube-ovn /kube-ovn/kube-ovn
COPY --from=builder /source/dist/images/kube-ovn-cmd /kube-ovn/kube-ovn-cmd
COPY --from=builder /source/dist/images/kube-ovn-daemon /kube-ovn/kube-ovn-daemon
COPY --from=builder /source/dist/images/kube-ovn-controller /kube-ovn/kube-ovn-controller
RUN ln -s /kube-ovn/kube-ovn-cmd /kube-ovn/kube-ovn-monitor && \
    ln -s /kube-ovn/kube-ovn-cmd /kube-ovn/kube-ovn-speaker && \
    ln -s /kube-ovn/kube-ovn-cmd /kube-ovn/kube-ovn-webhook && \
    ln -s /kube-ovn/kube-ovn-cmd /kube-ovn/kube-ovn-leader-checker && \
    ln -s /kube-ovn/kube-ovn-cmd /kube-ovn/kube-ovn-ic-controller && \
    ln -s /kube-ovn/kube-ovn-controller /kube-ovn/kube-ovn-pinger && \
    setcap CAP_NET_BIND_SERVICE+eip /kube-ovn/kube-ovn-cmd && \
    setcap CAP_NET_RAW,CAP_NET_BIND_SERVICE+eip /kube-ovn/kube-ovn-controller && \
    setcap CAP_NET_ADMIN,CAP_NET_RAW,CAP_NET_BIND_SERVICE,CAP_SYS_ADMIN+eip /kube-ovn/kube-ovn-daemon

FROM kubeovn/kube-ovn-base:$BASE_TAG

COPY --chmod=0644 --from=builder /source/dist/images/logrotate/* /etc/logrotate.d/
COPY --from=builder /source/dist/images/grace_stop_ovn_controller /usr/share/ovn/scripts/grace_stop_ovn_controller

COPY --from=setcap /kube-ovn /kube-ovn
RUN /kube-ovn/iptables-wrapper-installer.sh --no-sanity-check

WORKDIR /kube-ovn
