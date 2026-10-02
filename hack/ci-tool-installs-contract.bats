#!/usr/bin/env bats
# The flux CLI, yq and the helm-unittest plugin are build inputs like any image
# or action, so they are pinned the same way: a fixed version, and for flux and
# yq bytes checked against the release that version names. A floating install
# comes back by copy-paste from an older step rather than by anyone deciding to
# unpin one, which is why this scans every workflow instead of the steps that
# happened to be fixed. Other downloaded tools are not covered here.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
WORKFLOWS="$REPO_ROOT/.github/workflows"

# Prints file:line:text for every non-comment workflow line matching the ERE.
workflow_code() {
  grep --recursive --line-number --extended-regexp "$1" "$WORKFLOWS" \
    | grep --invert-match --extended-regexp '^[^:]+:[0-9]+:[[:space:]]*#' || true
}

@test "no workflow executes the fluxcd.io install script" {
  # It runs as root whatever fluxcd.io serves at that moment; the binary it
  # checksums is not the exposure, the unpinned script is.
  hits="$(workflow_code 'fluxcd\.io/install\.sh')"
  [ -z "$hits" ] || { printf '%s\n' "$hits"; false; }
}

@test "every use of the flux setup action is SHA-pinned at one version" {
  uses="$(workflow_code 'uses: fluxcd/flux2/action@')"
  [ -n "$uses" ]
  bad="$(printf '%s\n' "$uses" | grep --invert-match --extended-regexp 'action@[0-9a-f]{40} # v[0-9.]+$' || true)"
  [ -z "$bad" ] || { printf '%s\n' "$bad"; false; }

  # Without a version input the action resolves the latest release itself, and
  # a digest bump moves the ref and its comment but not the input, so each
  # step's input must sit in its own `with:` and match the tag in the comment.
  # One "sha tag input" triple per step; all steps must agree on it.
  steps="$(awk '
    FNR == 1 { if (pend) print sha, tag, "missing"; pend = 0 }
    /^[[:space:]]*#/ { next }
    /uses: fluxcd\/flux2\/action@/ {
      sha = $0; sub(/.*action@/, "", sha); sub(/ .*/, "", sha)
      tag = $0; sub(/.*# v/, "", tag)
      pend = 1; next
    }
    pend && /version: "/ {
      v = $0; sub(/.*version: "/, "", v); sub(/".*/, "", v)
      print sha, tag, v; pend = 0; next
    }
    pend && /^[[:space:]]*- / { print sha, tag, "missing"; pend = 0 }
    END { if (pend) print sha, tag, "missing" }
  ' "$WORKFLOWS"/*.yaml "$WORKFLOWS"/*.yml 2>/dev/null)"
  [ "$(printf '%s\n' "$steps" | grep --count .)" -eq "$(printf '%s\n' "$uses" | grep --count .)" ]
  bad="$(printf '%s\n' "$steps" | awk '$2 != $3' || true)"
  [ -z "$bad" ] || { printf '%s\n' "$bad"; false; }
  [ "$(printf '%s\n' "$steps" | sort --unique | wc -l | tr -d ' ')" -eq 1 ] \
    || { printf '%s\n' "$steps" | sort --unique; false; }
}

@test "no workflow downloads from a floating latest release" {
  hits="$(workflow_code 'releases/latest/download')"
  [ -z "$hits" ] || { printf '%s\n' "$hits"; false; }
}

@test "every yq download is checksum-verified at one version" {
  [ -n "$(workflow_code 'mikefarah/yq/releases/download')" ]
  # Each download, not each file: the check has to follow within three lines.
  unchecked="$(awk '
    FNR == 1 { if (pend) print pend; pend = "" }
    /^[[:space:]]*#/ { next }
    /mikefarah\/yq\/releases\/download/ { if (pend) print pend; pend = FILENAME ":" FNR; n = 0; next }
    pend && /sha256sum -c -/ && /YQ_SHA256/ { pend = ""; next }
    pend && ++n >= 3 { print pend; pend = "" }
    END { if (pend) print pend }
  ' "$WORKFLOWS"/*.yaml "$WORKFLOWS"/*.yml 2>/dev/null)"
  [ -z "$unchecked" ] || { printf '%s\n' "$unchecked"; false; }
  for key in YQ_VERSION YQ_SHA256; do
    values="$(workflow_code "$key: \"" | grep --only-matching "$key: \"[0-9a-f.]*\"" | sort --unique)"
    [ "$(printf '%s\n' "$values" | grep --count .)" -eq 1 ] || { printf '%s\n' "$values"; false; }
  done
}

@test "every helm plugin install names a version" {
  # helm-unittest embeds its own Helm, so an unpinned plugin moves the renderer
  # every chart test runs against without any PR asking for it.
  installs="$(workflow_code 'helm plugin install')"
  [ -n "$installs" ]
  bad="$(printf '%s\n' "$installs" | grep --invert-match -- '--version' || true)"
  [ -z "$bad" ] || { printf '%s\n' "$bad"; false; }
}
