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
# to shell parameter expansion and command substitution at Job runtime. A
# helm-unittest string `matchRegex` cannot catch a heredoc that the shell refuses
# to emit (e.g. an unbalanced backtick from an un-escaped value): it never runs
# the shell. These tests do, by extracting the `cat <<EOF ... EOF` block and
# running it.
#
# The chart's own invariant (see the INVARIANT note in
# packages/apps/kubernetes-nodes/templates/talos-reconcile-job.yaml) gives a field
# two ways to be safe here, and which one a field uses decides what to assert:
#
#   escaped     the value reaches the heredoc and is escaped for backslash,
#               dollar and backtick. Assert it survives a real shell as a
#               LITERAL. This covers talos.installerRepository and
#               talos.registryMirrors, both genuinely free-form.
#   validated   the value is refused at render before it can reach the heredoc.
#               Assert the refusal. This covers talos.schematicID and
#               talos.version, which also land in the worker DataVolume name and
#               the image URL, where an unquoted YAML scalar makes escaping the
#               wrong tool. templates/nodegroup.yaml pattern-checks them.
#
# A field that is in neither arm is the regression this file exists to catch.
#
# Needs `helm` + `yq`; cozytest.sh runs from the repo root.
# Run with: hack/cozytest.sh hack/talos-reconcile-heredoc_test.bats
# -----------------------------------------------------------------------------

@test "kubernetes-nodes refuses hostile Talos image coordinates at render" {
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
    # schematicID and version are not escaped into safety, they are refused. They
    # are also interpolated into the worker DataVolume name and its source URL,
    # where the scalar is unquoted and escaping buys nothing, so the render is the
    # only place that can stop them.
    for f in 'talos.schematicID=sch$(id)`q`z' 'talos.version=v1.13.6$(id)`v`w'; do
        if helm template kubernetes-nodes-myk8s-md0 packages/apps/kubernetes-nodes \
            -n tenant-test -f "$work/vals.yaml" --set "$f" >/dev/null 2>"$work/err"; then
            echo "render accepted a hostile ${f%%=*}, so the value reaches the DataVolume name and the heredoc" >&2
            rm -rf "$work"; exit 1
        fi
        grep -qE 'is not a plain lowercase alphanumeric identifier|is not a vMAJOR\.MINOR\.PATCH release' "$work/err" \
            || { echo "render failed on ${f%%=*} for some other reason:" >&2; cat "$work/err" >&2; rm -rf "$work"; exit 1; }
    done
    rm -rf "$work"
}

@test "kubernetes-nodes worker TalosConfigTemplate heredoc keeps a hostile installerRepository literal" {
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
    # installerRepository is an OCI repository prefix and stays free-form: it is
    # not part of the DataVolume name, so escaping is the right tool and this is
    # the assertion that proves it still works.
    helm template kubernetes-nodes-myk8s-md0 packages/apps/kubernetes-nodes -n tenant-test -f "$work/vals.yaml" \
        --set 'talos.installerRepository=reg$(id)`x`y/installer' \
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
    # The default version still reaches both of its sites, so narrowing this test
    # to installerRepository did not quietly drop the two-site assertion.
    [ "$(printf '%s' "$out" | grep -cF 'v1.13.6')" -ge 2 ] || { echo "kubernetes-nodes talos.version did not reach both sites" >&2; printf '%s\n' "$out" | grep -iE 'talosVersion|image:' >&2; rm -rf "$work"; exit 1; }
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
