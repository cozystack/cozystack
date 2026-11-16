# Kairos worker OS on Hadron: kubelet, kubeadm and
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
    && curl -fsSL "https://dl.k8s.io/release/${KUBERNETES_VERSION}/kubernetes-src.tar.gz" -o /tmp/src.tgz \
    && echo "$(curl -fsSL "https://dl.k8s.io/release/${KUBERNETES_VERSION}/kubernetes-src.tar.gz.sha512")  /tmp/src.tgz" | sha512sum -c - \
    && tar -xzf /tmp/src.tgz && rm /tmp/src.tgz
RUN export GOTOOLCHAIN="go$(cat .go-version)"; \
    CGO_ENABLED=0 GOOS=linux GOARCH="${TARGETARCH}" go build -trimpath \
      -ldflags "-s -w -X k8s.io/component-base/version.gitVersion=${KUBERNETES_VERSION} -X k8s.io/component-base/version.gitTreeState=clean" \
      -o /out/kubelet ./cmd/kubelet

FROM ${HADRON_TOOLCHAIN_IMAGE} AS fetch
ARG TARGETARCH
ARG KUBERNETES_VERSION
# Release assets on GitHub can be replaced after publication, so their
# digests are pinned here and bumped with the versions; containerd publishes no
# checksum for its unit file at all.
ARG CONTAINERD_VERSION=2.2.9
ARG CONTAINERD_SHA256_AMD64=c3ec6c9a4274f677d22f9b2963f55e150fb3d0a7fa2d4bd741203bf78c59d5f8
ARG CONTAINERD_SHA256_ARM64=84dc48860ec6f1df16a3637459376cdfd2a5baa3fa5dd1e20e906020e80f9b7c
ARG CONTAINERD_SERVICE_SHA256=1941362cbaa89dd591b99c32b050d82c583d3cd2e5fa63085d7017457ec5fca8
ARG RUNC_VERSION=v1.5.2
ARG RUNC_SHA256_AMD64=599f6f94ff8c5057241eff0d54c3c74f95c34935b6457b33fe545defc61e9488
ARG RUNC_SHA256_ARM64=d10ecae898361832a059be2089bab92d158aec54661b18ed7346ed79628b46b0
ARG CNI_PLUGINS_VERSION=v1.9.1
ARG CNI_PLUGINS_SHA256_AMD64=b98f74a0f8522f0a83867178729c1aa70f2158f90c45a2ca8fa791db1c76b303
ARG CNI_PLUGINS_SHA256_ARM64=56171987d3947707c3563db2f4001bccaf50fd63468611b9f3cbecb1375ee7ec
WORKDIR /dest
COPY --from=kubelet /out/kubelet usr/bin/
RUN case "${TARGETARCH}" in \
      amd64) containerd_sha="${CONTAINERD_SHA256_AMD64}" runc_sha="${RUNC_SHA256_AMD64}" cni_sha="${CNI_PLUGINS_SHA256_AMD64}" ;; \
      arm64) containerd_sha="${CONTAINERD_SHA256_ARM64}" runc_sha="${RUNC_SHA256_ARM64}" cni_sha="${CNI_PLUGINS_SHA256_ARM64}" ;; \
      *) echo "no pinned digests for ${TARGETARCH}" >&2; exit 1 ;; \
    esac && \
    mkdir -p usr/bin usr/sbin opt/cni/bin usr/lib/systemd/system && \
    curl -fL "https://dl.k8s.io/release/${KUBERNETES_VERSION}/bin/linux/${TARGETARCH}/kubeadm" -o usr/bin/kubeadm && \
    echo "$(curl -fsSL "https://dl.k8s.io/release/${KUBERNETES_VERSION}/bin/linux/${TARGETARCH}/kubeadm.sha512")  usr/bin/kubeadm" | sha512sum -c - && \
    chmod 0755 usr/bin/kubeadm && \
    curl -fL "https://github.com/containerd/containerd/releases/download/v${CONTAINERD_VERSION}/containerd-static-${CONTAINERD_VERSION}-linux-${TARGETARCH}.tar.gz" -o /tmp/containerd.tgz && \
    echo "${containerd_sha}  /tmp/containerd.tgz" | sha256sum -c - && \
    tar -xzf /tmp/containerd.tgz -C usr && \
    curl -fL "https://raw.githubusercontent.com/containerd/containerd/v${CONTAINERD_VERSION}/containerd.service" -o usr/lib/systemd/system/containerd.service && \
    echo "${CONTAINERD_SERVICE_SHA256}  usr/lib/systemd/system/containerd.service" | sha256sum -c - && \
    sed -i 's#/usr/local/bin/containerd#/usr/bin/containerd#' usr/lib/systemd/system/containerd.service && \
    curl -fL "https://github.com/opencontainers/runc/releases/download/${RUNC_VERSION}/runc.${TARGETARCH}" -o /tmp/runc && \
    echo "${runc_sha}  /tmp/runc" | sha256sum -c - && \
    install -m 0755 /tmp/runc usr/sbin/runc && \
    curl -fL "https://github.com/containernetworking/plugins/releases/download/${CNI_PLUGINS_VERSION}/cni-plugins-linux-${TARGETARCH}-${CNI_PLUGINS_VERSION}.tgz" -o /tmp/cni.tgz && \
    echo "${cni_sha}  /tmp/cni.tgz" | sha256sum -c - && \
    tar -xzf /tmp/cni.tgz -C opt/cni/bin

FROM ${HADRON_IMAGE} AS os
ARG KUBERNETES_VERSION
COPY --from=fetch /dest/ /
COPY rootfs/ /
# yip creates a missing parent directory from the file's mode plus owner
# execute (0644 gives 0744), so systemd-resolved could not enter a directory
# first created for a resolved.conf drop-in.
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
