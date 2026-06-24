FROM ubuntu:24.04

ARG KUBECTL_VERSION=1.33.13
ARG TALOSCTL_VERSION=1.13.5
ARG HELM_VERSION=3.21.2
ARG COZYHR_VERSION=1.6.1
ARG CRUST_GATHER_VERSION=v0.15.2
ARG CHAINSAW_VERSION=0.2.15

ARG TARGETOS
ARG TARGETARCH

RUN apt update -q
RUN apt install -yq --no-install-recommends psmisc genisoimage ca-certificates qemu-kvm qemu-utils iproute2 iptables wget xz-utils netcat-openbsd curl jq make git bash-completion
RUN curl -sSL "https://github.com/siderolabs/talos/releases/download/v${TALOSCTL_VERSION}/talosctl-${TARGETOS}-${TARGETARCH}" -o /usr/local/bin/talosctl \
 && chmod +x /usr/local/bin/talosctl
RUN curl -sSL "https://dl.k8s.io/release/v${KUBECTL_VERSION}/bin/${TARGETOS}/${TARGETARCH}/kubectl" -o /usr/local/bin/kubectl \
 && chmod +x /usr/local/bin/kubectl
RUN curl -sSL "https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3" | bash -s - --version "v${HELM_VERSION}"
RUN curl -sSL "https://github.com/mikefarah/yq/releases/download/v4.53.3/yq_${TARGETOS}_${TARGETARCH}" -o /usr/local/bin/yq \
 && chmod +x /usr/local/bin/yq
RUN curl -sSL "https://fluxcd.io/install.sh" | bash
RUN curl -sSL "https://github.com/cozystack/cozyhr/raw/refs/heads/main/hack/install.sh" | sh -s -- -v "${COZYHR_VERSION}"
RUN curl -fsSL "https://dl.min.io/client/mc/release/${TARGETOS}-${TARGETARCH}/mc" --create-dirs -o /usr/local/bin/mc \
 && chmod +x /usr/local/bin/mc
# crust-gather: full-cluster diagnostic snapshots, captured per failed test by
# cozytest.sh (BATS install/openapi tests) and by the global .chainsaw.yaml
# error.catch (Chainsaw app suite) — host plus each nested tenant cluster — and
# `crust-gather serve`-able from the cozyreport artifact. Installs a static musl
# binary as crust-gather.
RUN curl -sSfL "https://github.com/crust-gather/crust-gather/raw/main/install.sh" | sh -s -- --version "${CRUST_GATHER_VERSION}" --bin-dir /usr/local/bin --yes
RUN curl -sSL "https://github.com/kyverno/chainsaw/releases/download/v${CHAINSAW_VERSION}/chainsaw_${TARGETOS}_${TARGETARCH}.tar.gz" \
 | tar -xz -C /usr/local/bin chainsaw \
 && chmod +x /usr/local/bin/chainsaw
RUN <<'EOF'
cat <<'EOT' >> /etc/bash.bashrc
. /etc/bash_completion
. <(kubectl completion bash)
alias k=kubectl
complete -F __start_kubectl k
EOT
EOF
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
