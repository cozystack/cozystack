#!/bin/bash
# Emit a human-readable summary of "what is broken" to a single file.
# Reads the live cluster (not the report dir) so it can use kubectl JSONPath.
# Usage: cozyreport-summary.sh > summary.txt
#
# bash, not /bin/sh: we rely on `set -o pipefail` to surface failed kubectl
# calls in `kubectl ... | awk ...` chains as missing sections instead of
# silently-empty ones, and the e2e-sandbox image's /bin/sh (Ubuntu dash)
# does not implement pipefail. The call site wraps this script in `|| true`
# so a hard pipeline failure still produces a (truncated) summary.txt.
set -euo pipefail

echo "# Cozystack E2E Diagnostic Summary"
echo "Generated: $(date -Iseconds)"
echo

echo "## HelmReleases not Ready"
echo
if kubectl get crd helmreleases.helm.toolkit.fluxcd.io >/dev/null 2>&1; then
  kubectl get hr -A --no-headers 2>/dev/null \
    | awk '$4 != "True" {printf "  %s/%s — %s\n", $1, $2, $5}' \
    | head -40
fi
echo

echo "## Pods not Running/Succeeded"
echo
kubectl get pod -A --no-headers 2>/dev/null \
  | awk '$4 !~ /Running|Succeeded|Completed/ {printf "  %s/%s — %s (restarts=%s, age=%s)\n", $1, $2, $4, $5, $6}' \
  | head -40
echo

echo "## ImagePullBackOff / ErrImagePull"
echo
kubectl get pod -A --no-headers 2>/dev/null \
  | awk '$4 ~ /ImagePullBackOff|ErrImagePull/ {printf "  %s/%s — %s\n", $1, $2, $4}'
echo

echo "## Recent OOMKilled events (last 20)"
echo
# Catches kernel-OOM events emitted by kubelet at the node level. Misses
# cgroup-only container kills that exit 137 without escalating to a node
# OOMKilling event — those show up in the container-state section below.
kubectl get events -A --field-selector reason=OOMKilling --sort-by=.lastTimestamp 2>/dev/null \
  | tail -20
echo

echo "## Containers with OOMKilled lastState"
echo
# Complements the OOMKilling-event section above. Container statuses retain
# the lastState even when no node-level OOMKilling event was emitted (e.g.
# cgroup-only kills with no kernel memory-pressure broadcast), so this
# catches the silent-restart cases. Use go-template (not jsonpath) so the
# inner range can still reach pod-level metadata via the outer-scope $pod.
kubectl get pod -A -o go-template='{{range .items}}{{$pod := .}}{{range .status.containerStatuses}}{{if and .lastState.terminated (eq .lastState.terminated.reason "OOMKilled")}}  {{$pod.metadata.namespace}}/{{$pod.metadata.name}} container={{.name}} exitCode={{.lastState.terminated.exitCode}} finishedAt={{.lastState.terminated.finishedAt}}
{{end}}{{end}}{{range .status.initContainerStatuses}}{{if and .lastState.terminated (eq .lastState.terminated.reason "OOMKilled")}}  {{$pod.metadata.namespace}}/{{$pod.metadata.name}} initContainer={{.name}} exitCode={{.lastState.terminated.exitCode}} finishedAt={{.lastState.terminated.finishedAt}}
{{end}}{{end}}{{end}}' 2>/dev/null \
  | head -40
echo

echo "## Recent Warning events (top 30)"
echo
kubectl get events -A --field-selector type=Warning --sort-by=.lastTimestamp 2>/dev/null \
  | tail -30
echo

echo "## cert-manager: Certificates not Ready"
echo
if kubectl get crd certificates.cert-manager.io >/dev/null 2>&1; then
  kubectl get certificates.cert-manager.io -A --no-headers 2>/dev/null \
    | awk '$3 != "True" {printf "  %s/%s — Ready=%s\n", $1, $2, $3}'
fi
echo

echo "## Flux Sources not Ready"
echo
for kind in helmrepositories.source.toolkit.fluxcd.io ocirepositories.source.toolkit.fluxcd.io gitrepositories.source.toolkit.fluxcd.io externalartifacts.source.toolkit.fluxcd.io; do
  kubectl get crd "$kind" >/dev/null 2>&1 || continue
  short=${kind%%.*}
  kubectl get "$kind" -A -o jsonpath='{range .items[?(@.status.conditions[?(@.type=="Ready")].status!="True")]}  '"$short"' {.metadata.namespace}/{.metadata.name} — Ready={.status.conditions[?(@.type=="Ready")].status}{"\n"}{end}' 2>/dev/null
done
echo

echo "## Storage: PVCs not Bound, PVs not Bound"
echo
kubectl get pvc -A --no-headers 2>/dev/null | awk '$3 != "Bound" {printf "  PVC %s/%s — %s\n", $1, $2, $3}'
kubectl get pv --no-headers 2>/dev/null    | awk '$5 != "Bound" {printf "  PV %s — %s\n", $1, $5}'
echo

echo "## Node Conditions"
kubectl get nodes -o custom-columns=NAME:.metadata.name,READY:.status.conditions[?\(@.type==\"Ready\"\)].status,DISK:.status.conditions[?\(@.type==\"DiskPressure\"\)].status,MEM:.status.conditions[?\(@.type==\"MemoryPressure\"\)].status 2>/dev/null
