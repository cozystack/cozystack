# Shared by the script steps of this suite, whose cwd is this directory.
# Every call carries its own cap so a hang fails at a known budget rather
# than at the op ceiling (docs/agents/e2e-testing.md, section 8).

k() { timeout 30 kubectl "$@"; }

STORE=http://localhost:19471
CONSOLE=http://localhost:18080
# shellcheck disable=SC2034 # read by the steps that source this file
LOGS_A=/apis/cozystack.io/v1alpha1/namespaces/tenant-test-logsa/logs/select/logsql
# shellcheck disable=SC2034
LOGS_B=/apis/cozystack.io/v1alpha1/namespaces/tenant-test-logsb/logs/select/logsql

# forward <namespace> <service> <local>:<remote>. The caller's EXIT trap
# kills every pid in pf_pids; the forward gets 30s to start listening.
forward() {
  kubectl port-forward -n "$1" "service/$2" "$3" >/dev/null 2>&1 &
  pf_pids="${pf_pids:-} $!"
  timeout 30 sh -ec "until nc -z localhost ${3%%:*}; do sleep 1; done"
}

# ingest <namespace> <message> [account]: one entry shaped as Fluent Bit
# writes it, with kubernetes_namespace_name as a stream field.
ingest() {
  printf '{"kubernetes_namespace_name":"%s","log":"%s"}\n' "$1" "$2" |
    timeout 15 curl --silent --show-error --fail --data-binary @- \
      --header "AccountID: ${3:-0}" --header "ProjectID: 0" \
      "$STORE/insert/jsonline?_stream_fields=kubernetes_namespace_name&_msg_field=log"
}

# call <path> [curl args...]: a request through the console nginx, the way
# the browser sends it. Prints the status; the body lands in $body.
body=$(mktemp)
call() {
  path=$1
  shift
  timeout 30 curl --silent --show-error --output "$body" --write-out '%{http_code}' "$@" "$CONSOLE$path"
}

# expect <want-status> <got-status> <case>
expect() {
  if [ "$1" != "$2" ]; then
    echo "FAIL: $3: HTTP $2, want $1" >&2
    cat "$body" >&2
    exit 1
  fi
  echo "ok: $3: HTTP $2"
}

# absent <marker> <case>: the last body must not carry the marker.
absent() {
  if grep -q "$1" "$body"; then
    echo "FAIL: $2: the response carries $1, which is outside the scope" >&2
    cat "$body" >&2
    exit 1
  fi
}
