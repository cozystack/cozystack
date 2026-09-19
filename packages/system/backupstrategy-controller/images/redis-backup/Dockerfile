# redis-backup bundles the tools the Redis/Valkey strategy Pod needs and that no
# stock image carries together: redis-server + redis-cli (dump on backup, and on
# restore a throwaway server that loads the RDB so a real Redis emits the replay
# stream), and curl for the SigV4 S3 transport. The spotahome operator ships no
# backup CRD, so the strategy runs this image as a one-shot Job.
#
# The base is redis 8.x, not `apk add redis` (Alpine ships 7.2, which cannot load
# an RDB written by the redis 8.4 / valkey 8 apps — "Can't handle RDB format
# version 12"). Restore fidelity depends on the loader being at least as new as
# the source engine.
FROM redis:8-alpine@sha256:bd999b5cfee25fb24b8320a31fddbd69f462df44c8138c66e369582937beebc0

RUN apk add --no-cache curl ca-certificates

USER 65532:65532
