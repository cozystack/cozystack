#!/usr/bin/env bats

load test_helper

# collect_image_refs only sees digest-pinned references, so a bare tag is
# invisible to promote, retag and mirror until the first build stamps it.
@test "the worker controller tag names the image its package builds" {
  dir=packages/system/kubernetes-worker-classes
  registry=$(sed -n 's/^REGISTRY ?= //p' hack/common-envs.mk)
  repo="$registry/kubernetes-worker-controller"
  grep --fixed-strings --quiet 'echo "$(REGISTRY)/kubernetes-worker-controller:$(IMAGE_TAG)@' "$dir/Makefile"
  tag=$(cat "$dir/images/kubernetes-worker-controller.tag")
  case "$tag" in
    "$repo":*) ;;
    *) echo "images/kubernetes-worker-controller.tag '$tag' is not in $repo" >&2; false ;;
  esac
  # Bare until the first build stamps its digest.
  printf '%s\n' "${tag#"$repo":}" | grep --extended-regexp --quiet '^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}(@sha256:[0-9a-f]{64})?$'
  rendered=$(helm template worker-classes "$dir" | yq 'select(.kind == "Deployment") | .spec.template.spec.containers[].image' | sort -u)
  [ "$rendered" = "$tag" ]
}
