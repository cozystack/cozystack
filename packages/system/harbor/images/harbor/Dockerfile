# Upstream publishes the Harbor v2.15 component images for amd64 only; its
# multi-arch build is on main but in no release yet. This rebuilds the pinned
# release for every platform, one --target per image, following upstream's
# make/photon/<component>/Dockerfile{,.base} and the compile targets of its
# top-level Makefile. The portal is built by images/harbor-portal.
#
# Temporary: once the vendored chart moves to an upstream release that
# publishes arm64 (expected 2.16), delete both Dockerfiles, the image target,
# the image overrides in values.yaml, the build: line in the root Makefile,
# the portal's ARCH_NEUTRAL entry in hack/image-builders-cross-compile.bats
# and the harbor rule in .github/renovate.json, and drop the appVersion check
# from the package's update target, which reads VERSION from this file.
ARG VERSION=v2.15.1
ARG COMMIT=335c05f2b9507b4c7116fe69b84e31ef9f4b91dd
# REGISTRY_SRC_TAG, REGISTRYVERSION, TRIVYADAPTERVERSION, TRIVYVERSION and
# SWAGGER_VERSION in upstream's Makefile at VERSION; the source stage fails
# when they drift.
ARG DISTRIBUTION_VERSION=v2.8.3-harbor.1-rc.1
ARG DISTRIBUTION_COMMIT=086d3c33b3981cf598d0d82336a907fcf5c4c848
ARG REGISTRY_VERSION=v2.8.3-patch-redis
ARG TRIVY_ADAPTER_VERSION=v0.36.0
ARG TRIVY_ADAPTER_COMMIT=f76a65660d4f4897a6c9b445ec91cbd76d685b5e
ARG TRIVY_VERSION=v0.70.0
ARG SWAGGER_VERSION=v0.33.1

# Every Go stage uses this golang image; the source stage checks it against
# upstream's GOBUILDIMAGE.
FROM --platform=$BUILDPLATFORM golang:1.25.9@sha256:8a7adc288b77e9b787cd2695029eb54d10ae80571b21d44fed68d067ad0a9c96 AS source
ARG GOPROXY
ARG VERSION
ARG COMMIT
ARG DISTRIBUTION_VERSION
ARG REGISTRY_VERSION
ARG TRIVY_ADAPTER_VERSION
ARG TRIVY_VERSION
ARG SWAGGER_VERSION
RUN git clone --branch ${VERSION} --depth 1 https://github.com/goharbor/harbor /harbor \
    && test "$(git -C /harbor rev-parse HEAD)" = "${COMMIT}"
WORKDIR /harbor
# A VERSION bump must not pair the new core with the old registry, scanner or
# toolchain.
RUN grep -qxF "REGISTRY_SRC_TAG=${DISTRIBUTION_VERSION}" Makefile \
    && grep -qxF "REGISTRYVERSION=${REGISTRY_VERSION}" Makefile \
    && grep -qxF "TRIVYADAPTERVERSION=${TRIVY_ADAPTER_VERSION}" Makefile \
    && grep -qxF "TRIVYVERSION=${TRIVY_VERSION}" Makefile \
    && grep -qxF "SWAGGER_VERSION=${SWAGGER_VERSION}" Makefile \
    && grep -qxF "GOBUILDIMAGE=golang:$(go env GOVERSION | sed 's/^go//')" Makefile
# Upstream's gen_apis: the API server package is generated, not committed.
RUN go run github.com/go-swagger/go-swagger/cmd/swagger@${SWAGGER_VERSION} generate server \
      --template-dir=tools/swagger/templates --exclude-main \
      --additional-initialism=CVE --additional-initialism=GC --additional-initialism=OIDC \
      -f api/v2.0/swagger.yaml -A harbor --target src/server/v2.0
# Upstream's versions_prepare; core serves it as the component versions.
RUN printf 'VERSION_TAG: %s\nREGISTRY_VERSION: %s\nTRIVY_VERSION: %s\nTRIVY_ADAPTER_VERSION: %s\n' \
      "${VERSION}" "${REGISTRY_VERSION}" "${TRIVY_VERSION}" "${TRIVY_ADAPTER_VERSION}" > make/photon/prepare/versions

FROM --platform=$BUILDPLATFORM golang:1.25.9@sha256:8a7adc288b77e9b787cd2695029eb54d10ae80571b21d44fed68d067ad0a9c96 AS core-bin
ARG GOPROXY
ARG VERSION
ARG COMMIT
ARG TARGETOS
ARG TARGETARCH
COPY --from=source /harbor/src /harbor/src
WORKDIR /harbor/src/core
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -buildvcs=false \
      -ldflags "-w -s -X github.com/goharbor/harbor/src/pkg/version.GitCommit=$(echo ${COMMIT} | cut -c1-8) -X github.com/goharbor/harbor/src/pkg/version.ReleaseVersion=${VERSION}" \
      -o /out/harbor_core

FROM --platform=$BUILDPLATFORM golang:1.25.9@sha256:8a7adc288b77e9b787cd2695029eb54d10ae80571b21d44fed68d067ad0a9c96 AS jobservice-bin
ARG GOPROXY
ARG TARGETOS
ARG TARGETARCH
COPY --from=source /harbor/src /harbor/src
WORKDIR /harbor/src/jobservice
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -buildvcs=false -o /out/harbor_jobservice

FROM --platform=$BUILDPLATFORM golang:1.25.9@sha256:8a7adc288b77e9b787cd2695029eb54d10ae80571b21d44fed68d067ad0a9c96 AS registryctl-bin
ARG GOPROXY
ARG TARGETOS
ARG TARGETARCH
COPY --from=source /harbor/src /harbor/src
WORKDIR /harbor/src/registryctl
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -buildvcs=false -o /out/harbor_registryctl

FROM --platform=$BUILDPLATFORM golang:1.25.9@sha256:8a7adc288b77e9b787cd2695029eb54d10ae80571b21d44fed68d067ad0a9c96 AS exporter-bin
ARG GOPROXY
ARG VERSION
ARG COMMIT
ARG TARGETOS
ARG TARGETARCH
COPY --from=source /harbor/src /harbor/src
WORKDIR /harbor/src/cmd/exporter
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -buildvcs=false \
      -ldflags "-w -s -X github.com/goharbor/harbor/src/pkg/version.ReleaseVersion=${VERSION} -X github.com/goharbor/harbor/src/pkg/version.GitCommit=$(echo ${COMMIT} | cut -c1-8)" \
      -o /out/harbor_exporter

# Harbor's fork of distribution predates Go modules: it builds in GOPATH mode
# against its vendor/ tree, as upstream's make/photon/registry/Dockerfile.binary.
FROM --platform=$BUILDPLATFORM golang:1.25.9@sha256:8a7adc288b77e9b787cd2695029eb54d10ae80571b21d44fed68d067ad0a9c96 AS registry-bin
ARG GOPROXY
ARG DISTRIBUTION_VERSION
ARG DISTRIBUTION_COMMIT
ARG TARGETOS
ARG TARGETARCH
ENV SRC=/go/src/github.com/docker/distribution
RUN git clone --branch ${DISTRIBUTION_VERSION} --depth 1 https://github.com/goharbor/distribution ${SRC} \
    && test "$(git -C ${SRC} rev-parse HEAD)" = "${DISTRIBUTION_COMMIT}"
WORKDIR ${SRC}
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GO111MODULE=off GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -tags "include_oss include_gcs" \
      -ldflags "-s -w -X github.com/docker/distribution/version.Version=${DISTRIBUTION_VERSION} -X github.com/docker/distribution/version.Revision=${DISTRIBUTION_COMMIT} -X github.com/docker/distribution/version.Package=github.com/docker/distribution" \
      -o /out/registry ./cmd/registry

FROM --platform=$BUILDPLATFORM golang:1.25.9@sha256:8a7adc288b77e9b787cd2695029eb54d10ae80571b21d44fed68d067ad0a9c96 AS trivy-bin
ARG GOPROXY
ARG TRIVY_ADAPTER_VERSION
ARG TRIVY_ADAPTER_COMMIT
ARG TRIVY_VERSION
ARG TARGETOS
ARG TARGETARCH
RUN git clone --branch ${TRIVY_ADAPTER_VERSION} --depth 1 https://github.com/goharbor/harbor-scanner-trivy /src \
    && test "$(git -C /src rev-parse HEAD)" = "${TRIVY_ADAPTER_COMMIT}"
WORKDIR /src
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /out/scanner-trivy ./cmd/scanner-trivy
# Checksums from the release's trivy_<version>_checksums.txt.
RUN case "${TARGETARCH}" in \
      amd64) arch=64bit sum=8b4376d5d6befe5c24d503f10ff136d9e0c49f9127a4279fd110b727929a5aa9 ;; \
      arm64) arch=ARM64 sum=2f6bb988b553a1bbac6bdd1ce890f5e412439564e17522b88a4541b4f364fc8d ;; \
      *) echo "unsupported TARGETARCH ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
    && curl -sSfL -o /tmp/trivy.tgz https://github.com/aquasecurity/trivy/releases/download/${TRIVY_VERSION}/trivy_${TRIVY_VERSION#v}_Linux-${arch}.tar.gz \
    && echo "${sum}  /tmp/trivy.tgz" | sha256sum -c - \
    && tar -xzf /tmp/trivy.tgz -C /out trivy

FROM goharbor/photon:5.0@sha256:0e06d58615400bf2a3fe7da1175b889e2827969e3ac42e1c4010010770e6a712 AS harbor-base
RUN tdnf install -y tzdata shadow >> /dev/null \
    && tdnf clean all \
    && groupadd -r -g 10000 harbor && useradd --no-log-init -r -m -g 10000 -u 10000 harbor \
    && mkdir -p /harbor /etc/registry \
    && chown -R harbor:harbor /harbor /etc/pki/tls/certs

FROM harbor-base AS harbor-core
COPY --from=source --chown=harbor:harbor --chmod=755 /harbor/make/photon/common/install_cert.sh /harbor/make/photon/core/entrypoint.sh /harbor/
COPY --from=core-bin --chown=harbor:harbor /out/harbor_core /harbor/
COPY --from=source --chown=harbor:harbor /harbor/src/core/views /harbor/views
COPY --from=source --chown=harbor:harbor /harbor/make/migrations /harbor/migrations
COPY --from=source --chown=harbor:harbor /harbor/icons /harbor/icons
COPY --from=source --chown=harbor:harbor /harbor/make/photon/prepare/versions /harbor/
WORKDIR /harbor/
USER harbor
ENTRYPOINT ["/harbor/entrypoint.sh"]

FROM harbor-base AS harbor-jobservice
COPY --from=source --chown=harbor:harbor --chmod=755 /harbor/make/photon/common/install_cert.sh /harbor/make/photon/jobservice/entrypoint.sh /harbor/
COPY --from=jobservice-bin --chown=harbor:harbor /out/harbor_jobservice /harbor/
WORKDIR /harbor/
USER harbor
VOLUME ["/var/log/jobs/"]
ENTRYPOINT ["/harbor/entrypoint.sh"]

FROM harbor-base AS harbor-exporter
COPY --from=source --chown=harbor:harbor --chmod=755 /harbor/make/photon/common/install_cert.sh /harbor/make/photon/exporter/entrypoint.sh /harbor/
COPY --from=exporter-bin --chown=harbor:harbor /out/harbor_exporter /harbor/
WORKDIR /harbor
USER harbor
ENTRYPOINT ["/harbor/entrypoint.sh"]

FROM harbor-base AS registry-photon
COPY --from=source --chown=harbor:harbor --chmod=755 /harbor/make/photon/common/install_cert.sh /harbor/make/photon/registry/entrypoint.sh /home/harbor/
COPY --from=registry-bin --chown=harbor:harbor /out/registry /usr/bin/registry_DO_NOT_USE_GC
USER harbor
VOLUME ["/storage"]
ENTRYPOINT ["/home/harbor/entrypoint.sh"]

FROM harbor-base AS harbor-registryctl
COPY --from=source --chown=harbor:harbor --chmod=755 /harbor/make/photon/common/install_cert.sh /harbor/make/photon/registryctl/start.sh /home/harbor/
COPY --from=registryctl-bin --chown=harbor:harbor /out/harbor_registryctl /home/harbor/
COPY --from=registry-bin --chown=harbor:harbor /out/registry /usr/bin/registry_DO_NOT_USE_GC
USER harbor
VOLUME ["/var/lib/registry"]
ENTRYPOINT ["/home/harbor/start.sh"]

FROM goharbor/photon:5.0@sha256:0e06d58615400bf2a3fe7da1175b889e2827969e3ac42e1c4010010770e6a712 AS trivy-adapter-photon
ARG TRIVY_VERSION
RUN tdnf install -y rpm shadow >> /dev/null \
    && tdnf clean all \
    && groupadd -r -g 10000 scanner && useradd --no-log-init -m -r -g 10000 -u 10000 scanner \
    && chown -R scanner:scanner /etc/pki/tls/certs
COPY --from=source --chown=scanner:scanner --chmod=755 /harbor/make/photon/common/install_cert.sh /harbor/make/photon/trivy-adapter/entrypoint.sh /home/scanner/
COPY --from=trivy-bin --chown=scanner:scanner /out/trivy /usr/local/bin/trivy
COPY --from=trivy-bin --chown=scanner:scanner /out/scanner-trivy /home/scanner/bin/scanner-trivy
ENV TRIVY_VERSION=${TRIVY_VERSION}
USER scanner
ENTRYPOINT ["/home/scanner/entrypoint.sh"]

FROM goharbor/photon:5.0@sha256:0e06d58615400bf2a3fe7da1175b889e2827969e3ac42e1c4010010770e6a712 AS nginx-photon
RUN tdnf install -y nginx shadow >> /dev/null \
    && tdnf clean all \
    && groupmod -g 10000 nginx && usermod -g 10000 -u 10000 -d /home/nginx -s /bin/bash nginx \
    && ln -sf /dev/stdout /var/log/nginx/access.log \
    && ln -sf /dev/stderr /var/log/nginx/error.log
VOLUME /var/cache/nginx /var/log/nginx /run
STOPSIGNAL SIGQUIT
USER nginx
CMD ["nginx", "-g", "daemon off;"]
