# Container disk for CDI: the AuroraBoot raw disk of each architecture,
# converted to qcow2. The build context holds <arch>/*.raw.
FROM --platform=$BUILDPLATFORM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 AS convert
ARG TARGETARCH
RUN apk add --no-cache qemu-img
COPY ${TARGETARCH}/ /raw/
RUN set -- /raw/*.raw && test "$#" -eq 1 && test -f "$1" \
    && qemu-img convert -c -O qcow2 "$1" /disk.qcow2

FROM scratch
COPY --from=convert --chown=107:107 /disk.qcow2 /disk/
