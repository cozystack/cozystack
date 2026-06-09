# migration 45 runs the etcd-migrate adoption tool to move legacy etcd.aenix.io
# clusters onto etcd-operator.cozystack.io/v1alpha2. etcd-migrate is published
# as a standalone CLI asset of the etcd-operator release (the operator image
# itself contains only /manager), so we fetch the linux binary for the build's
# target arch and verify it against the release's cli-SHA256SUMS.txt. Bump
# ETCD_OPERATOR_VERSION (and the pinned checksums below) together, keeping it in
# sync with packages/system/etcd-operator (Chart appVersion).
FROM alpine:3.24

ARG TARGETOS
ARG TARGETARCH
ARG ETCD_OPERATOR_VERSION=v0.5.1

RUN wget -O- https://github.com/cozystack/cozyhr/raw/refs/heads/main/hack/install.sh | sh -s -- -v 1.6.1

RUN apk add --no-cache kubectl helm coreutils git jq ca-certificates bash curl

# SHA-256 of etcd-migrate-linux-<arch> from the etcd-operator v0.5.1 release
# (cli-SHA256SUMS.txt). The migrations image only ever runs as a Kubernetes Job,
# so only linux arches are needed.
RUN set -eux; \
    case "${TARGETARCH}" in \
      amd64) sha256="e8f7781abc4b0d18dd7a6ddb8ac47aca544ffdc20518b5850da333256b44030b" ;; \
      arm64) sha256="bcb81ef0b154ef6ba3341e3834b00479b10397894b430d785dc60715730b54e2" ;; \
      *) echo "unsupported TARGETARCH=${TARGETARCH:-<empty>} (build with buildx)" >&2; exit 1 ;; \
    esac; \
    curl -fsSL -o /usr/bin/etcd-migrate \
      "https://github.com/cozystack/etcd-operator/releases/download/${ETCD_OPERATOR_VERSION}/etcd-migrate-${TARGETOS:-linux}-${TARGETARCH}"; \
    echo "${sha256}  /usr/bin/etcd-migrate" | sha256sum -c -; \
    chmod +x /usr/bin/etcd-migrate

COPY migrations /migrations
COPY run-migrations.sh /usr/bin/run-migrations.sh

WORKDIR /migrations

ENTRYPOINT ["/usr/bin/run-migrations.sh"]
