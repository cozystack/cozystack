# migration 48 runs the etcd-migrate adoption tool to move legacy etcd.aenix.io
# clusters onto etcd-operator.cozystack.io/v1alpha2. etcd-migrate is published
# as a standalone CLI asset of the etcd-operator release (the operator image
# itself contains only /manager), so we fetch the linux binary for the build's
# target arch and verify it against the release's cli-SHA256SUMS.txt. Bump
# ETCD_OPERATOR_VERSION (and the pinned checksums below) together, keeping it in
# sync with packages/system/etcd-operator (Chart appVersion).
FROM alpine:3.24

ARG TARGETOS
ARG TARGETARCH
ARG ETCD_OPERATOR_VERSION=v0.5.2

RUN wget -O- https://github.com/cozystack/cozyhr/raw/refs/heads/main/hack/install.sh | sh -s -- -v 1.6.1

RUN apk add --no-cache kubectl helm coreutils git jq ca-certificates bash curl

# SHA-256 of etcd-migrate-linux-<arch> from the etcd-operator v0.5.2 release
# (cli-SHA256SUMS.txt). The migrations image only ever runs as a Kubernetes Job,
# so only linux arches are needed.
RUN set -eux; \
    case "${TARGETARCH}" in \
      amd64) sha256="34fd617e8c4a6312da99e0c449a6afe7ff92518f263d17c5a082087adbdf1936" ;; \
      arm64) sha256="05fc3096a594c15a963fc18ee4bde17575def48912730cc47225a43aa276859f" ;; \
      *) echo "unsupported TARGETARCH=${TARGETARCH:-<empty>} (build with buildx)" >&2; exit 1 ;; \
    esac; \
    curl -fsSL -o /usr/bin/etcd-migrate \
      "https://github.com/cozystack/etcd-operator/releases/download/${ETCD_OPERATOR_VERSION}/etcd-migrate-${TARGETOS:-linux}-${TARGETARCH}"; \
    echo "${sha256}  /usr/bin/etcd-migrate" | sha256sum -c -; \
    chmod +x /usr/bin/etcd-migrate

COPY migrations /migrations
# v1alpha2 etcd CRDs, vendored into the build context by `make image-migrations`.
# migration 48 applies these before adoption so it does not deadlock waiting for
# the etcd-operator-crds HelmRelease (which only reconciles after the migration
# pre-upgrade hook completes).
COPY etcd-operator-crds /etcd-operator-crds
COPY run-migrations.sh /usr/bin/run-migrations.sh

WORKDIR /migrations

ENTRYPOINT ["/usr/bin/run-migrations.sh"]
