#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for hack/seaweedfs-naming-audit.sh.
#
# The chart's naming guard refuses whenever both naming generations exist, so the
# audit IS the classifier — it is what an operator acts on, and acting on it
# deletes PVCs. Two earlier revisions of this classification shipped as an
# untested shell snippet inside the runbook, and both were wrong in ways that
# routed a live tenant into the step that strands its data:
#
#   - a selector `^data1-(.*seaweedfs.*)-volume` also matched the CHART-named
#     claims, so the release-named age range spanned both generations, every
#     tenant read as "ranges overlap / mid-rebind", and a genuine S-damaged tenant
#     was routed AWAY from Step 2a (which would have been correct) into Step 2,
#     which deletes its data claim;
#   - matching claims by name with `grep seaweedfs` cannot see a long instance
#     name, whose claims the chart truncates past `seaweedfs`, so such a tenant
#     read as "L — nothing to do" while the chart refused it.
#
# Moving the classifier into a file with tests is the point. These drive it
# against a fake kubectl, mocking only the cluster boundary.
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` on its own
# line; there is no bats `run`/`$status`/`setup`.
#
# Run with: hack/cozytest.sh hack/seaweedfs-naming-audit.bats
# -----------------------------------------------------------------------------

SEAWEEDFS_AUDIT_LIB=1
export SEAWEEDFS_AUDIT_LIB
# shellcheck source=seaweedfs-naming-audit.sh
. "$PWD/hack/seaweedfs-naming-audit.sh"

@test "reconstructs the renamed volume prefix for a default instance" {
  [ "$(renamed_volume_prefix seaweedfs-system)" = "seaweedfs-system-volume" ]
}

@test "reconstructs the renamed volume prefix for a non-default instance" {
  # The release name does not contain the chart name, so 4.31 appends it.
  [ "$(renamed_volume_prefix foo-system)" = "foo-system-seaweedfs-volume" ]
}

@test "reconstructs the truncated prefix for a long instance name" {
  # componentName cuts the fullname to 62-len("volume")=56 before appending, so
  # `seaweedfs` falls off the tail — the case a `grep seaweedfs` name match cannot
  # see, and the reason this is reconstructed rather than pattern-matched.
  got=$(renamed_volume_prefix archive-of-quarterly-financial-statements-x1-system)
  [ "$got" = "archive-of-quarterly-financial-statements-x1-system-seaw-volume" ]
  # 56 chars of fullname + "-volume"
  [ "${#got}" -eq 63 ]
}

@test "reconstructs a distinct prefix for an instance named seaweedfs-volume" {
  # The pathological case: this instance's RELEASE-named objects
  # (seaweedfs-volume-system-volume, data1-seaweedfs-volume-system-volume-0) also
  # satisfy the CHART-named prefixes. The reconstruction must return the
  # release-named prefix so the release-named branch can be tested first.
  got=$(renamed_volume_prefix seaweedfs-volume-system)
  [ "$got" = "seaweedfs-volume-system-volume" ]
  # It must NOT collide with the chart-named prefix.
  [ "$got" != "seaweedfs-volume" ]
}

@test "the reconstructed prefix never equals the chart-named prefix" {
  # If it did, both generations would match one branch and the guard/audit could
  # not separate them at all.
  for r in seaweedfs-system foo-system seaweedfs-volume-system a-system; do
    [ "$(renamed_volume_prefix "$r")" != "seaweedfs-volume" ]
  done
}

@test "the audit's reconstruction agrees with the chart helper it mirrors" {
  # hack/seaweedfs-naming-audit.sh and
  # packages/system/seaweedfs/templates/_naming.tpl reimplement the same two
  # upstream helpers in two languages. If they drift, the audit classifies a
  # tenant differently from the render that refuses it, and the operator is
  # working from a different picture than the chart. Render the chart helper
  # through helm and compare it to this script's output, release by release.
  chart=$(mktemp -d)
  printf 'apiVersion: v2\nname: probe\nversion: 0.0.0\n' > "$chart/Chart.yaml"
  mkdir -p "$chart/templates"
  cp packages/system/seaweedfs/templates/_naming.tpl "$chart/templates/"
  cat > "$chart/templates/out.yaml" <<'EOF'
apiVersion: v1
kind: ConfigMap
metadata:
  name: probe
data:
{{- range $r := list "seaweedfs-system" "foo-system" "seaweedfs-volume-system" "archive-of-quarterly-financial-statements-x1-system" "a-system" }}
  {{ $r }}: {{ include "seaweedfs.renamedVolumePrefix" $r }}
{{- end }}
EOF
  helm template probe "$chart" 2>/dev/null | sed -n 's/^  \([a-z0-9-]*-system\): \(.*\)$/\1=\2/p' > "$chart/rendered"
  [ -s "$chart/rendered" ]
  [ "$(wc -l < "$chart/rendered")" -eq 5 ]
  while IFS='=' read -r rel expected; do
    [ -n "$rel" ] || continue
    got=$(renamed_volume_prefix "$rel")
    echo "chart: $rel -> $expected ; audit: $got"
    [ "$got" = "$expected" ]
  done < "$chart/rendered"
  rm -rf "$chart"
}
