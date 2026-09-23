#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Execution-level regression test for the worker TalosConfigTemplate heredoc:
# it pins that the reconcile Job's unquoted heredoc still emits a valid config
# when talos.registryMirrors (and the Talos image coordinates) carry hostile
# free-form input.
#
# Since the Phase 2 split the worker pool, its talos-reconcile Job, and the
# TalosConfigTemplate it applies live only in the kubernetes-nodes chart; the
# parent kubernetes chart no longer renders a worker reconcile Job. These tests
# therefore render packages/apps/kubernetes-nodes.
#
# The reconcile Job applies the TalosConfigTemplate via an UNQUOTED
# `cat <<EOF | kubectl apply -f -` heredoc, so every line of its body is subject
# to shell parameter expansion and command substitution at Job runtime. The
# `talos.registryMirrors` knob and the Talos image coordinates render free-form
# tenant-facing input into that heredoc. A helm-unittest string `matchRegex`
# cannot catch a heredoc that the shell refuses to emit (e.g. an unbalanced
# backtick from an un-escaped value): it never runs the shell. This test does.
#
# It renders the Job with HOSTILE values (`$(...)` + a backtick), extracts the
# `cat <<EOF ... EOF` block, runs it through a real shell, and asserts the
# heredoc still emits the TalosConfigTemplate (non-empty) and that the hostile
# value rendered as a LITERAL (never command-substituted).
#
# The INVARIANT recorded above the data block admits two protections, escaping or
# render-time validation, and both are exercised here. talos.version and
# talos.installerRepository have only the escaping, so for them the assertion is
# that the heredoc emits and the value survives verbatim. talos.schematicID is
# guarded at render time as well, so for it the assertion is the opposite: the
# render is refused and no heredoc is produced. Its escape chain stays in the
# template as a second line of defence, unreachable for hostile input while the
# guard holds -- a benign schematic carrying a `/` still rides the same site here,
# so the site itself stays covered.
#
# Needs `helm` + `yq`; cozytest.sh runs from the repo root.
# Run with: hack/cozytest.sh hack/talos-reconcile-heredoc_test.bats
# -----------------------------------------------------------------------------

@test "kubernetes-nodes worker TalosConfigTemplate heredoc keeps the Talos image coordinates literal" {
    work=$(mktemp -d)
    cat > "$work/vals.yaml" <<'VALS'
cluster: myk8s
_cluster:
  cluster-domain: cozy.local
version: "v1.35"
minReplicas: 0
maxReplicas: 3
instanceType: ""
diskSize: 20Gi
storageClass: replicated
roles: [ingress-nginx]
resources: {cpu: "2", memory: 4Gi}
VALS
    helm template kubernetes-nodes-myk8s-md0 packages/apps/kubernetes-nodes -n tenant-test -f "$work/vals.yaml" \
        --set 'talos.installerRepository=reg$(id)`x`y/installer' \
        --set 'talos.schematicID=gpu/nvidia-open' \
        --set 'talos.version=v1.13.6$(id)`v`w' \
        --show-only templates/talos-reconcile-job.yaml \
        | yq 'select(.kind == "Job") | .spec.template.spec.containers[0].command[2]' \
        > "$work/cmd.sh"
    [ -s "$work/cmd.sh" ] || { echo "kubernetes-nodes render produced no Job command" >&2; rm -rf "$work"; exit 1; }
    awk '
      /^cat <<EOF \| kubectl apply/ { print "cat <<EOF"; inblock=1; next }
      inblock && /^EOF$/            { print "EOF"; inblock=0; next }
      inblock                       { print }
    ' "$work/cmd.sh" > "$work/heredoc.sh"
    grep -q '^cat <<EOF$' "$work/heredoc.sh" || { echo "could not extract the kubernetes-nodes heredoc" >&2; rm -rf "$work"; exit 1; }
    out=$(sh "$work/heredoc.sh" 2>"$work/err") || { echo "kubernetes-nodes heredoc shell exited non-zero" >&2; cat "$work/err" >&2; rm -rf "$work"; exit 1; }
    [ -n "$out" ] || { echo "kubernetes-nodes heredoc emitted no output" >&2; rm -rf "$work"; exit 1; }
    printf '%s' "$out" | grep -qF 'reg$(id)`x`y/installer' || { echo "kubernetes-nodes installerRepository was not preserved literally" >&2; rm -rf "$work"; exit 1; }
    printf '%s' "$out" | grep -qF 'gpu/nvidia-open' || { echo "kubernetes-nodes schematicID was not preserved literally" >&2; rm -rf "$work"; exit 1; }
    [ "$(printf '%s' "$out" | grep -cF 'v1.13.6$(id)`v`w')" -eq 2 ] || { echo "kubernetes-nodes talos.version was not preserved literally at both sites" >&2; printf '%s\n' "$out" | grep -iE 'talosVersion|image:' >&2; rm -rf "$work"; exit 1; }
    rm -rf "$work"
}

@test "kubernetes-nodes worker TalosConfigTemplate heredoc emits with a hostile registryMirrors value" {
    work=$(mktemp -d)
    cat > "$work/vals.yaml" <<'VALS'
cluster: myk8s
_cluster:
  cluster-domain: cozy.local
version: "v1.35"
minReplicas: 0
maxReplicas: 3
instanceType: ""
diskSize: 20Gi
storageClass: replicated
roles: [ingress-nginx]
resources: {cpu: "2", memory: 4Gi}
VALS
    helm template kubernetes-nodes-myk8s-md0 packages/apps/kubernetes-nodes -n tenant-test -f "$work/vals.yaml" \
        --set 'talos.registryMirrors.ghcr\.io.endpoints[0]=http://m$(id)`x`y' \
        --show-only templates/talos-reconcile-job.yaml \
        | yq 'select(.kind == "Job") | .spec.template.spec.containers[0].command[2]' \
        > "$work/cmd.sh"
    [ -s "$work/cmd.sh" ] || { echo "kubernetes-nodes render produced no Job command" >&2; rm -rf "$work"; exit 1; }
    awk '
      /^cat <<EOF \| kubectl apply/ { print "cat <<EOF"; inblock=1; next }
      inblock && /^EOF$/            { print "EOF"; inblock=0; next }
      inblock                       { print }
    ' "$work/cmd.sh" > "$work/heredoc.sh"
    grep -q '^cat <<EOF$' "$work/heredoc.sh" || { echo "could not extract the kubernetes-nodes heredoc" >&2; rm -rf "$work"; exit 1; }
    out=$(sh "$work/heredoc.sh" 2>"$work/err") || { echo "kubernetes-nodes heredoc shell exited non-zero" >&2; cat "$work/err" >&2; rm -rf "$work"; exit 1; }
    [ -n "$out" ] || { echo "kubernetes-nodes heredoc emitted no output" >&2; cat "$work/err" >&2; rm -rf "$work"; exit 1; }
    printf '%s' "$out" | grep -q 'kind: TalosConfigTemplate' || { echo "kubernetes-nodes heredoc output is not the TalosConfigTemplate" >&2; rm -rf "$work"; exit 1; }
    printf '%s' "$out" | grep -qF 'http://m$(id)`x`y' || { echo "kubernetes-nodes hostile endpoint not preserved literally" >&2; rm -rf "$work"; exit 1; }
    rm -rf "$work"
}

# The other half of the INVARIANT above the data block: a field may be protected
# by escaping OR by render-time validation. schematicID takes the second route,
# so the execution-level check for it is that no heredoc is produced at all.

@test "kubernetes-nodes TalosConfigTemplate render rejects a hostile schematicID before any heredoc exists" {
    work=$(mktemp -d)
    cat > "$work/vals.yaml" <<'VALS'
cluster: myk8s
_cluster:
  cluster-domain: cozy.local
version: "v1.35"
minReplicas: 0
maxReplicas: 3
instanceType: ""
diskSize: 20Gi
storageClass: replicated
roles: [ingress-nginx]
resources: {cpu: "2", memory: 4Gi}
VALS
    if helm template kubernetes-nodes-myk8s-md0 packages/apps/kubernetes-nodes -n tenant-test -f "$work/vals.yaml" \
        --set 'talos.schematicID=sch$(id)`q`z' \
        --show-only templates/talos-reconcile-job.yaml >"$work/out" 2>"$work/err"; then
        echo "kubernetes-nodes hostile schematicID rendered instead of being rejected" >&2; cat "$work/out" >&2; rm -rf "$work"; exit 1
    fi
    grep -q 'invalid schematicID' "$work/err" || { echo "kubernetes-nodes render failed for a reason other than the schematicID guard" >&2; cat "$work/err" >&2; rm -rf "$work"; exit 1; }
    rm -rf "$work"
}

@test "kubernetes-nodes TalosConfigTemplate render rejects a hostile per-pool schematicID too" {
    work=$(mktemp -d)
    cat > "$work/vals.yaml" <<'VALS'
cluster: myk8s
_cluster:
  cluster-domain: cozy.local
version: "v1.35"
minReplicas: 0
maxReplicas: 3
instanceType: ""
diskSize: 20Gi
storageClass: replicated
roles: [ingress-nginx]
resources: {cpu: "2", memory: 4Gi}
VALS
    if helm template kubernetes-nodes-myk8s-md0 packages/apps/kubernetes-nodes -n tenant-test -f "$work/vals.yaml" \
        --set 'schematicID=sch$(id)`q`z' \
        --show-only templates/talos-reconcile-job.yaml >"$work/out" 2>"$work/err"; then
        echo "kubernetes-nodes hostile per-pool schematicID rendered instead of being rejected" >&2; cat "$work/out" >&2; rm -rf "$work"; exit 1
    fi
    grep -q 'invalid schematicID' "$work/err" || { echo "kubernetes-nodes render failed for a reason other than the schematicID guard" >&2; cat "$work/err" >&2; rm -rf "$work"; exit 1; }
    rm -rf "$work"
}
