# Kairos worker OS on Hadron for `os: kairos` pools: kubelet, kubeadm and
# containerd, joined to the tenant control plane by `kubeadm join` from the
# Kairos bootstrap provider. The Makefile builds one per Kubernetes minor in
# files/versions.yaml and turns it into a bootable disk with AuroraBoot.
# hadron and hadron-toolchain move together: the toolchain stage fetches what
# the hadron stage runs.
ARG HADRON_IMAGE=ghcr.io/kairos-io/hadron:v0.5.3@sha256:ebc9fcc724a34c9f359ced6401e815112b4ea8ff74c980a719fab3be45c471cf
ARG HADRON_TOOLCHAIN_IMAGE=ghcr.io/kairos-io/hadron-toolchain:v0.5.3@sha256:61d118d11bf07bd5716a67be91e9254e4316b8bbfcaf32d2451d4b3247cbe244
ARG KAIROS_INIT_IMAGE=quay.io/kairos/kairos-init:v4.3.0@sha256:c8c5d46234ddda93908f090f136fb12826e312286f63e0b6f316e4063edeea53

FROM ${KAIROS_INIT_IMAGE} AS kairos-init

# The upstream kubelet release binary links against glibc and Hadron is musl,
# so kubelet is built from source without cgo, with the Go toolchain its tree
# names, which go fetches through GOPROXY. The upstream kubeadm is static and
# is taken from the same release; the bootstrap provider refuses a join unless
# kubeadm's version equals the Machine's to the patch.
FROM --platform=$BUILDPLATFORM golang:1.27@sha256:3680233e3204827fbdc66088528ae6d4b3d034f51d03a99d454f6de034888244 AS kubelet
ARG GOPROXY
ARG KUBERNETES_VERSION
ARG TARGETARCH
WORKDIR /src
RUN test -n "${KUBERNETES_VERSION}" \
    && curl -fsSL "https://github.com/kubernetes/kubernetes/archive/refs/tags/${KUBERNETES_VERSION}.tar.gz" | tar -xz --strip-components=1
RUN export GOTOOLCHAIN="go$(cat .go-version)"; \
    CGO_ENABLED=0 GOOS=linux GOARCH="${TARGETARCH}" go build -trimpath \
      -ldflags "-s -w -X k8s.io/component-base/version.gitVersion=${KUBERNETES_VERSION} -X k8s.io/component-base/version.gitTreeState=clean" \
      -o /out/kubelet ./cmd/kubelet

FROM ${HADRON_TOOLCHAIN_IMAGE} AS fetch
ARG TARGETARCH
ARG KUBERNETES_VERSION
ARG CONTAINERD_VERSION=2.2.9
ARG RUNC_VERSION=v1.5.2
ARG CNI_PLUGINS_VERSION=v1.9.1
WORKDIR /dest
COPY --from=kubelet /out/kubelet usr/bin/
RUN mkdir -p usr/bin usr/sbin opt/cni/bin usr/lib/systemd/system && \
    curl -fL "https://dl.k8s.io/release/${KUBERNETES_VERSION}/bin/linux/${TARGETARCH}/kubeadm" -o usr/bin/kubeadm && \
    echo "$(curl -fsSL "https://dl.k8s.io/release/${KUBERNETES_VERSION}/bin/linux/${TARGETARCH}/kubeadm.sha512")  usr/bin/kubeadm" | sha512sum -c - && \
    chmod 0755 usr/bin/kubeadm && \
    curl -fL "https://github.com/containerd/containerd/releases/download/v${CONTAINERD_VERSION}/containerd-static-${CONTAINERD_VERSION}-linux-${TARGETARCH}.tar.gz" -o /tmp/containerd.tgz && \
    curl -fL "https://github.com/containerd/containerd/releases/download/v${CONTAINERD_VERSION}/containerd-static-${CONTAINERD_VERSION}-linux-${TARGETARCH}.tar.gz.sha256sum" -o /tmp/containerd.sha && \
    (cd /tmp && sed 's# .*#  containerd.tgz#' containerd.sha | sha256sum -c -) && \
    tar -xzf /tmp/containerd.tgz -C usr && \
    curl -fL "https://raw.githubusercontent.com/containerd/containerd/v${CONTAINERD_VERSION}/containerd.service" -o usr/lib/systemd/system/containerd.service && \
    sed -i 's#/usr/local/bin/containerd#/usr/bin/containerd#' usr/lib/systemd/system/containerd.service && \
    curl -fL "https://github.com/opencontainers/runc/releases/download/${RUNC_VERSION}/runc.${TARGETARCH}" -o /tmp/runc.${TARGETARCH} && \
    curl -fsSL "https://github.com/opencontainers/runc/releases/download/${RUNC_VERSION}/runc.sha256sum" | grep " runc.${TARGETARCH}$" > /tmp/runc.sha && \
    (cd /tmp && sha256sum -c runc.sha) && \
    install -m 0755 /tmp/runc.${TARGETARCH} usr/sbin/runc && \
    curl -fL "https://github.com/containernetworking/plugins/releases/download/${CNI_PLUGINS_VERSION}/cni-plugins-linux-${TARGETARCH}-${CNI_PLUGINS_VERSION}.tgz" -o /tmp/cni.tgz && \
    curl -fsSL "https://github.com/containernetworking/plugins/releases/download/${CNI_PLUGINS_VERSION}/cni-plugins-linux-${TARGETARCH}-${CNI_PLUGINS_VERSION}.tgz.sha256" | sed 's# .*#  cni.tgz#' > /tmp/cni.sha && \
    (cd /tmp && sha256sum -c cni.sha) && \
    tar -xzf /tmp/cni.tgz -C opt/cni/bin

FROM ${HADRON_IMAGE} AS os
ARG KUBERNETES_VERSION
COPY --from=fetch /dest/ /
COPY rootfs/ /
# yip creates a missing parent directory from the file's mode plus owner
# execute (0644 gives 0744), so systemd-resolved could not enter a directory
# first created for the pool's resolved drop-in.
RUN systemctl enable containerd.service kubelet.service \
    && install -d -m 0755 /etc/systemd/resolved.conf.d

RUN --mount=type=bind,from=kairos-init,src=/kairos-init,dst=/kairos-init \
    /kairos-init --version "${KUBERNETES_VERSION}" --stage install

RUN --mount=type=bind,from=kairos-init,src=/kairos-init,dst=/kairos-init \
    /kairos-init --version "${KUBERNETES_VERSION}" --stage init

# A dynamically linked binary does not run on musl Hadron, which the node
# would only show when kubelet fails to start; refuse it at build time.
FROM --platform=$BUILDPLATFORM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 AS static-check
RUN apk add --no-cache file
COPY --from=os /usr/bin/kubelet /usr/bin/kubeadm /check/
RUN set -eux; \
    for b in /check/kubelet /check/kubeadm; do \
      file "$b" | grep -q 'statically linked'; \
      if file "$b" | grep -q interpreter; then exit 1; fi; \
    done; \
    touch /static-ok

FROM os AS test
ARG KUBERNETES_VERSION
COPY --from=static-check /static-ok /tmp/
RUN set -eux; \
    for b in /usr/bin/kubelet /usr/bin/kubeadm; do \
      if out=$(ldd "$b" 2>&1); then echo "$b is dynamic: $out"; exit 1; fi; \
      echo "$out" | grep -q 'Not a valid dynamic program'; \
    done; \
    test "$(kubelet --version)" = "Kubernetes ${KUBERNETES_VERSION}"; \
    test "$(kubeadm version --output short)" = "${KUBERNETES_VERSION}"; \
    containerd --version; \
    runc --version | head -1; \
    test -x /opt/cni/bin/loopback; \
    systemctl is-enabled containerd.service kubelet.service; \
    test -s /etc/kairos-release; \
    find /etc/systemd/resolved.conf.d -maxdepth 0 -type d -perm 0755 | grep -q .; \
    for s in 'EnvironmentFile=-/var/lib/kubelet/kubeadm-flags.env' 'EnvironmentFile=-/etc/sysconfig/kubelet' \
             '--kubeconfig=/etc/kubernetes/kubelet.conf' '$KUBELET_KUBEADM_ARGS'; do \
      grep -q -F -e "$s" /usr/lib/systemd/system/kubelet.service; \
    done; \
    ! command -v k3s

FROM os
