#!/usr/bin/env bats

load test_helper

# The fixtures reproduce the lines of the upstream alerta-web 9.1.0
# docker-entrypoint.sh and nginx.conf.j2 that the respin edits.
SCRIPT="packages/system/monitoring/images/alerta/patch-upstream.sh"

write_fixtures() {
  cat >"${BATS_TEST_TMPDIR}/docker-entrypoint.sh" <<'EOF'
#!/bin/bash
set -e

ADMIN_USER=${ADMIN_USERS%%,*}
ADMIN_PASSWORD=${ADMIN_PASSWORD:-alerta}

env | sort

if [ -n "${ADMIN_USERS}" ]; then
  echo "# Create admin users."
  alertad user --all --password "${ADMIN_PASSWORD}" || true
  echo "# Create admin API keys."
  alertad key --all

  if [ -n "${ADMIN_KEY}" ]; then
    echo "# Create user-defined admin API key."
    alertad key --username "${ADMIN_USER}" --key "${ADMIN_KEY}" --duration "${MAXAGE}" || true
  fi
fi

exec "$@"
EOF
  chmod 0755 "${BATS_TEST_TMPDIR}/docker-entrypoint.sh"
  cat >"${BATS_TEST_TMPDIR}/nginx.conf.j2" <<'EOF'
http {
    log_format main '$remote_addr - $remote_user [$time_local] '
        '"$request" $status $body_bytes_sent '
        '"$http_referer" "$http_user_agent" - $request_time';

    server {
        access_log /dev/stdout main;
    }
}
EOF
}

@test "the entrypoint no longer prints the environment or the admin API keys" {
  write_fixtures
  sh "${SCRIPT}" "${BATS_TEST_TMPDIR}/docker-entrypoint.sh" "${BATS_TEST_TMPDIR}/nginx.conf.j2"

  assert_file_lacks_pattern '^env' "${BATS_TEST_TMPDIR}/docker-entrypoint.sh"
  grep -qx '  alertad key --all >/dev/null' "${BATS_TEST_TMPDIR}/docker-entrypoint.sh"
  grep -qx '    alertad key --username "${ADMIN_USER}" --key "${ADMIN_KEY}" --duration "${MAXAGE}" >/dev/null || true' \
    "${BATS_TEST_TMPDIR}/docker-entrypoint.sh"
  [ -x "${BATS_TEST_TMPDIR}/docker-entrypoint.sh" ]
}

@test "the entrypoint syncs the admin password right after creating admin users" {
  write_fixtures
  sh "${SCRIPT}" "${BATS_TEST_TMPDIR}/docker-entrypoint.sh" "${BATS_TEST_TMPDIR}/nginx.conf.j2"

  run grep -A1 -x '  alertad user --all --password "${ADMIN_PASSWORD}" || true' "${BATS_TEST_TMPDIR}/docker-entrypoint.sh"
  [ "${status}" -eq 0 ]
  [ "${lines[1]}" = '  /venv/bin/python /app/sync-admin-password.py' ]
}

@test "nginx logs the request line with the api-key parameter masked" {
  write_fixtures
  sh "${SCRIPT}" "${BATS_TEST_TMPDIR}/docker-entrypoint.sh" "${BATS_TEST_TMPDIR}/nginx.conf.j2"

  grep -qF '"$request_redacted" $status' "${BATS_TEST_TMPDIR}/nginx.conf.j2"
  grep -qF 'map $request $request_redacted {' "${BATS_TEST_TMPDIR}/nginx.conf.j2"
  grep -qF '"~*^(?<redact_pre>.*[?&]api-key=)[^&\s]*(?<redact_post>.*)$" "${redact_pre}REDACTED${redact_post}";' \
    "${BATS_TEST_TMPDIR}/nginx.conf.j2"
  assert_file_lacks_pattern '"[$]request"' "${BATS_TEST_TMPDIR}/nginx.conf.j2"
}

@test "an upstream file that no longer matches fails the build instead of passing unedited" {
  write_fixtures
  sed 's/^env | sort$/printenv/' "${BATS_TEST_TMPDIR}/docker-entrypoint.sh" >"${BATS_TEST_TMPDIR}/changed.sh"

  run sh "${SCRIPT}" "${BATS_TEST_TMPDIR}/changed.sh" "${BATS_TEST_TMPDIR}/nginx.conf.j2"
  [ "${status}" -ne 0 ]
  [[ "${output}" == *"env dump not found"* ]]
}
