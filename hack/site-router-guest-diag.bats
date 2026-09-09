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
# document), passing $1 straight to --set so a caller can supply either the
# platform key (_logSerialConsole) or the un-prefixed name a tenant would reach
# for. Chart copied rather than edited in place so a failed run cannot leave a
# fixture reference in the tree; `cp -RL` dereferences charts/cozy-lib, a symlink
# that would dangle once copied.
render_userdata() {
  cp -RL "$CHART_SRC" "$TMP/chart"
  printf '%s\n' "$FIX_REF" > "$TMP/chart/images/vyos-router-disk.tag"
  helm template site-router-test "$TMP/chart" -n tenant-test \
    --set "$1" -s templates/secret-cloudinit.yaml \
    > "$TMP/rendered.yaml" 2> "$TMP/helm.err" || echo fail > "$TMP/helm.failed"
  yq e 'select(.metadata.name == "site-router-test-cloud-init") | .stringData.userdata' \
    "$TMP/rendered.yaml" > "$TMP/ud.yaml" 2>/dev/null || true
  yq e 'select(.metadata.name == "site-router-test-diag") | .stringData' \
    "$TMP/rendered.yaml" > "$TMP/diag.yaml" 2>/dev/null || true
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
  # listener on this appliance (the seed creates its log directory because it
  # does not start without one; see packages/system/vyos-router-image/overlay),
  # and top-cpu is what separates a slow boot from a hung service — the reading
  # the failed run could not make.
  for field in 'appliance-seed:' 'systemd:' 'failed-units:' 'seed-file:' \
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
  render_userdata _logSerialConsole=true
  [ ! -f "$TMP/helm.failed" ] || { cat "$TMP/helm.err" >&2; rm -rf "$TMP"; exit 1; }
  # The emitter travels on its own disk, not beside the configuration seed, so a
  # mistake here cannot reach the config.boot the gateway boots on.
  yq e '.["guest-diag.sh"]' "$TMP/diag.yaml" > "$TMP/shipped"
  [ -s "$TMP/shipped" ] || {
    echo "the rendered diagnostics Secret carries no emitter script" >&2
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
  yq e '.["guest-diag.cron"]' "$TMP/diag.yaml" | grep -q ' /config/scripts/cozy-guest-diag.sh$' || {
    echo "the cron entry does not run the path the script is installed to" >&2
    yq e '.["guest-diag.cron"]' "$TMP/diag.yaml" >&2
    rm -rf "$TMP"; exit 1
  }
  rm -rf "$TMP"
}

@test "the appliance seed installs the emitter where cron will actually read it" {
  # Two rules cron enforces silently, and both moved from the chart to the seed
  # when the diagnostics moved onto their own disk: it ignores /etc/cron.d
  # entries whose FILENAME carries anything but [A-Za-z0-9_-], and it refuses a
  # group- or world-writable crontab. Neither failure says anything at runtime,
  # so they are pinned here against the seed script that now does the installing.
  seed=packages/system/vyos-router-image/overlay/vyos-appliance-seed.sh
  [ -f "$seed" ] || { echo "appliance seed script missing at $seed" >&2; exit 1; }

  target=$(grep -oE '/etc/cron\.d/[A-Za-z0-9._-]+' "$seed" | head -1)
  [ -n "$target" ] || { echo "the seed installs no /etc/cron.d entry" >&2; exit 1; }
  case "${target##*/}" in
    *.*) echo "seed installs cron.d entry '${target##*/}', whose dot makes cron ignore it" >&2
         exit 1 ;;
  esac

  grep -qE "install -m 0644 [^ ]+ ${target}\$" "$seed" || {
    echo "the seed does not install ${target} mode 0644, which cron requires" >&2
    grep -n 'cron' "$seed" >&2 || true
    exit 1
  }
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
    --set _logSerialConsole=true -s templates/secret-cloudinit.yaml \
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
  render_userdata _logSerialConsole=false
  [ ! -f "$TMP/helm.failed" ] || { cat "$TMP/helm.err" >&2; rm -rf "$TMP"; exit 1; }
  # Off by default is what makes it safe to couple this to the console switch: a
  # production gateway that never asked for the console must not acquire a cron
  # job that writes to it.
  # The configuration seed still ships; it is the diagnostics disk that must not.
  grep -q '^// Release version: ' "$TMP/ud.yaml" || {
    echo "the config seed disappeared, which is not what this test is about" >&2
    rm -rf "$TMP"; exit 1
  }
  if [ -s "$TMP/diag.yaml" ] && [ "$(cat "$TMP/diag.yaml")" != "null" ]; then
    echo "diagnostics Secret rendered for a gateway that did not enable the console:" >&2
    cat "$TMP/diag.yaml" >&2
    rm -rf "$TMP"; exit 1
  fi
  rm -rf "$TMP"
}

@test "a tenant-supplied logSerialConsole ships no emitter into the guest" {
  TMP=$(mktemp -d)
  # THE BOUNDARY, from the tenant's side. `_logSerialConsole` cannot be submitted
  # through the aggregated API at all: validateNoInternalKeys in
  # pkg/registry/apps/application/rest.go rejects every top-level `_` key with a
  # 400 on Create and Update, which pkg/registry/apps/application/internal_keys_test.go
  # pins. So the only thing a tenant can actually get into spec.values is the
  # un-prefixed name, and it has to do nothing. Supplied here exactly as a tenant
  # would supply it.
  render_userdata logSerialConsole=true
  [ ! -f "$TMP/helm.failed" ] || { cat "$TMP/helm.err" >&2; rm -rf "$TMP"; exit 1; }
  grep -q '^// Release version: ' "$TMP/ud.yaml" || {
    echo "the config seed disappeared, which is not what this test is about" >&2
    rm -rf "$TMP"; exit 1
  }
  if [ -s "$TMP/diag.yaml" ] && [ "$(cat "$TMP/diag.yaml")" != "null" ]; then
    echo "a TENANT-SUPPLIED logSerialConsole installed the guest emitter; the switch is tenant-reachable again:" >&2
    cat "$TMP/diag.yaml" >&2
    rm -rf "$TMP"; exit 1
  fi
  rm -rf "$TMP"
}

@test "remote-site-b is injected with the same emitter, byte for byte" {
  TMP=$(mktemp -d)
  # Mirrors the bring-up step's own pipeline. awk, not sed: the script contains
  # `2>&1`, and sed would expand the `&` in a replacement to the whole match.
  awk -v f="$DIAG_SRC" '
    /__GUEST_DIAG_SCRIPT__/ {
      match($0, /^ */); pad = substr($0, 1, RLENGTH)
      while ((getline line < f) > 0) print pad line
      close(f); next
    }
    { print }
  ' "$SUITE_DIR/remote-site-b.yaml" \
    | sed "s|__VYOS_DISK_REF__|$FIX_REF|" > "$TMP/b.yaml"
  grep -q -e '__GUEST_DIAG_SCRIPT__' -e '__VYOS_DISK_REF__' "$TMP/b.yaml" && {
    echo "a placeholder survived substitution, so B would run a placeholder as its diagnostic" >&2
    rm -rf "$TMP"; exit 1
  }
  # B stopped shipping a `#cloud-config` when the appliance stopped having
  # cloud-init: the seed copies `user-data` verbatim into config.boot, so the
  # diagnostics ride their own `cozydiag` Secret, keyed the way the chart's
  # site-router.diagFiles keys A's. Reading .write_files[] here kept passing
  # against a shape the image could no longer boot, which is why this assertion
  # names the Secret rather than digging inside the userdata.
  yq e 'select(.kind == "Secret" and .metadata.name == "remote-site-b-diag") | .stringData."guest-diag.sh"' \
    "$TMP/b.yaml" > "$TMP/shipped"
  # B and A must report in the same format: the last diagnosis was credible
  # because the same symptom appeared on both ends through two different config
  # paths, and that argument needs both to say things the same way.
  strip_trailing_blanks "$DIAG_SRC" "$TMP/want"
  strip_trailing_blanks "$TMP/shipped" "$TMP/got"
  cmp -s "$TMP/want" "$TMP/got" || {
    echo "B's injected emitter differs from the source; sed-style & expansion is the usual cause" >&2
    diff "$TMP/want" "$TMP/got" >&2 || true
    rm -rf "$TMP"; exit 1
  }
  # And B's console has to be captured, or the emitter prints into nothing.
  lsc=$(yq e 'select(.kind == "VirtualMachine") | .spec.template.spec.domain.devices.logSerialConsole' "$TMP/b.yaml")
  [ "$lsc" = "true" ] || {
    echo "B does not log its serial console ($lsc), so its emitter output is discarded" >&2
    rm -rf "$TMP"; exit 1
  }
  rm -rf "$TMP"
}

@test "the suite's catch writes both guest consoles into the uploaded report" {
  TMP=$(mktemp -d)
  yq e '.spec.catch[] | select(.description == "guest serial consoles on failure") | .script.content' \
    "$SUITE_DIR/chainsaw-test.yaml" > "$TMP/catch.sh"
  [ -s "$TMP/catch.sh" ] || {
    echo "the suite has no guest-console catch, so a console that exists is never collected" >&2
    rm -rf "$TMP"; exit 1
  }
  mkdir -p "$TMP/bin" "$TMP/report"
  # A kubectl that answers exactly the two calls the collector makes, and refuses
  # a logs read that does not name the guest-console-log container — so a
  # collector that forgot the container fails this test rather than silently
  # collecting virt-launcher's own log.
  cat > "$TMP/bin/kubectl" <<'STUB'
#!/bin/sh
vm=""
for a in "$@"; do
  case "$a" in vm.kubevirt.io/name=*) vm="${a#vm.kubevirt.io/name=}" ;; esac
done
case " $* " in
  *" get pod "*) echo "virt-launcher-$vm-xk4d9" ;;
  *" logs "*)
    case " $* " in
      *" -c guest-console-log "*) ;;
      *) echo "container not found" >&2; exit 1 ;;
    esac
    echo "[    0.000000] Linux version 6.x"
    echo "[cozy-diag] t=42s p=900 nginx: activating"
    echo "vyos login:"
    ;;
  *) exit 1 ;;
esac
STUB
  chmod +x "$TMP/bin/kubectl"
  PATH="$TMP/bin:$PATH" COZY_REPORT_DIR="$TMP/report" TEST_NAME=site-router \
    sh "$TMP/catch.sh" > "$TMP/out" 2>&1 || {
      echo "the catch script failed; diagnostics must never fail the catch" >&2
      cat "$TMP/out" >&2; rm -rf "$TMP"; exit 1
    }
  # Collected as FILES under COZY_REPORT_DIR, which is the tree
  # hack/cozyreport.sh folds into cozyreport.tgz. Echoing to the job log alone
  # would be lost to a job-level timeout, and crust-gather's 180s budget has been
  # seen truncating a cluster this size.
  for vm in site-router-a remote-site-b; do
    [ -s "$TMP/report/snapshots/site-router/guest-consoles/$vm.log" ] || {
      echo "no collected console for $vm under COZY_REPORT_DIR" >&2
      find "$TMP/report" >&2 || true
      cat "$TMP/out" >&2
      rm -rf "$TMP"; exit 1
    }
  done
  # The [cozy-diag] timeline also has to reach the job log, which is the copy a
  # reader sees without downloading an artifact.
  grep -q '\[cozy-diag\] t=42s p=900 nginx: activating' "$TMP/out" || {
    echo "the catch collected the console but did not surface the [cozy-diag] timeline" >&2
    cat "$TMP/out" >&2; rm -rf "$TMP"; exit 1
  }
  rm -rf "$TMP"
}
