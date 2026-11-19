# Upstream publishes quay.io/outline/shadowbox for amd64 only, although its
# own Taskfile already knows how to target arm64. This rebuilds the same
# release as a multi-arch image; the layout follows upstream's
# src/shadowbox/Taskfile.yml `docker:build`.
ARG VERSION=v1.12.3
ARG COMMIT=067d0d47aedac94a3888a127fa6c65f2f53b9987

FROM --platform=$BUILDPLATFORM node:18.20.3-alpine3.18@sha256:8863523fed890ce945343aebf959daa56e6b089de1851074f4fe22fe86c04399 AS source
ARG VERSION
ARG COMMIT
RUN apk add --no-cache git
RUN git clone --branch server-${VERSION} --depth 1 https://github.com/OutlineFoundation/outline-server /src \
    && test "$(git -C /src rev-parse HEAD)" = "${COMMIT}"

# main.js is a single webpack bundle with no native addons, so it is built
# once on the build platform and shared by every target.
FROM source AS app
ARG VERSION
WORKDIR /src
RUN npm ci --ignore-scripts
RUN SB_VERSION=${VERSION} npx webpack --config=src/shadowbox/webpack.config.js \
      --output-path=/out/opt/outline-server/app --mode=production \
    && cp src/shadowbox/package.json /out/opt/outline-server/

FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d AS ss-server
ARG GOPROXY
ARG TARGETOS
ARG TARGETARCH
COPY --from=source /src /src
WORKDIR /src
RUN for attempt in 1 2 3; do go mod download && break; [ "$attempt" = 3 ] && exit 1; sleep $((attempt * 10)); done
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} CGO_ENABLED=0 go build -trimpath \
      -ldflags='-s -w -X main.version=embedded' \
      -o /out/opt/outline-server/bin/ github.com/Jigsaw-Code/outline-ss-server/cmd/outline-ss-server

# Version and checksums are upstream's, from third_party/Taskfile.yml.
FROM --platform=$BUILDPLATFORM node:18.20.3-alpine3.18@sha256:8863523fed890ce945343aebf959daa56e6b089de1851074f4fe22fe86c04399 AS prometheus
ARG TARGETARCH
ARG PROMETHEUS_VERSION=2.37.1
RUN case "${TARGETARCH}" in \
      amd64) sum=753f66437597cf52ada98c2f459aa8c03745475c249c9f2b40ac7b3919131ba6 ;; \
      arm64) sum=b59a66fb5c7ec5acf6bf426793528a5789a1478a0dad8c64edc2843caf31b1b8 ;; \
      *) echo "unsupported TARGETARCH ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
    && name=prometheus-${PROMETHEUS_VERSION}.linux-${TARGETARCH} \
    && wget -q -O /tmp/p.tgz https://github.com/prometheus/prometheus/releases/download/v${PROMETHEUS_VERSION}/${name}.tar.gz \
    && echo "${sum}  /tmp/p.tgz" | sha256sum -c - \
    && mkdir -p /out/opt/outline-server/bin \
    && tar -xzf /tmp/p.tgz -C /out/opt/outline-server/bin --strip-components=1 ${name}/prometheus

FROM node:18.20.3-alpine3.18@sha256:8863523fed890ce945343aebf959daa56e6b089de1851074f4fe22fe86c04399
ARG VERSION
LABEL shadowbox.github.release=${VERSION}
# The user management service doesn't quit with SIGTERM.
STOPSIGNAL SIGKILL
# curl detects the public IP; coreutils gives update_mmdb.sh a `date --date`.
RUN apk add --no-cache --upgrade coreutils curl
COPY --from=app /out /
COPY --from=ss-server /out /
COPY --from=prometheus /out /
COPY --from=source /src/src/shadowbox/scripts /scripts
COPY --from=source /src/src/shadowbox/scripts/update_mmdb.sh /etc/periodic/weekly/update_mmdb.sh
COPY --from=source /src/src/shadowbox/docker/cmd.sh /cmd.sh
RUN mkdir -p /root/shadowbox/persisted-state && /etc/periodic/weekly/update_mmdb.sh
WORKDIR /opt/outline-server
CMD ["/cmd.sh"]
