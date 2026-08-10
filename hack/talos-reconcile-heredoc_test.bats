#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Execution-level regression test for the worker TalosConfigTemplate heredoc:
# it pins that the reconcile Job's unquoted heredoc still emits a valid config
# when talos.registryMirrors carries hostile free-form input (and by default).
#
# The reconcile Job applies the TalosConfigTemplate via an UNQUOTED
# `cat <<EOF | kubectl apply -f -` heredoc, so every line of its body is subject
# to shell parameter expansion and command substitution at Job runtime. The
# `talos.registryMirrors` knob renders free-form tenant-facing input into that
# heredoc. A helm-unittest string `matchRegex` cannot catch a heredoc that the
# shell refuses to emit (e.g. an unbalanced backtick from an un-escaped value OR
# from a stray literal comment): it never runs the shell. This test does.
#
# It renders the Job with a HOSTILE mirror endpoint (`$(...)` + a backtick),
# extracts the `cat <<EOF ... EOF` block, runs it through a real shell, and
# asserts the heredoc still emits the TalosConfigTemplate (non-empty) and that
# the hostile value rendered as a LITERAL (never command-substituted). On the
# pre-fix code (unescaped value, or the explanatory comment carrying raw
# backticks) the heredoc dies with "bad substitution: no closing backtick" and
# `cat` emits zero bytes, which this test catches; a `matchRegex` did not.
#
# Needs `helm` + `yq`; cozytest.sh runs from the repo root.
# Run with: hack/cozytest.sh hack/talos-reconcile-heredoc_test.bats
# -----------------------------------------------------------------------------

CHART=packages/apps/kubernetes

@test "worker TalosConfigTemplate heredoc emits with a hostile registryMirrors value (no shell break, value stays literal)" {
    work=$(mktemp -d)
    # Hostile endpoint: a command substitution and a backtick. Single-quoted so
    # THIS shell does not expand it before helm sees it.
    helm template t "$CHART" -n tenant-test -f "$CHART/tests/values/common.yaml" \
        --set 'talos.registryMirrors.ghcr\.io.endpoints[0]=http://m$(id)`x`y' \
        --show-only templates/talos/talos-reconcile-job.yaml \
        | yq 'select(.kind == "Job") | .spec.template.spec.containers[0].command[2]' \
        > "$work/cmd.sh"
    [ -s "$work/cmd.sh" ] || { echo "render produced no Job command" >&2; rm -rf "$work"; exit 1; }

    # Extract just the TalosConfigTemplate heredoc, dropping the kubectl pipe so we
    # exercise the shell's handling of the body without applying anything. The
    # rendered script dedents the heredoc to column 0, so the terminator is `EOF`.
    awk '
      /^cat <<EOF \| kubectl apply/ { print "cat <<EOF"; inblock=1; next }
      inblock && /^EOF$/            { print "EOF"; inblock=0; next }
      inblock                       { print }
    ' "$work/cmd.sh" > "$work/heredoc.sh"
    grep -q '^cat <<EOF$' "$work/heredoc.sh" || { echo "could not extract the heredoc" >&2; rm -rf "$work"; exit 1; }

    # Run the heredoc through a real shell. Unset ${RELEASE}/${CLUSTER_ID}/... just
    # expand to empty; the point is that the shell can emit the body at all.
    out=$(sh "$work/heredoc.sh" 2>"$work/err") || { echo "heredoc shell exited non-zero" >&2; cat "$work/err" >&2; rm -rf "$work"; exit 1; }

    # (a) Non-empty: on the pre-fix break `cat` emitted zero bytes.
    [ -n "$out" ] || { echo "heredoc emitted no output (shell refused the body)" >&2; cat "$work/err" >&2; rm -rf "$work"; exit 1; }
    # (b) It really is the worker config.
    printf '%s' "$out" | grep -q 'kind: TalosConfigTemplate' || { echo "heredoc output is not the TalosConfigTemplate" >&2; rm -rf "$work"; exit 1; }
    # (c) The hostile value rendered LITERALLY: the command substitution was NOT
    # executed (its literal text survives) and no `id` output leaked in.
    printf '%s' "$out" | grep -qF 'http://m$(id)`x`y' || { echo "hostile endpoint was not preserved literally (shell expansion leaked)" >&2; printf '%s\n' "$out" | grep -i ghcr >&2; rm -rf "$work"; exit 1; }

    rm -rf "$work"
}

@test "worker TalosConfigTemplate heredoc keeps the Talos image coordinates literal" {
    work=$(mktemp -d)
    # installerRepository and schematicID reach the same unquoted heredoc as the
    # mirror endpoint, one block above it, and are free-form strings in the schema.
    helm template t "$CHART" -n tenant-test -f "$CHART/tests/values/common.yaml" \
        --set 'talos.installerRepository=reg$(id)`x`y/installer' \
        --set 'talos.schematicID=sch$(id)`q`z' \
        --set 'talos.version=v1.13.6$(id)`v`w' \
        --show-only templates/talos/talos-reconcile-job.yaml \
        | yq 'select(.kind == "Job") | .spec.template.spec.containers[0].command[2]' \
        > "$work/cmd.sh"
    [ -s "$work/cmd.sh" ] || { echo "render produced no Job command" >&2; rm -rf "$work"; exit 1; }
    awk '
      /^cat <<EOF \| kubectl apply/ { print "cat <<EOF"; inblock=1; next }
      inblock && /^EOF$/            { print "EOF"; inblock=0; next }
      inblock                       { print }
    ' "$work/cmd.sh" > "$work/heredoc.sh"
    grep -q '^cat <<EOF$' "$work/heredoc.sh" || { echo "could not extract the heredoc" >&2; rm -rf "$work"; exit 1; }
    out=$(sh "$work/heredoc.sh" 2>"$work/err") || { echo "heredoc shell exited non-zero" >&2; cat "$work/err" >&2; rm -rf "$work"; exit 1; }
    [ -n "$out" ] || { echo "heredoc emitted no output" >&2; cat "$work/err" >&2; rm -rf "$work"; exit 1; }
    printf '%s' "$out" | grep -qF 'reg$(id)`x`y/installer' || { echo "installerRepository was not preserved literally" >&2; printf '%s\n' "$out" | grep -i installer >&2; rm -rf "$work"; exit 1; }
    printf '%s' "$out" | grep -qF 'sch$(id)`q`z' || { echo "schematicID was not preserved literally" >&2; printf '%s\n' "$out" | grep -i image >&2; rm -rf "$work"; exit 1; }
    # talos.version reaches this heredoc twice -- as talosVersion and as the
    # installer image tag -- and no chart validator constrains its shape, so it is
    # the one free-form value here that only the escaping protects.
    [ "$(printf '%s' "$out" | grep -cF 'v1.13.6$(id)`v`w')" -eq 2 ] || { echo "talos.version was not preserved literally at both sites" >&2; printf '%s\n' "$out" | grep -iE 'talosVersion|image:' >&2; rm -rf "$work"; exit 1; }
    rm -rf "$work"
}

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
        --set 'talos.schematicID=sch$(id)`q`z' \
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
    printf '%s' "$out" | grep -qF 'sch$(id)`q`z' || { echo "kubernetes-nodes schematicID was not preserved literally" >&2; rm -rf "$work"; exit 1; }
    [ "$(printf '%s' "$out" | grep -cF 'v1.13.6$(id)`v`w')" -eq 2 ] || { echo "kubernetes-nodes talos.version was not preserved literally at both sites" >&2; printf '%s\n' "$out" | grep -iE 'talosVersion|image:' >&2; rm -rf "$work"; exit 1; }
    rm -rf "$work"
}

@test "worker TalosConfigTemplate heredoc emits with the default (no registryMirrors)" {
    work=$(mktemp -d)
    helm template t "$CHART" -n tenant-test -f "$CHART/tests/values/common.yaml" \
        --show-only templates/talos/talos-reconcile-job.yaml \
        | yq 'select(.kind == "Job") | .spec.template.spec.containers[0].command[2]' \
        > "$work/cmd.sh"
    awk '
      /^cat <<EOF \| kubectl apply/ { print "cat <<EOF"; inblock=1; next }
      inblock && /^EOF$/            { print "EOF"; inblock=0; next }
      inblock                       { print }
    ' "$work/cmd.sh" > "$work/heredoc.sh"
    out=$(sh "$work/heredoc.sh" 2>/dev/null) || { echo "default heredoc shell exited non-zero" >&2; rm -rf "$work"; exit 1; }
    [ -n "$out" ] || { echo "default heredoc emitted no output" >&2; rm -rf "$work"; exit 1; }
    printf '%s' "$out" | grep -q 'kind: TalosConfigTemplate' || { echo "default heredoc output is not the TalosConfigTemplate" >&2; rm -rf "$work"; exit 1; }
    # Default renders no machine-level registries mirrors block.
    printf '%s' "$out" | grep -q 'mirrors:' && { echo "default unexpectedly rendered a mirrors block" >&2; rm -rf "$work"; exit 1; }
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
