#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Contract: a Chainsaw script op whose script carries its own waits gives those
# waits room to fire first. When the op ceiling fires instead, Chainsaw kills
# the shell, the script's own failure message never prints, and a cleanup trap
# never runs (.chainsaw.yaml states the rule above its catch scripts).
#
# The gateway step is held by arithmetic: its polls are `timeout N` prefixes
# and its cleanup deletes run under `timeout -k K N`, so their sum, kill
# grace included, is readable from the text. kubectl's own `--timeout` is not
# counted: it bounds only the wait after the delete request, not discovery or
# the request itself. Only those two spellings are summed: a wait written any
# other way, a `kubectl wait` or a sleep loop, is invisible to the sum. The
# redis-tls step cannot be summed that way, because some of its
# reads sit in loops and one loop polls, so it carries a step budget that
# every kubectl call is clamped to, and the guard holds that budget under the
# op. Its failure messages are checked by running the step's script against a
# stubbed kubectl.
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` on its
# own line; a `}` at column zero gets `return 0` inserted ahead of it, so the
# helpers below report through stdout only.
#
# Run with: hack/cozytest.sh hack/chainsaw-op-budgets.bats
# -----------------------------------------------------------------------------

GATEWAY_SUITE="hack/e2e-chainsaw/gateway/chainsaw-test.yaml"
GATEWAY_STEP="parent-child-and-route-drive-listener-set"
REDIS_SUITE="hack/e2e-chainsaw/redis/chainsaw-test.yaml"
REDIS_STEP="verify-tenant-ca-projection"

# Seconds the gateway op keeps above its summed waits for what carries no cap
# of its own: three applies and one label read.
GATEWAY_UNCAPPED=60
# Seconds the redis op keeps above its step budget: the kill grace on the last
# clamped read, one 5s poll sleep, and the openssl and shell work between reads.
REDIS_AFTER_BUDGET=30

# Print the lines of the first `- script:` op inside step $2 of file $1, with
# the op's own indentation, from `- script:` up to the next op or step.
op_lines() {
  awk -v step="$2" '
    $0 ~ "^[[:space:]]*- name: " step "[[:space:]]*$" { instep = 1; next }
    instep && !inop && /^[[:space:]]*- script:[[:space:]]*$/ {
      match($0, /^[[:space:]]*/); opindent = RLENGTH; inop = 1; print; next
    }
    inop && /[^[:space:]]/ {
      match($0, /^[[:space:]]*/)
      if (RLENGTH <= opindent) exit
    }
    inop { print }
    instep && /^---/ { exit }
  ' "$1"
}

# Print the op timeout in seconds of the op in $1 (op_lines output), or
# nothing when it states none in minutes or seconds.
op_timeout_seconds() {
  printf '%s\n' "$1" | awk '
    /^[[:space:]]*timeout:[[:space:]]*[0-9]+m[[:space:]]*$/ { match($0, /[0-9]+/); print substr($0, RSTART, RLENGTH) * 60; exit }
    /^[[:space:]]*timeout:[[:space:]]*[0-9]+s[[:space:]]*$/ { match($0, /[0-9]+/); print substr($0, RSTART, RLENGTH); exit }
    /^[[:space:]]*content:/ { exit }
  '
}

# Print the op's shell, or `sh`, the Chainsaw default, when it names none.
op_shell() {
  sh_name=$(printf '%s\n' "$1" | awk '
    /^[[:space:]]*shell:[[:space:]]*[^[:space:]]+[[:space:]]*$/ { sub(/^[[:space:]]*shell:[[:space:]]*/, ""); sub(/[[:space:]]*$/, ""); print; exit }
    /^[[:space:]]*content:/ { exit }
  ')
  printf '%s\n' "${sh_name:-sh}"
}

# Print the op's script, dedented, without the content key.
op_content() {
  printf '%s\n' "$1" | awk '
    !incontent && /^[[:space:]]*content:[[:space:]]*\|[[:space:]]*$/ { match($0, /^[[:space:]]*/); keyindent = RLENGTH; incontent = 1; next }
    incontent && /[^[:space:]]/ {
      match($0, /^[[:space:]]*/)
      if (RLENGTH <= keyindent) exit
      if (!bodyindent) bodyindent = RLENGTH
    }
    incontent { print substr($0, bodyindent + 1) }
  '
}

# Write a kubectl stub into directory $1 that answers the redis-tls step's
# reads as a healthy cluster would. STUB_FAIL, when set, is a pattern matched
# against the arguments: a matching call prints STUB_ERR to stderr and exits
# STUB_RC instead, after printing STUB_OUT to stdout when that is set, from the
# STUB_FAIL_FROM-th matching call on (default the first). STUB_PROJ_KEY=1 puts
# PEM private key material into the projection. STUB_SLEEP_ON is a pattern
# whose matching calls sleep STUB_SLEEP seconds first; the stub execs sleep,
# so a timeout that kills it leaves no child holding the output pipe.
write_redis_stubs() {
  cat > "$1/kubectl" <<'STUB'
#!/bin/sh
args="$*"
if [ -n "${STUB_SLEEP_ON:-}" ]; then
  case "$args" in
    $STUB_SLEEP_ON) exec sleep "${STUB_SLEEP:-60}" ;;
  esac
fi
if [ -n "${STUB_FAIL:-}" ]; then
  case "$args" in
    $STUB_FAIL)
      echo x >> "$(dirname "$0")/fail-matches"
      if [ "$(wc -l < "$(dirname "$0")/fail-matches")" -ge "${STUB_FAIL_FROM:-1}" ]; then
        [ -z "${STUB_OUT:-}" ] || echo "$STUB_OUT"
        echo "${STUB_ERR:-error: stubbed failure}" >&2; exit "${STUB_RC:-1}"
      fi ;;
  esac
fi
cert_b64=$(printf 'CERT' | base64)
proj_b64=$cert_b64
if [ "${STUB_PROJ_KEY:-}" = 1 ]; then
  proj_b64=$(printf 'CERT\n-----BEGIN PRIVATE KEY-----\nKEY\n' | base64 | tr -d '\n')
fi
case "$args" in
  *"get secret redis-secure.tenant-ca -o go-template"*) printf 'ca.crt\n' ;;
  *"get secret redis-secure.ca-cert -o go-template"*) printf 'ca.crt\n' ;;
  *"get secret redis-secure.ca-tls -o go-template"*) printf 'ca.crt\ntls.crt\ntls.key\n' ;;
  *"get secret redis-secure.ca-tls -o jsonpath"*) printf '%s' "$cert_b64" ;;
  *"get secret redis-secure.tenant-ca -o jsonpath"*) printf '%s' "$proj_b64" ;;
  *"get tenantsecret redis-secure.tenant-ca -o jsonpath"*) printf '%s' "$(printf -- '-----BEGIN CERTIFICATE-----' | base64)" ;;
  *"get tenantsecret "*) echo "Error from server (NotFound): tenantsecrets \"x\" not found" >&2; exit 1 ;;
  *"exec rfr-redis-secure-0 -c redis -- cat /tls/ca.crt"*) printf 'CERT\n' ;;
  *"exec rfr-redis-secure-0 -c redis -- timeout 15 redis-cli --tls"*) printf 'PONG\n' ;;
  *"exec rfr-redis-secure-0 -c redis -- timeout 15 redis-cli -h"*) printf 'I/O error\n'; exit 1 ;;
  *"exec rfr-redis-secure-0 -c redis -- sh -c"*) printf 'role:master\n' ;;
  *"exec rfr-redis-secure-1 -c redis -- sh -c"*) printf 'role:slave\nmaster_link_status:up\n' ;;
  *"get pod -l app.kubernetes.io/component=sentinel"*) printf 'rfs-redis-secure-0' ;;
  *"exec rfs-redis-secure-0 -c sentinel"*) printf '10.0.0.1\n6379\n' ;;
  *) echo "kubectl stub: unexpected call: $args" >&2; exit 99 ;;
esac
STUB
  cat > "$1/openssl" <<'STUB'
#!/bin/sh
cat >/dev/null
echo "sha256 Fingerprint=AA"
STUB
  chmod +x "$1/kubectl" "$1/openssl"
}

# Run the redis-tls step's script, as its op's shell would, against the stubs
# in $1. Prints the script's combined output and then `rc=<status>`. RS_BUDGET,
# when set, replaces the step's budget so the clamp can be driven in seconds.
run_redis_step() {
  rs_op=$(op_lines "$REDIS_SUITE" "$REDIS_STEP")
  rs_shell=$(op_shell "$rs_op")
  op_content "$rs_op" > "$1/step.sh"
  if [ -n "${RS_BUDGET:-}" ]; then
    sed "s/^step_budget=[0-9]*\$/step_budget=${RS_BUDGET}/" "$1/step.sh" > "$1/step.budget"
    if cmp -s "$1/step.sh" "$1/step.budget"; then echo "no step_budget line to override"; echo "rc=97"; return; fi
    mv "$1/step.budget" "$1/step.sh"
  fi
  rs_rc=0
  rs_out=$(cd "$1" && PATH="$1:$PATH" NAMESPACE=tenant-test STUB_FAIL="${STUB_FAIL:-}" STUB_FAIL_FROM="${STUB_FAIL_FROM:-1}" STUB_RC="${STUB_RC:-1}" STUB_ERR="${STUB_ERR:-}" STUB_OUT="${STUB_OUT:-}" STUB_SLEEP_ON="${STUB_SLEEP_ON:-}" STUB_SLEEP="${STUB_SLEEP:-}" STUB_PROJ_KEY="${STUB_PROJ_KEY:-}" "$rs_shell" "$1/step.sh" 2>&1) || rs_rc=$?
  printf '%s\nrc=%s\n' "$rs_out" "$rs_rc"
}

@test "the gateway listener-set op outlasts the waits inside it" {
    gw_op=$(op_lines "$GATEWAY_SUITE" "$GATEWAY_STEP")
    [ -n "$gw_op" ] || { echo "no script op found in step $GATEWAY_STEP of $GATEWAY_SUITE" >&2; exit 1; }
    gw_timeout=$(op_timeout_seconds "$gw_op")
    case "$gw_timeout" in ''|*[!0-9]*) echo "the $GATEWAY_STEP op states no timeout in m or s (got '${gw_timeout}')" >&2; exit 1 ;; esac
    gw_body=$(op_content "$gw_op")
    gw_polls=$(printf '%s\n' "$gw_body" | grep -oE '^[[:space:]]*timeout +[0-9]+' | grep -oE '[0-9]+$' || true)
    gw_n=$(printf '%s\n' "$gw_polls" | grep -c . || true)
    [ "$gw_n" -ge 5 ] || { echo "found ${gw_n} capped polls in $GATEWAY_STEP, expected at least 5; the extractor is reading the wrong lines" >&2; exit 1; }
    gw_deletes=$(printf '%s\n' "$gw_body" | grep -E '(^|[[:space:]])kubectl .* delete ' | grep -oE '(^|[[:space:]])timeout -k [0-9]+ [0-9]+ kubectl ' | awk '{ print $3 + $4 }' || true)
    gw_sum=0
    for gw_w in $gw_polls $gw_deletes; do gw_sum=$(( gw_sum + gw_w )); done
    [ $(( gw_timeout - gw_sum )) -ge "$GATEWAY_UNCAPPED" ] || {
        echo "the $GATEWAY_STEP op allows ${gw_timeout}s; its polls ($(printf '%s ' $gw_polls)) and cleanup deletes ($(printf '%s ' $gw_deletes)) may wait ${gw_sum}s" >&2
        echo "it must keep ${GATEWAY_UNCAPPED}s above them for the uncapped applies and reads" >&2
        exit 1
    }
}

@test "every delete in the gateway listener-set cleanup is bounded as a whole" {
    # A tenant delete blocks until its child namespaces drain, and a stalled
    # API request blocks with no bound at all, so a delete not wrapped in an
    # outer timeout is a wait the op sum above cannot see.
    gw_op=$(op_lines "$GATEWAY_SUITE" "$GATEWAY_STEP")
    gw_deletes=$(op_content "$gw_op" | grep -E '(^|[[:space:]])kubectl .* delete ' || true)
    [ -n "$gw_deletes" ] || { echo "no kubectl delete found in $GATEWAY_STEP; the cleanup this test reasons about is gone" >&2; exit 1; }
    gw_bad=$(printf '%s\n' "$gw_deletes" | grep -vE '(^|[[:space:]])timeout -k [0-9]+ [0-9]+ kubectl ' || true)
    [ -z "$gw_bad" ] || { echo "deletes in $GATEWAY_STEP whose wait has no bound:" >&2; printf '%s\n' "$gw_bad" >&2; exit 1; }
}

@test "the gateway listener-set cleanup deletes the parent only once the child is gone" {
    # A parent deleted while its child is still uninstalling wedges the
    # parent's cleanup, so a child delete that ran into its bound must leave
    # the parent alone.
    gw_op=$(op_lines "$GATEWAY_SUITE" "$GATEWAY_STEP")
    gw_order=$(op_content "$gw_op" | awk '
      /^[[:space:]]*if timeout -k [0-9]+ [0-9]+ kubectl .* delete tenant rchild .*; then[[:space:]]*$/ { guarded = 1; next }
      guarded && /[^[:space:]]/ { print (/delete tenant rparent/ ? "ok" : "unguarded"); exit }
      /delete tenant rparent/ { print "unguarded"; exit }
    ')
    [ "$gw_order" = ok ] || { echo "the rparent delete in $GATEWAY_STEP does not sit directly under a successful rchild delete (got '${gw_order}')" >&2; exit 1; }
}

@test "a failed tenant delete in the gateway cleanup fails the step with its own error" {
    # A tenant left in place is one every later suite inherits, so
    # the step must say so and fail rather than pass with the leak.
    gw_dir=$(mktemp -d)
    op_content "$(op_lines "$GATEWAY_SUITE" "$GATEWAY_STEP")" | awk '/^cleanup\(\) \{/ { on = 1 } on { print } on && /^\}/ { exit }' > "$gw_dir/cleanup.sh"
    grep -q 'delete tenant rchild' "$gw_dir/cleanup.sh" || { echo "no cleanup() with the rchild delete found in $GATEWAY_STEP" >&2; exit 1; }
    # The stub fails the delete of the tenant named in STUB_FAIL_TENANT with a
    # Forbidden error, which is not the bound firing, and logs every delete.
    printf '%s\n' '#!/bin/sh' 'echo "$*" >> "$(dirname "$0")/calls"' 'case "$*" in *"delete tenant $STUB_FAIL_TENANT "*) echo "Error from server (Forbidden): stubbed" >&2; exit 3 ;; esac' > "$gw_dir/kubectl"
    chmod +x "$gw_dir/kubectl"
    for gw_tenant in rchild rparent; do
        : > "$gw_dir/calls"
        gw_rc=0
        gw_out=$(PATH="$gw_dir:$PATH" STUB_FAIL_TENANT="$gw_tenant" sh -c '. "$1/cleanup.sh"; ns=tenant-test; cleanup' sh "$gw_dir" 2>&1) || gw_rc=$?
        [ "$gw_rc" -ne 0 ] || { printf '%s\n' "$gw_out" >&2; echo "cleanup exited 0 with the $gw_tenant delete failed" >&2; exit 1; }
        case "$gw_out" in *"Forbidden"*"$gw_tenant delete exited 3"*"rparent is left in tenant-test"*) ;; *) printf '%s\n' "$gw_out" >&2; echo "with the $gw_tenant delete failed, the output does not carry kubectl's error, its exit status and the tenant left behind" >&2; exit 1 ;; esac
        if [ "$gw_tenant" = rchild ] && grep -q 'delete tenant rparent' "$gw_dir/calls"; then echo "the parent was deleted although the child delete failed" >&2; exit 1; fi
    done
    rm -rf "$gw_dir"
}

@test "the redis-tls projection op outlasts the step budget its reads are clamped to" {
    rt_op=$(op_lines "$REDIS_SUITE" "$REDIS_STEP")
    [ -n "$rt_op" ] || { echo "no script op found in step $REDIS_STEP of $REDIS_SUITE" >&2; exit 1; }
    rt_timeout=$(op_timeout_seconds "$rt_op")
    case "$rt_timeout" in ''|*[!0-9]*) echo "the $REDIS_STEP op states no timeout in m or s (got '${rt_timeout}')" >&2; exit 1 ;; esac
    rt_body=$(op_content "$rt_op")
    rt_budget=$(printf '%s\n' "$rt_body" | sed -n 's/^step_budget=\([0-9][0-9]*\)$/\1/p')
    [ -n "$rt_budget" ] || {
        echo "$REDIS_STEP sets no step_budget, so nothing bounds the sum of its reads below the ${rt_timeout}s op" >&2
        echo "its kubectl call sites alone come to $(printf '%s\n' "$rt_body" | grep -cE '(^|[^[:alnum:]_])k -n ')" >&2
        exit 1
    }
    [ $(( rt_timeout - rt_budget )) -ge "$REDIS_AFTER_BUDGET" ] || {
        echo "the $REDIS_STEP op allows ${rt_timeout}s and the step budget is ${rt_budget}s" >&2
        echo "it must keep ${REDIS_AFTER_BUDGET}s above the budget for the last read's kill grace and the work between reads" >&2
        exit 1
    }
    # The budget bounds only the reads that go through k(). A bare kubectl
    # outside it would add an uncapped read the arithmetic above never sees.
    rt_bare=$(printf '%s\n' "$rt_body" | grep -vE '^[[:space:]]*#' | grep -nE '(^|[^[:alnum:]_-])kubectl([[:space:]]|$)' | grep -v '"\$k_left" kubectl' || true)
    [ -z "$rt_bare" ] || { echo "kubectl calls in $REDIS_STEP that bypass the clamped k():" >&2; printf '%s\n' "$rt_bare" >&2; exit 1; }
}

@test "the redis-tls projection script passes against a healthy stub" {
    # The positive control for the two failure tests below: without it, a
    # stub that broke the script somewhere else would make them pass.
    rt_dir=$(mktemp -d)
    write_redis_stubs "$rt_dir"
    rt_out=$(run_redis_step "$rt_dir")
    case "$rt_out" in *"
rc=0") ;; *) printf '%s\n' "$rt_out" >&2; echo "the step failed against a healthy stub" >&2; exit 1 ;; esac
    rm -rf "$rt_dir"
}

@test "a failed replication exec reports its own error, not the 15s cap" {
    rt_dir=$(mktemp -d)
    write_redis_stubs "$rt_dir"
    rt_out=$(STUB_FAIL='*exec rfr-redis-secure-1 -c redis -- sh -c*' STUB_RC=1 \
      STUB_ERR='Error from server (Forbidden): pods "rfr-redis-secure-1" is forbidden' run_redis_step "$rt_dir")
    case "$rt_out" in *"
rc=0") printf '%s\n' "$rt_out" >&2; echo "the step passed with a forbidden exec" >&2; exit 1 ;; esac
    case "$rt_out" in *"15s cap"*) printf '%s\n' "$rt_out" >&2; echo "a forbidden exec was reported as the 15s cap firing" >&2; exit 1 ;; esac
    case "$rt_out" in *"rfr-redis-secure-1"*"exit 1"*"Forbidden"*) ;; *) printf '%s\n' "$rt_out" >&2; echo "the message does not carry the exit status and what the exec printed" >&2; exit 1 ;; esac
    rm -rf "$rt_dir"
}

@test "a failed sentinel exec reports its own error, not the 15s cap" {
    rt_dir=$(mktemp -d)
    write_redis_stubs "$rt_dir"
    rt_out=$(STUB_FAIL='*exec rfs-redis-secure-0 -c sentinel*' STUB_RC=1 \
      STUB_ERR='command terminated with exit code 1' STUB_OUT='NOAUTH Authentication required.' run_redis_step "$rt_dir")
    case "$rt_out" in *"
rc=0") printf '%s\n' "$rt_out" >&2; echo "the step passed with a failed sentinel exec" >&2; exit 1 ;; esac
    case "$rt_out" in *"15s cap"*) printf '%s\n' "$rt_out" >&2; echo "a refused sentinel command was reported as the 15s cap firing" >&2; exit 1 ;; esac
    case "$rt_out" in *"command terminated with exit code 1"*"rfs-redis-secure-0 failed (exit 1): NOAUTH Authentication required."*) ;; *) printf '%s\n' "$rt_out" >&2; echo "the output does not carry what the exec wrote to both streams and its exit status" >&2; exit 1 ;; esac
    rm -rf "$rt_dir"
}

@test "a failed Secret read is not reported as a wrong source name" {
    # base64 -d exits 0 on empty input, so without pipefail the pipeline takes
    # its status and the emptiness check below names the wrong cause.
    rt_dir=$(mktemp -d)
    write_redis_stubs "$rt_dir"
    rt_out=$(STUB_FAIL='*get secret redis-secure.ca-tls -o jsonpath*' STUB_RC=1 \
      STUB_ERR='Error from server (ServiceUnavailable): the server is currently unable to handle the request' run_redis_step "$rt_dir")
    case "$rt_out" in *"
rc=0") printf '%s\n' "$rt_out" >&2; echo "the step passed with a failed Secret read" >&2; exit 1 ;; esac
    case "$rt_out" in *"source name or key is wrong"*) printf '%s\n' "$rt_out" >&2; echo "an API error was reported as a wrong source name" >&2; exit 1 ;; esac
    case "$rt_out" in *"ServiceUnavailable"*) ;; *) printf '%s\n' "$rt_out" >&2; echo "the API error is missing from the output" >&2; exit 1 ;; esac
    rm -rf "$rt_dir"
}

@test "the private-key check fails when the projection it reads carries a key" {
    # The projection is read once above this check. A check that reads it
    # again passes when that second read fails, because an empty read holds
    # no key material: here the first read returns a key and the second one
    # is refused.
    rt_dir=$(mktemp -d)
    write_redis_stubs "$rt_dir"
    rt_out=$(STUB_PROJ_KEY=1 STUB_FAIL='*get secret redis-secure.tenant-ca -o jsonpath*' STUB_FAIL_FROM=2 STUB_RC=1 \
      STUB_ERR='Error from server (ServiceUnavailable): the server is currently unable to handle the request' run_redis_step "$rt_dir")
    case "$rt_out" in *"
rc=0") printf '%s\n' "$rt_out" >&2; echo "the step passed with key material in the projection" >&2; exit 1 ;; esac
    case "$rt_out" in *"projection carries PEM private key material"*) ;; *) printf '%s\n' "$rt_out" >&2; echo "the step did not fail on the key material" >&2; exit 1 ;; esac
    rm -rf "$rt_dir"
}

@test "a read that finds the step budget spent does not run and says so" {
    rt_dir=$(mktemp -d)
    write_redis_stubs "$rt_dir"
    rt_out=$(RS_BUDGET=0 run_redis_step "$rt_dir")
    case "$rt_out" in *"
rc=0") printf '%s\n' "$rt_out" >&2; echo "the step passed with its budget spent before the first read" >&2; exit 1 ;; esac
    case "$rt_out" in *"step budget is spent, so this read did not run: -n tenant-test get secret redis-secure.tenant-ca"*) ;; *) printf '%s\n' "$rt_out" >&2; echo "the first read did not report the spent budget" >&2; exit 1 ;; esac
    rm -rf "$rt_dir"
}

@test "a read is clamped to what the step budget has left and names itself" {
    # A 2s budget against a read that would hang for 20s: the clamp has to
    # end the read at the budget, not at the 30s per-read cap.
    rt_dir=$(mktemp -d)
    write_redis_stubs "$rt_dir"
    rt_start=$(date +%s)
    rt_out=$(RS_BUDGET=2 STUB_SLEEP_ON='*get secret redis-secure.tenant-ca -o go-template*' STUB_SLEEP=20 run_redis_step "$rt_dir")
    rt_took=$(( $(date +%s) - rt_start ))
    case "$rt_out" in *"
rc=0") printf '%s\n' "$rt_out" >&2; echo "the step passed with a hung read" >&2; exit 1 ;; esac
    case "$rt_out" in *"ran into its "[12]"s bound: -n tenant-test get secret redis-secure.tenant-ca"*) ;; *) printf '%s\n' "$rt_out" >&2; echo "the hung read did not name itself at the budget's bound" >&2; exit 1 ;; esac
    [ "$rt_took" -lt 15 ] || { echo "the step took ${rt_took}s against a 2s budget; the read was not clamped" >&2; exit 1; }
    rm -rf "$rt_dir"
}
