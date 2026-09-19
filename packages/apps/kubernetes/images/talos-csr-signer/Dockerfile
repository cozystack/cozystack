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
FROM golang:1.26@sha256:6c2a5538f964f1c82f97ad14988bf05de100d922d159d0e398b54c7b0ca0c6c9 AS builder

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

FROM gcr.io/distroless/static:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
WORKDIR /
COPY --from=builder /workspace/talos-csr-signer /talos-csr-signer
USER 65532:65532

ENTRYPOINT ["/talos-csr-signer"]
