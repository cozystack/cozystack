# Mark a VyOS normal-boot GRUB menu entry bootable without authentication.
#
# Shared by hack/inject-appliance.sh (which applies it to both the entries
# already generated into the image and the template a structure migration would
# re-render them from) and hack/grub-unrestrict.bats, so the one regex that has
# to keep matching upstream's line shape is exercised by the test rather than
# copied into it.
#
# Why the flag is needed at all: the appliance sets GRUB `superusers`, which
# restricts editing, the command line AND every menu entry that is not marked
# --unrestricted. Without this the VM stops at a password prompt on every boot.
# See the long comment in inject-appliance.sh.
#
# Both shapes this must match, and they differ only in whether the fields are
# literal or Jinja placeholders:
#   menuentry "1.5-stream-2026.03" --id vyos-1A2B3C4D {
#   menuentry "{{ version_name }}" --id {{ version_uuid }} {
#
# Idempotent: an entry that already carries the flag is left alone, so a rerun
# (or a future second caller) cannot produce `--unrestricted --unrestricted`.
/--unrestricted/! s/^(menuentry .* --id .*[^ ]) [{]$/\1 --unrestricted {/
