#!/usr/bin/env bats
# The console nginx sends the logs and metrics roots of the observability
# proxy to that proxy instead of the API server. The routing lives in the
# nginx config baked into the console image, which no chart template and no
# helm-unittest case can see, so it is pinned here.
#
# Three properties are checked. The location regex must take the four path
# shapes the proxy serves and leave every ordinary Kubernetes API path to the
# /apis and /api prefix locations; nginx runs the regex locations whenever the
# longest prefix match is a plain prefix, so the prefix locations must not
# carry ^~, which would stop that. The location must stream: tail and watch
# style responses stall behind proxy buffering and die at the default 60s
# read timeout. And the upstream must be a variable resolved per request,
# because nginx resolves a literal upstream host once at startup and refuses
# to start when it does not resolve, which is the case on every cluster that
# does not run the proxy.
#
# The regex is evaluated with perl, whose syntax nginx's PCRE accepts for the
# constructs used here.
# Run with: bats hack/console-nginx-observability-proxy.bats

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
CONSOLE_CONTAINERFILE="packages/system/dashboard/images/console/Containerfile"

console_nginx_template() {
    awk '
        /^COPY <<.EOF. \/etc\/nginx\/templates\/default\.conf\.template$/ { inblock = 1; next }
        inblock && /^EOF$/ { inblock = 0; next }
        inblock { print }
    ' "$REPO_ROOT/$CONSOLE_CONTAINERFILE"
}

# Prints the regex of every `location ~ <regex> {` line, one per line.
regex_locations() {
    printf '%s\n' "$1" | awk '$1 == "location" && $2 == "~" && $4 == "{" { print $3 }'
}

# Prints the body of the location block whose regex is $2.
location_body() {
    # Through ENVIRON rather than -v, which would expand the backslashes.
    printf '%s\n' "$1" | LOCATION_RE="$2" awk '
        $1 == "location" && $2 == "~" && $3 == ENVIRON["LOCATION_RE"] { inblock = 1; next }
        inblock && $1 == "}" { exit }
        inblock { print }
    '
}

matches() {
    perl -e 'exit(($ARGV[1] =~ /$ARGV[0]/) ? 0 : 1)' "$1" "$2"
}

@test "console nginx routes exactly the observability proxy roots to the proxy" {
    conf=$(console_nginx_template)
    [ -n "$conf" ] || { echo "no /etc/nginx/templates/default.conf.template heredoc in $CONSOLE_CONTAINERFILE" >&2; exit 1; }

    regexes=$(regex_locations "$conf")
    [ "$(printf '%s\n' "$regexes" | grep -c .)" -eq 1 ] \
        || { echo "expected exactly one regex location, got: $regexes" >&2; exit 1; }
    re=$regexes

    for path in \
        /apis/cozystack.io/v1alpha1/namespaces/tenant-foo/logs/select/logsql/query \
        /apis/cozystack.io/v1alpha1/namespaces/tenant-foo/metrics/api/v1/query_range \
        /apis/apps.cozystack.io/v1alpha1/namespaces/tenant-foo/postgreses/db/log/select/logsql/tail \
        /apis/apps.cozystack.io/v1alpha1/namespaces/tenant-foo/vminstances/vm1/metrics/api/v1/query
    do
        matches "$re" "$path" || { echo "$path does not reach the proxy" >&2; exit 1; }
    done

    for path in \
        /apis/cozystack.io/v1alpha1/namespaces/tenant-foo/logs \
        /apis/cozystack.io/v1alpha1/namespaces/tenant-foo/workloadmonitors/db \
        /apis/cozystack.io/v1alpha1/applicationdefinitions/postgres \
        /apis/cozystack.io/v1alpha1/watch/namespaces/tenant-foo/logs/ \
        /apis/apps.cozystack.io/v1alpha1/namespaces/tenant-foo/postgreses \
        /apis/apps.cozystack.io/v1alpha1/namespaces/tenant-foo/postgreses/log \
        /apis/apps.cozystack.io/v1alpha1/namespaces/tenant-foo/postgreses/log/ \
        /apis/apps.cozystack.io/v1alpha1/namespaces/tenant-foo/postgreses/db/status \
        /apis/apps.cozystack.io/v1alpha1/namespaces/tenant-foo/postgreses/db/log \
        /apis/apps.cozystack.io/v1alpha1/watch/namespaces/tenant-foo/postgreses/db/log/ \
        /apis/metrics.k8s.io/v1beta1/namespaces/tenant-foo/pods/ \
        /api/v1/namespaces/tenant-foo/pods/db-0/log \
        /k8s/apis/cozystack.io/v1alpha1/namespaces/tenant-foo/logs/select/logsql/query
    do
        if matches "$re" "$path"; then
            echo "FAIL: $path would reach the proxy instead of the API server" >&2
            false
        fi
    done

    # A ^~ on the /apis or /api prefix would stop nginx from trying the regex.
    if printf '%s\n' "$conf" | grep -Eq '^[[:space:]]*location[[:space:]]+\^~'; then
        echo "FAIL: a ^~ prefix location disables the regex location" >&2
        false
    fi
}

@test "the observability proxy location streams, forwards the token and resolves per request" {
    conf=$(console_nginx_template)
    re=$(regex_locations "$conf")
    body=$(location_body "$conf" "$re")
    [ -n "$body" ] || { echo "no body found for location ~ $re" >&2; exit 1; }

    for directive in \
        'proxy_http_version 1.1;' \
        'proxy_buffering off;' \
        'proxy_read_timeout 86400s;' \
        'proxy_send_timeout 86400s;'
    do
        printf '%s\n' "$body" | grep -Fqx "        $directive" \
            || { echo "the proxy location lacks '$directive'" >&2; exit 1; }
    done

    # nginx forwards client headers unless told otherwise; the proxy needs the
    # Authorization header the gatekeeper set.
    if printf '%s\n' "$body" | grep -Eq 'proxy_set_header[[:space:]]+Authorization|proxy_pass_request_headers[[:space:]]+off'; then
        echo "FAIL: the proxy location drops or rewrites the Authorization header" >&2
        false
    fi

    printf '%s\n' "$body" | grep -Eq '^[[:space:]]*resolver[[:space:]]+\$\{NGINX_LOCAL_RESOLVERS\}' \
        || { echo "the proxy location sets no resolver from the pod's resolv.conf" >&2; exit 1; }
    printf '%s\n' "$body" | grep -Eq '^[[:space:]]*set[[:space:]]+\$observability_proxy[[:space:]]+observability-proxy\.cozy-observability-proxy\.svc\.\$\{CLUSTER_DOMAIN\}:80;' \
        || { echo "the proxy location does not name the Service by its fully qualified name" >&2; exit 1; }
    printf '%s\n' "$body" | grep -Eq '^[[:space:]]*proxy_pass[[:space:]]+http://\$observability_proxy;' \
        || { echo "the proxy location does not proxy_pass through the variable" >&2; exit 1; }
}

@test "the console image defines every variable its nginx template substitutes" {
    cd "$REPO_ROOT"
    conf=$(console_nginx_template)

    # The image's entrypoint substitutes only variables present in the
    # environment; one that is not would reach nginx as ${NAME} and fail it.
    grep -Eq '^ENV NGINX_ENTRYPOINT_LOCAL_RESOLVERS=1$' "$CONSOLE_CONTAINERFILE" \
        || { echo "the image does not ask its entrypoint for NGINX_LOCAL_RESOLVERS" >&2; exit 1; }
    grep -Eq '^ENV CLUSTER_DOMAIN=cozy\.local$' "$CONSOLE_CONTAINERFILE" \
        || { echo "the image has no CLUSTER_DOMAIN default" >&2; exit 1; }

    used=$(printf '%s\n' "$conf" | grep -oE '\$\{[A-Za-z_][A-Za-z0-9_]*\}' | sort -u)
    expected=$(printf '%s\n' '${CLUSTER_DOMAIN}' '${NGINX_LOCAL_RESOLVERS}')
    [ "$used" = "$expected" ] \
        || { echo "template substitutes: $used; the image defines: $expected" >&2; exit 1; }
}
