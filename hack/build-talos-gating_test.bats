#!/usr/bin/env bats
# The Talos build is the only job on the PR path whose output nothing on that
# path consumes, so it is gated. These hold the gate and the two couplings that
# make it safe to skip.

WF=.github/workflows/pull-requests.yaml

@test "the Talos build runs only when its sources changed, or for a fork" {
  gate=$(yq '.jobs["build-talos"].if' "$WF")

  # The touched list is space-joined by the plan job, so the trailing space is
  # what keeps this from matching a future packages/core/talos-something.
  case "$gate" in
    *"contains(needs.plan.outputs.touched, 'packages/core/talos ')"*) ;;
    *)
      echo "build-talos is not gated on the plan's touched list: $gate" >&2
      return 1
      ;;
  esac
  # Forks keep the unconditional build until e2e-fork.yaml's publish guard stops
  # depending on an archive always being there. Losing this arm fails a fork PR
  # that touches nothing under packages/, which is a legitimate PR.
  case "$gate" in
    *'github.event.pull_request.head.repo.fork'*) ;;
    *)
      echo "build-talos lost its fork escape; e2e-fork publish requires >=1 OCI archive: $gate" >&2
      return 1
      ;;
  esac
}

@test "finalize tolerates a skipped Talos build" {
  # A skipped job's result is 'skipped', not 'success'. Gating on success alone
  # would block finalize -- and therefore e2e -- on every PR that does not touch
  # packages/core/talos, which is nearly all of them.
  gate=$(yq '.jobs.finalize.if' "$WF")

  case "$gate" in
    *"needs.build-talos.result == 'skipped'"*) ;;
    *)
      echo "finalize does not tolerate a skipped build-talos, so skipping it blocks every PR: $gate" >&2
      return 1
      ;;
  esac
  if ! printf '%s\n' "$gate" | grep -Fq "needs.build-talos.result == 'success'"; then
    echo "finalize stopped requiring a SUCCESSFUL build-talos when one ran: $gate" >&2
    return 1
  fi
}

@test "nothing on the PR path downloads a Talos artifact" {
  # The gate is only safe while this holds. A step that reads talos-image or the
  # nocloud disk would fail on every run where the build was skipped.
  if grep -nE 'name: talos-image|nocloud-amd64\.raw\.xz' "$WF" | grep -v 'const diskId'; then
    echo "a PR-path step consumes a Talos artifact that the gate can skip building" >&2
    return 1
  fi
}
