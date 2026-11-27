#!/bin/sh
# Adapts the upstream Alerta entrypoint and nginx template.
#
# Credentials out of the container log, which tenant users who may only view
# their namespace can still read: the upstream entrypoint prints the whole
# environment (ADMIN_PASSWORD, ADMIN_KEY, ALERTA_API_KEY) and the admin API
# keys it creates, and nginx logs the request line, where Alertmanager and the
# Telegram webhook pass the key as ?api-key=.
#
# Admin password follows ADMIN_PASSWORD: `alertad user --all` only creates
# missing admin users, so an existing one would keep the password it was
# created with. The entrypoint runs sync-admin-password.py right after it.
#
# Every edit is checked, so an upstream change to either file fails the image
# build instead of silently bringing the old behaviour back.
#
# Usage: patch-upstream.sh <docker-entrypoint.sh> <nginx.conf.j2>
set -eu

entrypoint=$1
nginx_conf=$2

fail() {
  echo "patch-upstream: $*" >&2
  exit 1
}

# sed -i differs between GNU and BSD; rewrite in place through a temporary
# file, keeping the target's mode.
edit() {
  file=$1
  shift
  sed "$@" "$file" >"$file.redact"
  cat "$file.redact" >"$file"
  rm -f "$file.redact"
}

grep -qx 'env | sort' "$entrypoint" || fail "env dump not found in $entrypoint"
grep -qx '  alertad key --all' "$entrypoint" || fail "'alertad key --all' not found in $entrypoint"
grep -qx '  alertad user --all --password "${ADMIN_PASSWORD}" || true' "$entrypoint" \
  || fail "admin user creation not found in $entrypoint"
grep -q '^    alertad key --username "${ADMIN_USER}" --key "${ADMIN_KEY}" --duration "${MAXAGE}" || true$' "$entrypoint" \
  || fail "user-defined key creation not found in $entrypoint"

edit "$entrypoint" \
  -e '/^env | sort$/d' \
  -e '/^  alertad user --all --password "${ADMIN_PASSWORD}" || true$/a\
  /venv/bin/python /app/sync-admin-password.py
' \
  -e 's/^  alertad key --all$/  alertad key --all >\/dev\/null/' \
  -e 's/^\(    alertad key --username "${ADMIN_USER}" --key "${ADMIN_KEY}" --duration "${MAXAGE}"\) || true$/\1 >\/dev\/null || true/'

if grep -q '^env' "$entrypoint"; then
  fail "env dump still present in $entrypoint"
fi
[ "$(grep -c '^ *alertad key .*>/dev/null' "$entrypoint")" -eq 2 ] || fail "key creation output is not silenced in $entrypoint"
grep -qx '  /venv/bin/python /app/sync-admin-password.py' "$entrypoint" || fail "admin password sync not added to $entrypoint"

grep -q "^    log_format main " "$nginx_conf" || fail "log_format main not found in $nginx_conf"
[ "$(grep -c '"\$request"' "$nginx_conf")" -eq 1 ] || fail "expected exactly one \$request in $nginx_conf"

# The map rewrites the first api-key parameter; a request line carries one.
edit "$nginx_conf" \
  -e '/^    log_format main /i\
    map $request $request_redacted {\
        "~*^(?<redact_pre>.*[?\&]api-key=)[^\&\\s]*(?<redact_post>.*)$" "${redact_pre}REDACTED${redact_post}";\
        default $request;\
    }\
' \
  -e 's/"\$request"/"$request_redacted"/'

grep -q '"\$request_redacted"' "$nginx_conf" || fail "log_format still logs the raw request line in $nginx_conf"
