# shellcheck shell=sh
# The finalizer the computeplane teardown holds the KamajiControlPlane with,
# sourced by the teardown script that takes it and by the separate operation
# that releases it.
#
# The release tests that the entry it removes is still this suite's. The hold
# tests the finalizer list it extends when there is one; with none it creates
# the list untested, on an object that nothing else puts finalizers on.

kcp_ns=tenant-cplane
kcp_ref=kamajicontrolplanes.controlplane.cluster.x-k8s.io/computeplane-cluster
kcp_hold_finalizer=e2e.cozystack.io/teardown-order

# Returns 2 when there is no KamajiControlPlane to hold.
kcp_hold() {
  _cur=$(timeout 30 kubectl -n "$kcp_ns" get "$kcp_ref" --ignore-not-found -o json) || return 1
  [ -n "$_cur" ] || return 2
  _patch=$(printf '%s' "$_cur" | jq -c --arg h "$kcp_hold_finalizer" '
    if .metadata.finalizers == null
    then [{"op":"add","path":"/metadata/finalizers","value":[$h]}]
    else [{"op":"test","path":"/metadata/finalizers","value":.metadata.finalizers},
          {"op":"add","path":"/metadata/finalizers/-","value":$h}] end')
  timeout 30 kubectl -n "$kcp_ns" patch "$kcp_ref" --type json --patch "$_patch" >/dev/null
}

# Succeeds when the finalizer is gone afterwards, which includes the object
# being gone and the finalizer never having been there.
kcp_release() {
  _cur=$(timeout 30 kubectl -n "$kcp_ns" get "$kcp_ref" --ignore-not-found -o json) || return 1
  [ -n "$_cur" ] || return 0
  _patch=$(printf '%s' "$_cur" | jq -c --arg h "$kcp_hold_finalizer" '
    ((.metadata.finalizers // []) | index($h)) as $i
    | if $i == null then empty
      else [{"op":"test","path":"/metadata/finalizers/\($i)","value":$h},
            {"op":"remove","path":"/metadata/finalizers/\($i)"}] end')
  [ -n "$_patch" ] || return 0
  timeout 30 kubectl -n "$kcp_ns" patch "$kcp_ref" --type json --patch "$_patch" >/dev/null || return 1
  echo "» released $kcp_hold_finalizer on $kcp_ref"
}
