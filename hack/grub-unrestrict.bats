#!/usr/bin/env bats
# The two ways the VyOS appliance's bootloader lock can fail open silently.
#
# The appliance sets GRUB `superusers` so the serial console cannot reach a root
# shell: cozy:tenant:use:base grants virtualmachineinstances/console and /vnc in
# every tenant namespace, which is where a SiteRouter lands, and stock VyOS ships
# a "Password reset" boot entry that runs
# init=/usr/libexec/vyos/system/standalone_root_pw_reset. Root in that guest
# removes the only compensating control the relaxed OVN port has
# (docs/security-model.md). packages/system/vyos-router-image/hack/inject-appliance.sh
# installs the lock.
#
# Neither half of it fails loudly on its own, which is why they are pinned here.
#
# The drop-in name. grub_main.j2 upstream sources ONLY
# `${prefix}/grub.cfg.d/*-autoload.cfg`. A drop-in named anything else is written
# to a real directory, survives every check, and is never read — the image boots
# fine and the lock simply is not there.
#
# The menuentry regex. `superusers` restricts editing, the command line AND every
# menu entry not marked --unrestricted, so the normal-boot entry has to carry the
# flag or the VM stops at a password prompt no one can answer. That entry's line
# shape belongs to upstream (data/templates/grub/grub_vyos_version.j2), so the
# regex is the part that rots. It lives in its own .sed file precisely so this
# test runs the same program the build runs, instead of a copy that can drift.
#
# Harness note: the CI path is hack/cozytest.sh, NOT real bats. There is no `run`,
# `$status`, `$output`, `skip`, or setup()/teardown(); each test runs as a shell
# function under `set -eu -x`, so a non-zero exit is the failure. Paths are
# repo-root-relative — BATS_TEST_DIRNAME is unset and would abort the whole suite
# under `set -u`.
#
# Run with: hack/cozytest.sh hack/grub-unrestrict.bats

SEDPROG=packages/system/vyos-router-image/hack/grub-unrestrict.sed
INJECT=packages/system/vyos-router-image/hack/inject-appliance.sh

@test "the unrestrict regex marks a generated VyOS version menu entry" {
  # The shape raw_image.py writes into grub.cfg.d/vyos-versions/*.cfg.
  got="$(printf 'menuentry "1.5-stream-2026.03" --id vyos-1A2B3C4D {\n' | sed -E -f "$SEDPROG")"
  want='menuentry "1.5-stream-2026.03" --id vyos-1A2B3C4D --unrestricted {'
  if [ "$got" != "$want" ]; then
    echo "generated entry not marked unrestricted"
    echo "  got:  $got"
    echo "  want: $want"
    echo "Upstream changed the menuentry line shape, or $SEDPROG did."
    echo "Unfixed, the appliance boots to a GRUB password prompt and never comes up."
    exit 1
  fi
}

@test "the unrestrict regex marks the upstream Jinja template line" {
  # data/templates/grub/grub_vyos_version.j2, patched so a structure migration
  # (grub_update.py -> version_add) re-renders the entry with the flag intact.
  got="$(printf 'menuentry "{{ version_name }}" --id {{ version_uuid }} {\n' | sed -E -f "$SEDPROG")"
  want='menuentry "{{ version_name }}" --id {{ version_uuid }} --unrestricted {'
  if [ "$got" != "$want" ]; then
    echo "template line not marked unrestricted"
    echo "  got:  $got"
    echo "  want: $want"
    exit 1
  fi
}

@test "the unrestrict regex is idempotent and leaves other lines alone" {
  in_lines='menuentry "already" --id vyos-DEADBEEF --unrestricted {
    linux /boot/1.5/vmlinuz console=ttyS0,115200
}
submenu "Boot options" {'
  got="$(printf '%s\n' "$in_lines" | sed -E -f "$SEDPROG")"
  if [ "$got" != "$in_lines" ]; then
    echo "the regex must not touch an entry that already carries the flag,"
    echo "nor any line that is not a version menuentry"
    echo "  got:  $got"
    exit 1
  fi
}

@test "the GRUB drop-in is named so upstream's grub.cfg actually sources it" {
  # grub_main.j2: `for cfgfile in ${prefix}/grub.cfg.d/*-autoload.cfg`
  dropins="$(grep -oE '(grub\.cfg\.d|GRUB_CFG_D\})/[A-Za-z0-9._-]+\.cfg' "$INJECT" | sed 's|.*/||' | sort -u || true)"
  if [ -z "$dropins" ]; then
    echo "no GRUB drop-in filename found in $INJECT — has the bootloader lock been removed?"
    exit 1
  fi
  for f in $dropins; do
    case "$f" in
      *-autoload.cfg) ;;
      *)
        echo "GRUB drop-in $f is never read: grub.cfg sources only *-autoload.cfg."
        echo "The file would be written, every check would pass, and the bootloader"
        echo "would stay unlocked."
        exit 1
        ;;
    esac
  done
}
