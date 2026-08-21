#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Guest bring-up diagnostics for the site-router VyOS appliance.
#
# The failure this exists for: the suite reached establish-tunnel, neither guest
# served its HTTPS management API inside the step's budget, and the run captured
# NOTHING from inside either guest — so "the guests are slower here than on a dev
# stand" and "the API never comes up at all" were both consistent with every
# artifact, and they need opposite fixes. logSerialConsole gets the console
# captured; it carries kernel and boot output and says nothing about whether
# cloud-init finished, whether the config committed, whether nginx (the :443
# listener) ever bound, or whether the box was busy or wedged.
#
# packages/apps/site-router/files/guest-diag.sh answers those from inside the
# guest, over the serial console — the one channel that does not run through the
# management API whose failure is the subject. This suite pins the two ways that
# can silently stop being true:
#
#   ABSENT     — the emitter never reaches the guest, or reaches it corrupted.
#                The chart embeds it with .Files.Get and B's manifest has it
#                injected by the bring-up step; either can break while everything
#                still renders and boots, leaving a console with only kernel
#                output and no way to tell that anything was lost.
#   UNCOLLECTED — the guest prints perfectly and nothing keeps it. The console
#                lives in a guest-console-log container whose log crust-gather
#                collects under a 180s budget it has been seen exceeding, so the
#                suite's catch writes it into COZY_REPORT_DIR itself.
#
# So the tests below EXECUTE the emitter against a stubbed guest and the catch
# script against a stubbed kubectl, rather than asserting that a string is
# present somewhere. A test that only greps the template would still pass with an
# emitter that cannot run and a collector that writes nothing.
#
# Harness note: the CI path is hack/cozytest.sh, NOT real bats. There is no
# `run`, `$status`, `$output`, `skip`, or setup()/teardown(); each test runs as a
# shell function under `set -eu -x`, so a non-zero exit is the failure, and a
# helper's closing brace on its own line is rewritten to `return 0` — so helpers
# report through files, never through their exit status. Compatible with `bats`
# directly as well.
#
# Run with: hack/cozytest.sh hack/site-router-guest-diag.bats
# -----------------------------------------------------------------------------

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
CHART_SRC="$REPO_ROOT/packages/apps/site-router"
DIAG_SRC="$CHART_SRC/files/guest-diag.sh"
SUITE_DIR="$REPO_ROOT/hack/e2e-chainsaw/site-router"

# Same synthetic reference the appliance-ref suite uses: a documentation host
# (RFC 2606 .example) and a non-zero synthetic digest, so no real registry,
# cluster or build appears here and hack/image-refs-no-placeholder.bats stays
# happy. Needed at all only because templates/dv.yaml refuses to render against
# the committed digest-less placeholder .tag.
FIX_REF="registry.example/site-router/vyos-router-disk:v1.6.0@sha256:deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

# Drop trailing blank lines so a comparison is not decided by whether a YAML
# emitter appended a newline. Everything that could be corrupted — `2>&1`, `>>`,
# `$((...))` — lives on a non-blank line, so this weakens nothing that matters.
strip_trailing_blanks() {
  awk '
    { line[NR] = $0 }
    END { last = 0
          for (i = 1; i <= NR; i++) if (line[i] != "") last = i
          for (i = 1; i <= last; i++) print line[i] }
  ' "$1" > "$2"
}

# Render the chart's cloud-init Secret into $TMP/ud.yaml (the inner cloud-config
# document), with logSerialConsole set to $1. Chart copied rather than edited in
# place so a failed run cannot leave a fixture reference in the tree; `cp -RL`
# dereferences charts/cozy-lib, a symlink that would dangle once copied.
render_userdata() {
  cp -RL "$CHART_SRC" "$TMP/chart"
  printf '%s\n' "$FIX_REF" > "$TMP/chart/images/vyos-router-disk.tag"
  helm template site-router-test "$TMP/chart" -n tenant-test \
    --set "logSerialConsole=$1" -s templates/secret-cloudinit.yaml \
    > "$TMP/rendered.yaml" 2> "$TMP/helm.err" || echo fail > "$TMP/helm.failed"
  yq e 'select(.metadata.name == "site-router-test-cloud-init") | .stringData.userdata' \
    "$TMP/rendered.yaml" > "$TMP/ud.yaml" 2>/dev/null || true
}

# A stub PATH where every command the emitter reads answers instantly, so the
# test measures the emitter rather than this machine.
make_guest_stubs() {
  mkdir -p "$TMP/bin"
  for _t in cloud-init systemctl ss swanctl journalctl; do
    printf '#!/bin/sh\necho "stub-%s $*"\n' "$_t" > "$TMP/bin/$_t"
    chmod +x "$TMP/bin/$_t"
  done
}

@test "the emitter prints a labelled bring-up sample it can be read back from" {
  TMP=$(mktemp -d)
  make_guest_stubs
  # A huge window because /proc/uptime here is the host's, which is far past any
  # real bring-up; the window itself is exercised by its own test below.
  PATH="$TMP/bin:$PATH" \
    COZY_DIAG_CONSOLE="$TMP/console" COZY_DIAG_SAMPLES=1 COZY_DIAG_WINDOW=99999999 \
    sh "$DIAG_SRC" || { echo "emitter exited non-zero" >&2; rm -rf "$TMP"; exit 1; }
  [ -s "$TMP/console" ] || { echo "the emitter wrote nothing to the console" >&2; rm -rf "$TMP"; exit 1; }
  # Every line must be greppable, because the catch script and any human reading
  # a console full of kernel output find these lines by this tag and nothing else.
  total=$(grep -c . "$TMP/console")
  tagged=$(grep -c '^\[cozy-diag\] ' "$TMP/console")
  [ "$total" = "$tagged" ] || {
    echo "$((total - tagged)) of $total console lines are missing the [cozy-diag] tag" >&2
    rm -rf "$TMP"; exit 1
  }
  # The fields that decide the open question. nginx is the :443 HTTPS-API
  # listener on this appliance (packages/system/vyos-router-image/flavors/
  # vyos-router.toml D4 exists because it does not start without its log dir),
  # and top-cpu is what separates a slow boot from a hung service — the reading
  # the failed run could not make.
  for field in 'cloud-init:' 'systemd:' 'failed-units:' 'seed-file:' \
               'active-https-node:' 'nginx:' 'listeners:' 'ipsec-unit:' \
               'loadavg:' 'top-cpu:' 'errors:'; do
    grep -q "\[cozy-diag\] .* $field" "$TMP/console" || {
      echo "sample is missing the '$field' field" >&2
      sed -n '1,40p' "$TMP/console" >&2
      rm -rf "$TMP"; exit 1
    }
  done
  # Uptime and pid on every line: overlapping cron ticks are tolerated rather
  # than locked out, and these are what keep two interleaved runs readable.
  grep -q '^\[cozy-diag\] t=[0-9][0-9]*s p=[0-9][0-9]* ' "$TMP/console" || {
    echo "lines carry no uptime/pid stamp, so interleaved runs cannot be told apart" >&2
    rm -rf "$TMP"; exit 1
  }
  rm -rf "$TMP"
}

@test "no single field can bury the rest of the sample" {
  TMP=$(mktemp -d)
  make_guest_stubs
  # ss floods, the way a real `ps` on a busy guest does. Without the per-field
  # cap this buries every field after it in a console a human has to read.
  cat > "$TMP/bin/ss" <<'STUB'
#!/bin/sh
i=0
while [ "$i" -lt 500 ]; do echo "LISTEN 0 128 0.0.0.0:$i 0.0.0.0:*"; i=$((i + 1)); done
STUB
  chmod +x "$TMP/bin/ss"
  PATH="$TMP/bin:$PATH" \
    COZY_DIAG_CONSOLE="$TMP/console" COZY_DIAG_SAMPLES=1 COZY_DIAG_WINDOW=99999999 \
    sh "$DIAG_SRC"
  listeners=$(grep -c '\[cozy-diag\] .* listeners: ' "$TMP/console")
  [ "$listeners" -le 13 ] || {
    echo "listeners emitted $listeners lines; the per-field cap is not holding" >&2
    rm -rf "$TMP"; exit 1
  }
  # Truncation must be announced. A silently cut field reads as a complete one
  # that found less, which is the failure mode this whole suite is about.
  grep -q 'listeners: <truncated at ' "$TMP/console" || {
    echo "output was cut without saying so" >&2
    rm -rf "$TMP"; exit 1
  }
  # And the fields after the flood still have to be there.
  grep -q '\[cozy-diag\] .* top-cpu: ' "$TMP/console" || {
    echo "a flooding field buried the fields after it" >&2
    rm -rf "$TMP"; exit 1
  }
  rm -rf "$TMP"
}

@test "the emitter goes silent after the bring-up window" {
  TMP=$(mktemp -d)
  make_guest_stubs
  # Past the window every cron tick must cost a few milliseconds and print
  # nothing, or a long-lived gateway with the console on pays console noise for
  # its whole life and this becomes a thing operators turn off.
  PATH="$TMP/bin:$PATH" \
    COZY_DIAG_CONSOLE="$TMP/console" COZY_DIAG_WINDOW=0 \
    sh "$DIAG_SRC" || { echo "emitter exited non-zero past the window" >&2; rm -rf "$TMP"; exit 1; }
  [ ! -s "$TMP/console" ] || {
    echo "emitter still wrote $(grep -c . "$TMP/console") lines past the window" >&2
    rm -rf "$TMP"; exit 1
  }
  rm -rf "$TMP"
}

@test "the chart ships the emitter into the guest, uncorrupted, with cron pointed at it" {
  TMP=$(mktemp -d)
  render_userdata true
  [ ! -f "$TMP/helm.failed" ] || { cat "$TMP/helm.err" >&2; rm -rf "$TMP"; exit 1; }
  yq e '.write_files[] | select(.path == "/config/scripts/cozy-guest-diag.sh") | .content' \
    "$TMP/ud.yaml" > "$TMP/shipped"
  [ -s "$TMP/shipped" ] || {
    echo "the rendered cloud-init carries no emitter script" >&2
    rm -rf "$TMP"; exit 1
  }
  # Byte-identical, not merely present. The chart embeds the file through a YAML
  # block scalar, and a mis-indent silently mangles lines like `2>&1` into
  # something cron will run and fail.
  strip_trailing_blanks "$DIAG_SRC" "$TMP/want"
  strip_trailing_blanks "$TMP/shipped" "$TMP/got"
  cmp -s "$TMP/want" "$TMP/got" || {
    echo "the shipped emitter differs from packages/apps/site-router/files/guest-diag.sh" >&2
    diff "$TMP/want" "$TMP/got" >&2 || true
    rm -rf "$TMP"; exit 1
  }
  # Scheduled-but-absent is the quiet failure: a cron entry naming a path nothing
  # writes installs cleanly and produces silence. Assert the two agree.
  cron=$(yq e '.write_files[] | select(.path == "/etc/cron.d/cozy-guest-diag") | .content' "$TMP/ud.yaml")
  printf '%s\n' "$cron" | grep -q ' /config/scripts/cozy-guest-diag.sh$' || {
    echo "the cron entry does not run the path the script is written to" >&2
    printf '%s\n' "$cron" >&2
    rm -rf "$TMP"; exit 1
  }
  # cron ignores /etc/cron.d entries whose FILENAME contains anything but
  # [A-Za-z0-9_-], so a dot there would install cleanly and never run. Matched on
  # the basename, not the path: /etc/cron.d/ has a dot of its own.
  cronname=$(yq e '.write_files[].path' "$TMP/ud.yaml" | grep '^/etc/cron\.d/' | sed 's|.*/||')
  [ -n "$cronname" ] || { echo "no /etc/cron.d entry rendered at all" >&2; rm -rf "$TMP"; exit 1; }
  case "$cronname" in
    *.*) echo "cron.d filename '$cronname' contains a dot, so cron will ignore it" >&2
         rm -rf "$TMP"; exit 1 ;;
  esac
  # cron also refuses a group- or world-writable crontab.
  perms=$(yq e '.write_files[] | select(.path == "/etc/cron.d/cozy-guest-diag") | .permissions' "$TMP/ud.yaml")
  [ "$perms" = "0644" ] || {
    echo "cron.d entry has permissions $perms, want 0644" >&2
    rm -rf "$TMP"; exit 1
  }
  # And the shipped copy has to actually run, not just match. This is the whole
  # chain: chart -> YAML -> guest file -> output.
  make_guest_stubs
  chmod +x "$TMP/shipped"
  PATH="$TMP/bin:$PATH" \
    COZY_DIAG_CONSOLE="$TMP/console" COZY_DIAG_SAMPLES=1 COZY_DIAG_WINDOW=99999999 \
    sh "$TMP/shipped"
  grep -q '\[cozy-diag\] .* nginx: ' "$TMP/console" || {
    echo "the shipped emitter renders but does not run" >&2
    rm -rf "$TMP"; exit 1
  }
  rm -rf "$TMP"
}

@test "a chart that lost the emitter file fails the render instead of shipping silence" {
  TMP=$(mktemp -d)
  cp -RL "$CHART_SRC" "$TMP/chart"
  printf '%s\n' "$FIX_REF" > "$TMP/chart/images/vyos-router-disk.tag"
  # .Files.Get returns "" for a path outside the packaged chart, so a .helmignore
  # that grows a /files entry would install a cron job pointing at an empty file:
  # boots clean, fires every minute, prints nothing. That is the failure this
  # whole change exists to end, and it would arrive with every render green.
  : > "$TMP/chart/files/guest-diag.sh"
  helm template site-router-test "$TMP/chart" -n tenant-test \
    --set logSerialConsole=true -s templates/secret-cloudinit.yaml \
    > "$TMP/rendered.yaml" 2> "$TMP/helm.err" && echo ok > "$TMP/rendered.ok"
  [ ! -f "$TMP/rendered.ok" ] || {
    echo "an empty emitter file rendered cleanly; the guest would run an empty diagnostic" >&2
    rm -rf "$TMP"; exit 1
  }
  grep -q 'guest-diag.sh is empty or missing' "$TMP/helm.err" || {
    echo "render failed, but not with a message naming the cause" >&2
    cat "$TMP/helm.err" >&2; rm -rf "$TMP"; exit 1
  }
  rm -rf "$TMP"
}

@test "a gateway that did not ask for the console gets no cron job and no script" {
  TMP=$(mktemp -d)
  render_userdata false
  [ ! -f "$TMP/helm.failed" ] || { cat "$TMP/helm.err" >&2; rm -rf "$TMP"; exit 1; }
  # Off by default is what makes it safe to couple this to logSerialConsole: a
  # production gateway that never asked for the console must not acquire a cron
  # job that writes to it.
  paths=$(yq e '.write_files[].path' "$TMP/ud.yaml")
  printf '%s\n' "$paths" | grep -q '^/opt/vyatta/etc/config/config.boot$' || {
    echo "the config seed disappeared, which is not what this test is about" >&2
    rm -rf "$TMP"; exit 1
  }
  printf '%s\n' "$paths" | grep -q 'cozy-guest-diag' && {
    echo "diagnostics installed on a gateway that did not enable the console:" >&2
    printf '%s\n' "$paths" >&2
    rm -rf "$TMP"; exit 1
  }
  rm -rf "$TMP"
}
