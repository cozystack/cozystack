# Builds clastix/talos-csr-signer from upstream source at a pinned commit.
# The upstream project does not publish semver releases yet (only a rolling
# `:latest` tag on ghcr.io/clastix/talos-csr-signer), so we mirror it into
# ghcr.io/cozystack/cozystack/talos-csr-signer:<chart-version> with a digest
# pin. The image tag is the kubernetes chart version (KUBERNETES_PKG_TAG /
# IMAGE_TAG in the Makefile), not the source commit — the commit is
# fingerprinted indirectly via the resulting image digest, which the
# Makefile writes into images/talos-csr-signer.tag.
#
# To bump the upstream version, update COMMIT below to the desired master
# SHA and rerun `make image-talos-csr-signer` in packages/apps/kubernetes;
# the regenerated images/talos-csr-signer.tag must be committed alongside.
FROM golang:1.26@sha256:45a5f7a810238aabcbad211d70b9ae082022d96f7c7259e94041ad1b933575ac AS builder

# Upstream is at clastix/talos-csr-signer. Pin to a master SHA so rebuilds
# are reproducible; the resulting image digest is what CI fingerprints
# (via images/talos-csr-signer.json), not the upstream commit directly.
ARG COMMIT=3b434408954012ee5cc7fe3736166f9b02b58871
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

RUN curl -sSL "https://github.com/clastix/talos-csr-signer/archive/${COMMIT}.tar.gz" \
    | tar -xzf - --strip=1

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -ldflags="-w -s" -o talos-csr-signer .

FROM gcr.io/distroless/static:nonroot@sha256:1c2c046bc09ed40fad370b599a0b1ae7987f55b01e247cf27a7c27cd97e5bbc7
WORKDIR /
COPY --from=builder /workspace/talos-csr-signer /talos-csr-signer
USER 65532:65532

ENTRYPOINT ["/talos-csr-signer"]
