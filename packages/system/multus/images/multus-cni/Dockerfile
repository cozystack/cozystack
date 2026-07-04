FROM --platform=$BUILDPLATFORM golang:1.26@sha256:079e59808d2d252516e27e3f3a9c003740dee7f75e55aa71528766d52bcfc16a AS build

ARG VERSION=v4.3.0
ARG TARGETPLATFORM

WORKDIR /usr/src/multus-cni

RUN curl -sSL https://github.com/k8snetworkplumbingwg/multus-cni/archive/refs/tags/${VERSION}.tar.gz | \
    tar -xzf - --strip=1

COPY patches /patches
RUN git init && git config user.email "build@cozystack" && git config user.name "build" && \
    git add -A && git commit -m "init" && git tag ${VERSION} && \
    git apply /patches/*.diff

RUN ./hack/build-go.sh

# Stage the upstream reference CNI plugins into the image so the
# install-cni-plugins init container can install them into the host
# /opt/cni/bin. Nodes need them for Multus-backed NetworkAttachmentDefinitions
# (bridge, macvlan, ipvlan, ...), but nothing guarantees they are there: k3s
# keeps its own copies under /var/lib/rancher/k3s/data/current/bin, and Cilium
# only installs cilium-cni + loopback, so /opt/cni/bin can be left without a
# `bridge` binary and a bridge-type NAD then fails with "failed to find plugin
# bridge".
#
# loopback is deliberately not staged: multus never delegates to it (the
# runtime calls it for the sandbox lo interface, so a node where it is missing
# runs no pods at all), and both cilium and kube-ovn already install it. portmap
# is staged because a NAD conflist may chain it for hostPort, which does route
# through multus.
#
# portmap and macvlan are therefore shared with kube-ovn, which installs them
# into the same directory -- as of the vendored v1.15.10 with a plain `cp -f`,
# which truncates the destination in place. The init container replaces each
# plugin with an atomic rename instead of writing onto the live path, so a
# concurrent exec never observes a truncated binary -- the same reason multus's
# own install_multus uses CopyFileAtomic and cilium's install-plugin.sh renames
# a temporary file into place. Upstream kube-ovn has since made its own install
# atomic and added ipvlan to the set it stages, so revisit the overlap named
# here when that bump lands.
#
# Keep CNI_PLUGINS_VERSION below in step with the CNI_PLUGINS_VERSION kube-ovn
# pins in its own base image (both are v1.9.1 today). That match is what makes
# sharing portmap and macvlan harmless: the two copies are byte-identical, so it
# does not matter which daemonset wrote last. Let the pins diverge and those two
# plugins flap version with daemonset restart order.
ARG TARGETARCH
ARG CNI_PLUGINS_VERSION=v1.9.1
# SHA-256 of cni-plugins-linux-<arch>-<version>.tgz, taken from the release's
# per-asset .sha256 sidecar. These are root-executed node binaries, so the
# tarball is verified before extraction rather than trusted by tag alone. Bump
# the checksums together with CNI_PLUGINS_VERSION.
RUN set -eux; \
    case "${TARGETARCH}" in \
      amd64) sha256="b98f74a0f8522f0a83867178729c1aa70f2158f90c45a2ca8fa791db1c76b303" ;; \
      arm64) sha256="56171987d3947707c3563db2f4001bccaf50fd63468611b9f3cbecb1375ee7ec" ;; \
      *) echo "unsupported TARGETARCH=${TARGETARCH:-<empty>} (build with buildx)" >&2; exit 1 ;; \
    esac; \
    curl --fail --silent --show-error --location -o /tmp/cni-plugins.tgz \
      "https://github.com/containernetworking/plugins/releases/download/${CNI_PLUGINS_VERSION}/cni-plugins-linux-${TARGETARCH}-${CNI_PLUGINS_VERSION}.tgz"; \
    echo "${sha256}  /tmp/cni-plugins.tgz" | sha256sum -c -; \
    mkdir -p /cni-plugins /cni-plugins-licenses; \
    tar -xz -C /cni-plugins -f /tmp/cni-plugins.tgz \
      ./bridge ./host-local ./static ./macvlan ./ipvlan ./vlan \
      ./tuning ./portmap ./firewall ./bandwidth ./host-device ./ptp ./sbr ./vrf; \
    tar -xz -C /cni-plugins-licenses -f /tmp/cni-plugins.tgz ./LICENSE; \
    rm -f /tmp/cni-plugins.tgz

FROM debian:stable-slim@sha256:ee12ffb55625b99d62837a72f037d9b2f18fd0c787a89c2b9a4f09666c48776c
LABEL org.opencontainers.image.source=https://github.com/cozystack/cozystack
COPY --from=build /usr/src/multus-cni/bin /usr/src/multus-cni/bin
COPY --from=build /usr/src/multus-cni/LICENSE /usr/src/multus-cni/LICENSE
COPY --from=build /usr/src/multus-cni/bin/cert-approver /
COPY --from=build /cni-plugins /cni-plugins
# Apache-2.0 licence of the containernetworking plugins redistributed above.
# Kept out of /cni-plugins so the init container does not install it as a plugin.
COPY --from=build /cni-plugins-licenses /cni-plugins-licenses
WORKDIR /

ENTRYPOINT ["/usr/src/multus-cni/bin/multus-daemon"]
