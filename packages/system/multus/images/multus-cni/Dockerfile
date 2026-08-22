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
# The upstream reference CNI plugins, installed into the host /opt/cni/bin by
# the install-cni-plugins init container. See packages/system/multus/values.yaml
# for the switch.
#
# Four of the release's plugins are not staged:
#
#   loopback   the runtime calls it for every pod sandbox, not only for pods
#              using a NAD; cilium and kube-ovn both install it already.
#   dhcp       needs a long-running host daemon this package does not run.
#   tap, dummy no consumer in this tree.
#
# The list here, the count, and docs/vm-external-vlan.md are asserted against
# each other in hack/cni-plugins-staging-contract.bats.
#
# kube-ovn installs portmap and macvlan into the same directory from its own
# image, so those two have a second writer, and its plugins version lives inside
# its base image rather than in this tree. The marker below records the kube-ovn
# release CNI_PLUGINS_VERSION was last checked against; the same test compares
# the two.
#
# kube-ovn-cni-plugins-checked-against: v1.15.10
#
# Talos strips the same upstream release in siderolabs/talos' Dockerfile down to
# bridge, firewall, host-local, loopback and portmap, and adds flannel from a
# different repo. Four of those are names this image stages -- loopback is not,
# it is excluded above, and flannel was never in the reference set. Staging is on
# for Talos too, so those four are rewritten on every pod recreation; that is
# safe only while both sides pin the SAME containernetworking/plugins version.
# The marker records the Talos release whose pin was compared with
# CNI_PLUGINS_VERSION above -- it is about the VERSIONS matching, not about
# which plugins exist.
#
# talos-cni-plugins-checked-against: v1.13.6
ARG TARGETARCH
ARG CNI_PLUGINS_VERSION=v1.9.1
# SHA-256 of cni-plugins-linux-<arch>-<version>.tgz, from the release's per-asset
# .sha256 sidecar. Bump together with CNI_PLUGINS_VERSION.
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
# Apache-2.0 licence of the plugins redistributed above. Kept out of
# /cni-plugins so it is not installed as a plugin.
COPY --from=build /cni-plugins-licenses /cni-plugins-licenses
WORKDIR /

ENTRYPOINT ["/usr/src/multus-cni/bin/multus-daemon"]
