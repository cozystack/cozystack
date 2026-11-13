#!/usr/bin/env bats
# Behavioural tests for the "release system databases" pass of the Postgres
# init script:
#
#   packages/apps/postgres/templates/init-script.yaml
#
# The pass is cut out of the rendered script and RUN against a fake psql that
# records which database each session connects to. The helm-unittest suite in
# packages/apps/postgres/tests/system_databases_test.yaml matches the SQL text,
# and stayed green when the template0 guard was inverted: the rendered pass then
# connected to template0, which accepts no connection, and skipped postgres and
# template1, which are the two it exists to hand back.
#
# What is pinned here:
#   * no session ever connects to template0;
#   * postgres and template1 are each connected to and released;
#   * every taken-over system database, template0 included, has its comment
#     cleared;
#   * releasing template1 visits every other connectable database, for the
#     trigger it passed on to them, and a failed lookup of those databases
#     stops the pass before the comment that selects template1 is cleared;
#   * the session that releases a system database hands its public schema
#     and the database itself back only from a marked <db>_admin or
#     <db>_readonly owner (or cozystack:orphaned), before the reassignment,
#     an inherited public only from a marked template1_admin, and touches
#     only the roles that carry the
#     "cozystack:managed" mark, not a role that merely has one of the names.
#
# Run from the repo root with make bats-unit-tests. Direct status checks
# retain compatibility with cozytest, which has no Bats run/setup helpers.
# Inline cleanup preserves Bats' TAP handler on failed assertions.

load test_helper

PG_CHART=packages/apps/postgres

# render_pass <out>
# Extract the release pass, from its banner to the next one, out of the rendered
# init script. The non-empty check must be an early `return 1`: hack/cozytest.sh
# rewrites every line matching ^}$ into `return 0` plus `}`, helpers included,
# so a check left in last-command position has its status discarded.
render_pass() {
  helm template pg-test "$PG_CHART" --namespace tenant-test --set-json '_cluster={"scheduling":{}}' 2>/dev/null \
    | yq eval 'select(.metadata.name == "pg-test-init-script") | .stringData["init.sh"]' - \
    | awk '/^echo "== release system databases"/ { f = 1 } /^echo "== delete databases"/ { f = 0 } f' > "$1"
  grep -q 'SYSTEM_DBS=' "$1" || return 1
}

# write_fake_psql <dir>
# Emit a fake psql. A `-c` query answers from $SYSTEM_DBS for the system
# database lookup and from $OTHER_DBS for the connectable-database lookup,
# which exits with $CLONE_LOOKUP_FAILS instead when that is set. A
# session fed on stdin logs "connect <dbname>" ("connect -" without --dbname)
# followed by its first line and every -v db= value, to $PLOG, and its whole
# input under a "-- session <dbname> db=<value>" header to $PLOG.sql.
# No line below is a bare column-0 `}`, so cozytest.sh's parser leaves it alone.
write_fake_psql() {
  cat > "$1/psql" <<'PEOF'
#!/bin/sh
dbname=- query= vdb=
while [ $# -gt 0 ]; do
  case "$1" in
    --dbname) dbname=$2; shift ;;
    -c) query=$2; shift ;;
    -v) case "$2" in db=*) vdb=${2#db=} ;; esac; shift ;;
  esac
  shift
done
if [ -n "$query" ]; then
  case "$query" in
    *"d.datname IN ('postgres', 'template0', 'template1')"*) printf '%s\n' $SYSTEM_DBS ;;
    *datallowconn*)
      if [ -n "${CLONE_LOOKUP_FAILS:-}" ]; then
        echo "fake psql: connectable-database lookup failed" >&2
        exit "$CLONE_LOOKUP_FAILS"
      fi
      printf '%s\n' $OTHER_DBS ;;
    *) echo "fake psql: unexpected query: $query" >&2; exit 64 ;;
  esac
  exit 0
fi
sql=$(cat)
echo "connect $dbname db=$vdb $(printf '%s\n' "$sql" | head -n 1)" >> "$PLOG"
printf -- '-- session %s db=%s\n%s\n' "$dbname" "$vdb" "$sql" >> "$PLOG.sql"
PEOF
  chmod +x "$1/psql"
}

# run_pass <dir>
# Run the extracted pass with the fake psql first on PATH; status in $rc.
run_pass() {
  export PLOG="$1/calls"
  : > "$PLOG"
  rc=0
  PATH="$1:$PATH" sh -e "$1/pass.sh" > "$1/out" 2> "$1/err" || rc=$?
}

@test "a render that produces no release pass fails instead of passing as a no-op" {
  tmp=$(mktemp -d)
  if PG_CHART=packages/apps/no-such-chart render_pass "$tmp/pass.sh"; then
    echo "FAIL: render_pass reported success for a chart that does not exist"
    false
  fi
  rm -rf "$tmp"
}

@test "release pass connects to postgres and template1, never to template0" {
  tmp=$(mktemp -d)
  write_fake_psql "$tmp"
  render_pass "$tmp/pass.sh"
  export SYSTEM_DBS="postgres template0 template1" OTHER_DBS="postgres app"
  run_pass "$tmp"

  if [ "$rc" -ne 0 ]; then
    echo "FAIL: the pass exited $rc"
    cat "$tmp/out" "$tmp/err"
    false
  fi
  if grep -q '^connect template0 ' "$tmp/calls"; then
    echo "FAIL: the pass connected to template0, which accepts no connection"
    cat "$tmp/calls"
    false
  fi
  for db in postgres template1; do
    if ! grep -q "^connect $db db=$db DROP EVENT TRIGGER IF EXISTS trigger_auto_grant;" "$tmp/calls"; then
      echo "FAIL: $db was not connected to and released"
      cat "$tmp/calls"
      false
    fi
  done
  for db in postgres template0 template1; do
    if ! grep -q "^connect - db=$db COMMENT ON DATABASE :\"db\" IS NULL;" "$tmp/calls"; then
      echo "FAIL: the comment on $db was not cleared"
      cat "$tmp/calls"
      false
    fi
  done
  rm -rf "$tmp"
}

@test "release session hands public back and touches only marked roles" {
  tmp=$(mktemp -d)
  write_fake_psql "$tmp"
  render_pass "$tmp/pass.sh"
  export SYSTEM_DBS="postgres" OTHER_DBS="postgres"
  run_pass "$tmp"

  if [ "$rc" -ne 0 ]; then
    echo "FAIL: the pass exited $rc"
    cat "$tmp/out" "$tmp/err"
    false
  fi
  awk '/^-- session / { f = ($0 == "-- session postgres db=postgres") } f' "$tmp/calls.sql" > "$tmp/release.sql"
  # Each statement must end on its whole predicate: a clause appended after the
  # mark (an OR, say) would select an unmarked role again.
  reassign_where="WHERE EXISTS (SELECT FROM pg_auth_members m JOIN pg_roles g ON g.oid = m.roleid JOIN pg_roles u ON u.oid = m.member WHERE g.rolname = 'cozystack:managed' AND u.rolname = r)\\gexec"
  owner_marked="(o.rolname = 'cozystack:orphaned' OR o.rolname IN (:'db' || '_admin', :'db' || '_readonly') AND EXISTS (SELECT FROM pg_auth_members m JOIN pg_roles g ON g.oid = m.roleid WHERE g.rolname = 'cozystack:managed' AND m.member = o.oid))\\gexec"
  public_where="WHERE n.nspname = 'public' AND $owner_marked"
  database_where="WHERE d.datname = :'db' AND $owner_marked"
  public_line=$(grep -n 'ALTER SCHEMA public OWNER TO' "$tmp/release.sql" | head -1)
  database_line=$(grep -n 'ALTER DATABASE' "$tmp/release.sql" | head -1)
  reassign_line=$(grep -n 'REASSIGN OWNED BY' "$tmp/release.sql" | head -1)
  if [ "${public_line##*"$public_where"}" != "" ] || [ -z "$public_line" ]; then
    echo "FAIL: the release session does not hand public back only from a marked <db>_admin or <db>_readonly owner"
    cat "$tmp/release.sql"
    false
  fi
  if [ "${database_line##*"$database_where"}" != "" ] || [ -z "$database_line" ]; then
    echo "FAIL: the release session does not take the database back only from a marked owner"
    cat "$tmp/release.sql"
    false
  fi
  if [ "${reassign_line##*"$reassign_where"}" != "" ] || [ -z "$reassign_line" ]; then
    echo "FAIL: the release session does not reassign only roles carrying the cozystack:managed mark"
    cat "$tmp/release.sql"
    false
  fi
  # public and the database must be handed back before REASSIGN OWNED would
  # move them to cozystack:orphaned.
  if [ "${public_line%%:*}" -ge "${reassign_line%%:*}" ] || [ "${database_line%%:*}" -ge "${reassign_line%%:*}" ]; then
    echo "FAIL: public or the database is handed back after the reassignment has already moved it"
    cat "$tmp/release.sql"
    false
  fi
  rm -rf "$tmp"
}

@test "release pass of template1 visits every other connectable database" {
  tmp=$(mktemp -d)
  write_fake_psql "$tmp"
  render_pass "$tmp/pass.sh"
  export SYSTEM_DBS="template1" OTHER_DBS="postgres app"
  run_pass "$tmp"

  if [ "$rc" -ne 0 ]; then
    echo "FAIL: the pass exited $rc"
    cat "$tmp/out" "$tmp/err"
    false
  fi
  for db in postgres app; do
    if ! grep -q "^connect $db db= SELECT 'DROP EVENT TRIGGER IF EXISTS trigger_auto_grant'" "$tmp/calls"; then
      echo "FAIL: $db was not checked for the trigger inherited from template1"
      cat "$tmp/calls"
      false
    fi
  done
  # An inherited public changes hands only from a template1_admin the chart
  # marked; an owner that merely has the name keeps it.
  inherited_where="WHERE n.nspname = 'public' AND o.rolname = 'template1_admin' AND EXISTS (SELECT FROM pg_auth_members m JOIN pg_roles g ON g.oid = m.roleid WHERE g.rolname = 'cozystack:managed' AND m.member = o.oid)\\gexec"
  inherited_line=$(awk '/^-- session / { f = ($0 == "-- session app db=") } f' "$tmp/calls.sql" | grep 'ALTER SCHEMA public OWNER TO' | head -1)
  if [ -z "$inherited_line" ] || [ "${inherited_line##*"$inherited_where"}" != "" ]; then
    echo "FAIL: an inherited public is handed back without checking that a marked template1_admin owns it"
    cat "$tmp/calls.sql"
    false
  fi
  rm -rf "$tmp"
}

@test "release pass of template1 stops before clearing its comment when the clone lookup fails" {
  # Clearing the comment is what stops later runs from selecting template1, so
  # a lookup failure that let the pass go on would skip the clone repair for
  # good.
  tmp=$(mktemp -d)
  write_fake_psql "$tmp"
  render_pass "$tmp/pass.sh"
  export SYSTEM_DBS="template1" OTHER_DBS="postgres app" CLONE_LOOKUP_FAILS=73
  run_pass "$tmp"
  unset CLONE_LOOKUP_FAILS

  if [ "$rc" -ne 73 ]; then
    echo "FAIL: the pass exited $rc instead of stopping on the failed lookup (73)"
    cat "$tmp/out" "$tmp/err" "$tmp/calls"
    false
  fi
  if grep -q 'COMMENT ON DATABASE' "$tmp/calls"; then
    echo "FAIL: the comment on template1 was cleared although its clones were never visited"
    cat "$tmp/calls"
    false
  fi
  rm -rf "$tmp"
}

@test "release pass of postgres alone leaves the other databases alone" {
  tmp=$(mktemp -d)
  write_fake_psql "$tmp"
  render_pass "$tmp/pass.sh"
  export SYSTEM_DBS="postgres" OTHER_DBS="postgres app"
  run_pass "$tmp"

  if [ "$rc" -ne 0 ]; then
    echo "FAIL: the pass exited $rc"
    cat "$tmp/out" "$tmp/err"
    false
  fi
  if grep -q '^connect app ' "$tmp/calls"; then
    echo "FAIL: the pass visited app although template1 was not taken over"
    cat "$tmp/calls"
    false
  fi
  rm -rf "$tmp"
}
