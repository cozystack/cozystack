#!/usr/bin/env bats
# Asserts that the Grafana plugin set is identical on both sides of the
# in-cluster mirror: the PRODUCER that bakes the archives into the
# grafana-dashboards image, and the CONSUMER that installs them at Grafana
# startup.
#
#   producer: packages/system/grafana-operator/images/grafana-dashboards/Dockerfile
#             (a `for p in <id>:<ver> ...` loop that curls each plugin into
#             /plugins/<id>-<ver>.zip)
#   consumer: packages/system/monitoring/templates/grafana/grafana.yaml
#             (a `$plugins := dict "<id>" "<ver>" ...` that renders
#             GF_INSTALL_PLUGINS pointing at .../plugins/<id>-<ver>.zip)
#
# The two lists are hand-maintained in different files and different languages,
# so they drift silently. When they do, Grafana asks the mirror for an archive
# name the image never built, the mirror answers 404, and Grafana crashloops on
# startup — which is exactly how #4391 reached production charts (the image
# predated the plugins the chart now asks for). E2E cannot be relied on to catch
# it: a stacked PR overlays main's freshly built image, so a stale committed
# image or a drifted list passes there and only surfaces on a real install.
# This holds the two lists together at PR time, offline.
#
# Harness note: the CI path is hack/cozytest.sh, NOT real bats. There is no
# `run`, `$status`, `$output`, `skip`, or setup()/teardown(); each test runs as
# a shell function under `set -eu -x`, so a non-zero exit is the failure. Paths
# are repo-root-relative: BATS_TEST_DIRNAME is unset and would abort the whole
# suite under `set -u`.
#
# Run with: hack/cozytest.sh hack/grafana-dashboards-plugin-parity.bats

DOCKERFILE=packages/system/grafana-operator/images/grafana-dashboards/Dockerfile
GRAFANA=packages/system/monitoring/templates/grafana/grafana.yaml

# Producer: the <id>:<ver> tokens inside the `for p in ... ; do` loop, one
# "<id> <ver>" per line, sorted. Scoped to the loop body so an unrelated
# `name:tag` elsewhere in the Dockerfile (a FROM, a digest) cannot leak in.
mbw_producer_plugins() {
    awk '
        /for p in/      { inblock = 1; next }
        inblock && /;[[:space:]]*do/ { inblock = 0 }
        inblock         { print }
    ' "$DOCKERFILE" \
    | grep -oE '[a-z][a-z0-9-]*:[0-9]+(\.[0-9]+)*' \
    | sed 's/:/ /' \
    | sort
}

# Consumer: the "<id>" "<ver>" pairs inside the `$plugins := dict ... }}` block
# in grafana.yaml, one "<id> <ver>" per line, sorted.
mbw_consumer_plugins() {
    awk '
        /\$plugins[[:space:]]*:=[[:space:]]*dict/ { inblock = 1; next }
        inblock && /}}/ { inblock = 0 }
        inblock         { print }
    ' "$GRAFANA" \
    | grep -oE '"[a-z][a-z0-9-]*"[[:space:]]+"[0-9]+(\.[0-9]+)*"' \
    | tr -d '"' \
    | awk '{ print $1, $2 }' \
    | sort
}

@test "the mirror builds exactly the plugins the Grafana chart installs" {
    [ -f "$DOCKERFILE" ] || { echo "$DOCKERFILE not found" >&2; exit 1; }
    [ -f "$GRAFANA" ] || { echo "$GRAFANA not found" >&2; exit 1; }

    producer=$(mbw_producer_plugins)
    consumer=$(mbw_consumer_plugins)

    # Guard against a parser that silently matched nothing: a passing test on
    # two empty lists would be worse than no test.
    [ -n "$producer" ] || { echo "parsed no plugins from the producer $DOCKERFILE; the parser or the file shape changed" >&2; exit 1; }
    [ -n "$consumer" ] || { echo "parsed no plugins from the consumer $GRAFANA; the parser or the file shape changed" >&2; exit 1; }

    if [ "$producer" != "$consumer" ]; then
        pf=$(mktemp)
        cf=$(mktemp)
        printf '%s\n' "$producer" > "$pf"
        printf '%s\n' "$consumer" > "$cf"
        echo "Grafana plugin producer and consumer disagree." >&2
        echo "The image builds /plugins/<id>-<ver>.zip for one set; the chart's" >&2
        echo "GF_INSTALL_PLUGINS asks the mirror for another. Whatever only one" >&2
        echo "side lists 404s at Grafana startup and crashloops the pod." >&2
        echo >&2
        echo "producer ($DOCKERFILE):" >&2
        printf '  %s\n' "$producer" >&2
        echo "consumer ($GRAFANA):" >&2
        printf '  %s\n' "$consumer" >&2
        echo >&2
        echo "'<' only in producer, '>' only in consumer:" >&2
        diff "$pf" "$cf" >&2 || true
        rm -f "$pf" "$cf"
        exit 1
    fi
}
