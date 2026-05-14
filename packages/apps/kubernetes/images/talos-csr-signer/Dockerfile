# Builds clastix/talos-csr-signer from upstream source at a pinned commit.
# The upstream project does not publish semver releases yet (only a rolling
# `:latest` tag on ghcr.io/clastix/talos-csr-signer), so we mirror it into
# ghcr.io/cozystack/cozystack/talos-csr-signer:<commit> with a digest pin.
#
# To bump the upstream version, update COMMIT below to the desired master
# SHA and rerun `make image-talos-csr-signer` in packages/apps/kubernetes.
FROM golang:1.26 AS builder

# Upstream is at clastix/talos-csr-signer. Pin to a master SHA so rebuilds
# are reproducible; CI fingerprints the resulting image by digest.
ARG COMMIT=3b434408954012ee5cc7fe3736166f9b02b58871
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

RUN curl -sSL "https://github.com/clastix/talos-csr-signer/archive/${COMMIT}.tar.gz" \
    | tar -xzf - --strip=1

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -ldflags="-w -s" -o talos-csr-signer .

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/talos-csr-signer /talos-csr-signer
USER 65532:65532

ENTRYPOINT ["/talos-csr-signer"]
