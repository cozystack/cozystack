# Chainsaw is built from source because v0.2.15 ends an assert on the first API
# error other than NotFound, so an etcd leader change during a poll fails the
# step long before its timeout. The patch keeps polling on transient server
# errors. Go back to the release tarball, dropping this stage, patches/ and
# chainsaw-transient/, once a chainsaw release carries kyverno/chainsaw#2761.
# Go 1.26 and -trimpath follow the v0.2.15 release binary (go1.26.0); a newer
# Go minor is not what that release was tested with.
FROM --platform=$BUILDPLATFORM golang:1.26.8@sha256:3c3e25a4da13fd0478eed2df1eb35a0e667094a7124d3993a6a1d30f71c17e79 AS chainsaw
ARG GOPROXY
ARG CHAINSAW_VERSION=0.2.15
ARG CHAINSAW_SOURCE_SHA256=04f3fc7dd8e958e4fc7d73383bc22b05827240dd2fe7a8e32ee99d8c0332da2a
ARG TARGETOS
ARG TARGETARCH
ARG BUILDOS
ARG BUILDARCH

WORKDIR /workspace

RUN curl -fsSL "https://github.com/kyverno/chainsaw/archive/refs/tags/v${CHAINSAW_VERSION}.tar.gz" -o /tmp/chainsaw.tar.gz \
 && echo "${CHAINSAW_SOURCE_SHA256}  /tmp/chainsaw.tar.gz" | sha256sum -c - \
 && tar -xzf /tmp/chainsaw.tar.gz --strip=1 \
 && rm -f /tmp/chainsaw.tar.gz

COPY patches /patches
RUN git apply /patches/*.patch

# go build never compiles a _test.go, so nothing else runs the tests of the
# packages the patches touch. The list is read into a variable first: a for
# loop drops the exit status of a command substitution.
RUN files=$(git apply --numstat /patches/*.patch) || exit 1; \
    for dir in $(printf '%s\n' "$files" | cut -f3 | grep '\.go$' | xargs -r -n1 dirname | sort -u); do \
      (cd "$dir" && go test .) || exit 1; \
    done

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags "-s -w -X github.com/kyverno/chainsaw/pkg/version.BuildVersion=${CHAINSAW_VERSION}" \
    -o /chainsaw .

# The test executes chainsaw, so a cross build tests a host build of the same
# patched tree instead of the shipped binary. The tests skip without CHAINSAW,
# and a skip reports ok, so a skip fails the step here.
COPY chainsaw-transient /chainsaw-transient
RUN if [ "$TARGETOS/$TARGETARCH" = "$BUILDOS/$BUILDARCH" ]; then \
      bin=/chainsaw; \
    else \
      bin=/tmp/chainsaw-host && CGO_ENABLED=0 go build -o "$bin" . || exit 1; \
    fi; \
    cd /chainsaw-transient && out=$(CHAINSAW="$bin" go test -count=1 -v .) || { printf '%s\n' "$out"; exit 1; }; \
    printf '%s\n' "$out"; \
    if printf '%s\n' "$out" | grep -q -- '--- SKIP'; then echo "chainsaw-transient skipped its tests" >&2; exit 1; fi

FROM ubuntu:24.04@sha256:008173c23f95b170204355c12626cb5a965d779a7e1283b09e9cffbb1bf33ca3

ARG KUBECTL_VERSION=1.33.13
ARG TALOSCTL_VERSION=1.13.5
ARG HELM_VERSION=3.21.2
ARG COZYHR_VERSION=1.6.1
ARG CRUST_GATHER_VERSION=v0.15.2
ARG MC_VERSION=RELEASE.2025-08-13T08-35-41Z
ARG FLUX_VERSION=2.8.6

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
RUN curl -fsSL "https://fluxcd.io/install.sh" -o /tmp/flux-install.sh \
 && FLUX_VERSION="${FLUX_VERSION}" bash /tmp/flux-install.sh \
 && rm -f /tmp/flux-install.sh
RUN curl -sSL "https://github.com/cozystack/cozyhr/raw/refs/heads/main/hack/install.sh" | sh -s -- -v "${COZYHR_VERSION}"
# dl.min.io stopped serving community mc builds (HTTP 410) when MinIO archived
# the project; the final release is still published on GitHub.
RUN case "${TARGETARCH}" in \
      amd64) sum=01f866e9c5f9b87c2b09116fa5d7c06695b106242d829a8bb32990c00312e891 ;; \
      arm64) sum=14c8c9616cfce4636add161304353244e8de383b2e2752c0e9dad01d4c27c12c ;; \
      *) echo "no pinned mc checksum for ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
 && curl -fsSL "https://github.com/minio/mc/releases/download/${MC_VERSION}/mc.${TARGETOS}-${TARGETARCH}.${MC_VERSION}" -o /usr/local/bin/mc \
 && echo "${sum}  /usr/local/bin/mc" | sha256sum -c - \
 && chmod +x /usr/local/bin/mc
# crust-gather: full-cluster diagnostic snapshots, captured per failed test by
# cozytest.sh (BATS install/openapi tests) and by the global .chainsaw.yaml
# error.catch (Chainsaw app suite) — host plus each nested tenant cluster — and
# `crust-gather serve`-able from the cozyreport artifact. Installs a static musl
# binary as crust-gather.
RUN curl -sSfL "https://github.com/crust-gather/crust-gather/raw/main/install.sh" | sh -s -- --version "${CRUST_GATHER_VERSION}" --bin-dir /usr/local/bin --yes
COPY --from=chainsaw /chainsaw /usr/local/bin/chainsaw
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
