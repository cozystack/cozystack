FROM ubuntu:22.04

ARG KUBECTL_VERSION=1.33.2
ARG TALOSCTL_VERSION=1.10.4
ARG HELM_VERSION=3.18.3
ARG COZYPKG_VERSION=1.1.0

ARG TARGETOS
ARG TARGETARCH

RUN apt update -q
RUN apt install -yq --no-install-recommends psmisc genisoimage ca-certificates qemu-kvm qemu-utils iproute2 iptables wget xz-utils netcat curl jq make git
RUN curl -sSL "https://github.com/siderolabs/talos/releases/download/v${TALOSCTL_VERSION}/talosctl-${TARGETOS}-${TARGETARCH}" -o /usr/local/bin/talosctl \
 && chmod +x /usr/local/bin/talosctl
RUN curl -sSL "https://dl.k8s.io/release/v${KUBECTL_VERSION}/bin/${TARGETOS}/${TARGETARCH}/kubectl" -o /usr/local/bin/kubectl \
 && chmod +x /usr/local/bin/kubectl
RUN curl -sSL "https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3" | bash -s - --version "v${HELM_VERSION}"
RUN curl -sSL "https://github.com/mikefarah/yq/releases/download/v4.44.3/yq_${TARGETOS}_${TARGETARCH}" -o /usr/local/bin/yq \
 && chmod +x /usr/local/bin/yq
RUN curl -sSL "https://fluxcd.io/install.sh" | bash
RUN curl -sSL "https://github.com/cozystack/cozypkg/raw/refs/heads/main/hack/install.sh" | sh -s -- -v "${COZYPKG_VERSION}"
RUN curl https://dl.min.io/client/mc/release/${TARGETOS}-${TARGETARCH}/mc --create-dirs -o  /usr/local/bin/mc \
&& chmod +x /usr/local/bin/mc
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
