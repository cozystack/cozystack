# backup-http-client runs the Altinity (ClickHouse) and RabbitMQ strategy Pods:
# both scripts only drive an HTTP API and S3 with curl, parse JSON with jq, and
# verify TLS against the bundled ca-certificates. Everything is installed at
# build time so the Pods never run `apk add` and work on air-gapped clusters.
FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6

RUN apk add --no-cache curl jq ca-certificates

USER 65532:65532
