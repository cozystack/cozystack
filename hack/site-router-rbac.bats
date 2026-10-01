#!/usr/bin/env bats
# The site-router-controller's kubebuilder RBAC markers are documentation only:
# nothing generates config/rbac from them, and the role the controller actually
# runs with is hand-written in the chart. Two hand-maintained copies of one fact
# drift, and this one had: the marker block granted pods `patch` months after the
# grant was deliberately removed from the chart (and argued against at length in
# the comment right above it), missed that services had gained `list`, and never
# mentioned nodes at all. Every one of those is invisible until someone trusts
# the marker -- a reader deciding what the controller can do, or a future
# controller-gen run that would put the removed grant back.
#
# So this asserts the two agree, exactly, on every (group, resource, verbs,
# resourceNames) tuple. It is the cheap half of making the markers trustworthy;
# the other half would be generating one from the other, which the chart's
# templating and per-rule comments make more expensive than it is worth today.
#
# python3 is already required by hack/md-no-hardwrap.bats, and helm by the chart
# unit tests, so this adds no dependency to the unit lane.

@test "the site-router-controller RBAC markers match the chart-authored ClusterRole" {
  root=$(pwd)
  out=$(helm template rbac-check "$root/packages/system/site-router-controller") || {
    echo "helm template failed for packages/system/site-router-controller"
    return 1
  }
  printf '%s\n' "$out" | python3 "$root/hack/site-router-rbac-check.py" \
    "$root/internal/controller/siterouter/reconciler.go"
}
