#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Execution-level regression test for the worker TalosConfigTemplate heredoc:
# it pins that the reconcile Job's unquoted heredoc still emits a valid config
# when talos.registryMirrors, talos.sysctls and the Talos image coordinates
# carry hostile free-form input.
#
# Since the Phase 2 split the worker pool, its talos-reconcile Job, and the
# TalosConfigTemplate it applies live only in the kubernetes-nodes chart; the
# parent kubernetes chart no longer renders a worker reconcile Job. These tests
# therefore render packages/apps/kubernetes-nodes.
#
# The reconcile Job applies the TalosConfigTemplate via an UNQUOTED `cat <<EOF`
# heredoc, captured into a variable and then applied, so every line of its body
# is subject to shell parameter expansion and command substitution at Job
# runtime. The `talos.registryMirrors` and `talos.sysctls` knobs and the Talos
# image coordinates render free-form tenant-facing input into that heredoc. A helm-unittest
# string `matchRegex` cannot catch a heredoc that the shell refuses to emit
# (e.g. an unbalanced backtick from an un-escaped value): it never runs the
# shell. This test does.
#
# The chart's own invariant (see the INVARIANT note in
# packages/apps/kubernetes-nodes/templates/talos-reconcile-job.yaml) gives a field
# two ways to be safe here, and which one a field uses decides what to assert:
#
#   escaped     the value reaches the heredoc and is escaped for backslash,
#               dollar and backtick. Assert it survives a real shell as a
#               LITERAL. This covers talos.installerRepository and
#               talos.registryMirrors and talos.sysctls (both keys and values).
#   validated   the value is refused at render before it can reach the heredoc.
#               Assert the refusal. This covers talos.schematicID and
#               talos.version, which also land in the worker DataVolume name and
#               the image URL, where an unquoted YAML scalar makes escaping the
#               wrong tool. templates/nodegroup.yaml pattern-checks them. It also
#               covers kernelModules, whose names and parameters
#               templates/_helpers.tpl checks before rebuilding the list.
#               Sysctl map/value types and UTF-8 are validated before escaping.
#
# A field that is in neither arm is the regression this file exists to catch.
#
# Needs `helm`, `yq`, Bash and Dash and must run from the repo root.
# Run with: bats hack/talos-reconcile-heredoc_test.bats
# -----------------------------------------------------------------------------

load test_helper

render_sysctl_pool() {
    out="$1"; shift
    helm template kubernetes-nodes-myk8s-md0 packages/apps/kubernetes-nodes \
        --namespace tenant-test \
        --set cluster=myk8s \
        --set _cluster.cluster-domain=cozy.local \
        --set version=v1.35 \
        --set minReplicas=0 --set maxReplicas=3 \
        --set instanceType="" \
        --set diskSize=20Gi --set storageClass=replicated \
        --set 'roles[0]=ingress-nginx' \
        --set resources.cpu=2 --set resources.memory=4Gi \
        "$@" > "$out" 2>"$out.err"
    [ -s "$out" ] || { cat "$out.err" >&2; return 1; }
}

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
      /(^|=\"?\$\()cat <<EOF/ { print "cat <<EOF"; inblock=1; next }
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
      /(^|=\"?\$\()cat <<EOF/ { print "cat <<EOF"; inblock=1; next }
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

# The proxmox substrate adds the only tenant-controlled value in this heredoc
# that is not shell-escaped: proxmox.dnsServers. `quote` there is YAML quoting
# and leaves $(...) and backticks for the Job's shell to run — rendered with a
# substitution, the shell executed it and its output ended up in the worker's
# resolver list. The fix is render-time validation rather than escaping, because
# Talos takes addresses here and an address cannot carry a metacharacter, so
# these two tests hold both halves: the render refuses what is not an address,
# and what is an address survives the shell untouched.
@test "kubernetes-nodes refuses a proxmox dnsServers entry that is not an address" {
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
substrate: proxmox
proxmox:
  templateTags: [talos]
  network: {bridge: vmbr0}
  dnsServers: ["10.0.0.1", "$(id -u)"]
VALS
    if helm template kubernetes-nodes-myk8s-md0 packages/apps/kubernetes-nodes -n tenant-test -f "$work/vals.yaml" \
        --show-only templates/talos-reconcile-job.yaml > "$work/out.yaml" 2>"$work/err"; then
        echo "render accepted a command substitution in proxmox.dnsServers" >&2
        rm -rf "$work"; exit 1
    fi
    grep -qF 'proxmox.dnsServers[1]' "$work/err" || { echo "refusal does not name the offending entry" >&2; cat "$work/err" >&2; rm -rf "$work"; exit 1; }
    rm -rf "$work"
}

# The validator has two branches and a value without a colon never reaches the
# second one. This case does: `::1$(id -u)` looks enough like an IPv6 address to
# take the IPv6 path, so it is what pins that path's character class. Widen the
# class and this entry renders, with the substitution back in the heredoc, while
# the case above still passes on its IPv4 branch.
@test "kubernetes-nodes refuses an IPv6-shaped dnsServers entry carrying a substitution" {
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
substrate: proxmox
proxmox:
  templateTags: [talos]
  network: {bridge: vmbr0}
  dnsServers: ["10.0.0.1", "::1$(id -u)"]
VALS
    if helm template kubernetes-nodes-myk8s-md0 packages/apps/kubernetes-nodes -n tenant-test -f "$work/vals.yaml" \
        --show-only templates/talos-reconcile-job.yaml > "$work/out.yaml" 2>"$work/err"; then
        echo "render accepted a substitution inside an IPv6-shaped entry" >&2
        rm -rf "$work"; exit 1
    fi
    grep -qF 'proxmox.dnsServers[1]' "$work/err" || { echo "refusal does not name the IPv6-shaped entry" >&2; cat "$work/err" >&2; rm -rf "$work"; exit 1; }
    rm -rf "$work"
}

@test "kubernetes-nodes proxmox heredoc emits the nameservers it was given" {
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
substrate: proxmox
proxmox:
  templateTags: [talos]
  network: {bridge: vmbr0}
  dnsServers: ["10.0.0.1", "2001:4860:4860::8888"]
VALS
    helm template kubernetes-nodes-myk8s-md0 packages/apps/kubernetes-nodes -n tenant-test -f "$work/vals.yaml" \
        --show-only templates/talos-reconcile-job.yaml \
        | yq 'select(.kind == "Job") | .spec.template.spec.containers[0].command[2]' \
        > "$work/cmd.sh"
    [ -s "$work/cmd.sh" ] || { echo "proxmox render produced no Job command" >&2; rm -rf "$work"; exit 1; }
    awk '
      # Matched on shape: `cat <<EOF` at the start of a line, or right after
      # `=$(` / `="$(` when the heredoc is captured into a variable.
      /(^|=\"?\$\()cat <<EOF/ { print "cat <<EOF"; inblock=1; next }
      inblock && /^EOF$/            { print "EOF"; inblock=0; next }
      inblock                       { print }
    ' "$work/cmd.sh" > "$work/heredoc.sh"
    grep -q '^cat <<EOF$' "$work/heredoc.sh" || { echo "could not extract the proxmox heredoc" >&2; rm -rf "$work"; exit 1; }
    out=$(sh "$work/heredoc.sh" 2>"$work/err") || { echo "proxmox heredoc shell exited non-zero" >&2; cat "$work/err" >&2; rm -rf "$work"; exit 1; }
    printf '%s' "$out" | grep -qF -e '- "10.0.0.1"' || { echo "IPv4 nameserver missing from the emitted config" >&2; rm -rf "$work"; exit 1; }
    printf '%s' "$out" | grep -qF -e '- "2001:4860:4860::8888"' || { echo "IPv6 nameserver missing from the emitted config" >&2; rm -rf "$work"; exit 1; }
    rm -rf "$work"
}

# kernelModules is in the validated arm: its names and parameters are refused at
# render, and the list is rebuilt from those two fields so no other key reaches
# the heredoc. The refusal and the one character class it deliberately lets
# through are pinned separately, because a guard that grows to reject `;` breaks
# NVIDIA's documented multi-value parameter while every refusal still passes.
@test "kubernetes-nodes refuses a hostile kernelModules name or parameter at render" {
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
    cat > "$work/name.yaml" <<'VALS'
kernelModules:
  - name: nvidia$(id)`q`z
VALS
    cat > "$work/param.yaml" <<'VALS'
kernelModules:
  - name: nvidia
    parameters: ["opt=$(id)`q`z"]
VALS
    for f in name param; do
        if helm template kubernetes-nodes-myk8s-md0 packages/apps/kubernetes-nodes -n tenant-test -f "$work/vals.yaml" -f "$work/$f.yaml" \
            --show-only templates/talos-reconcile-job.yaml >/dev/null 2>"$work/err"; then
            echo "render accepted a hostile kernelModules $f, so the value reaches the heredoc" >&2
            rm -rf "$work"; exit 1
        fi
        grep -qF "invalid kernelModules $f" "$work/err" \
            || { echo "render failed on kernelModules $f for some other reason:" >&2; cat "$work/err" >&2; rm -rf "$work"; exit 1; }
    done
    rm -rf "$work"
}

@test "kubernetes-nodes worker TalosConfigTemplate heredoc keeps a semicolon kernelModules parameter literal" {
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
kernelModules:
  - name: nvidia
    parameters: ["NVreg_RegistryDwords=PowerMizerEnable=0x1;PerfLevelSrc=0x2222"]
VALS
    helm template kubernetes-nodes-myk8s-md0 packages/apps/kubernetes-nodes -n tenant-test -f "$work/vals.yaml" \
        --show-only templates/talos-reconcile-job.yaml \
        | yq 'select(.kind == "Job") | .spec.template.spec.containers[0].command[2]' \
        > "$work/cmd.sh"
    [ -s "$work/cmd.sh" ] || { echo "kubernetes-nodes render produced no Job command" >&2; rm -rf "$work"; exit 1; }
    awk '
      # Matched on shape: `cat <<EOF` at the start of a line, or right after
      # `=$(` / `="$(` when the heredoc is captured into a variable.
      /(^|=\"?\$\()cat <<EOF/ { print "cat <<EOF"; inblock=1; next }
      inblock && /^EOF$/            { print "EOF"; inblock=0; next }
      inblock                       { print }
    ' "$work/cmd.sh" > "$work/heredoc.sh"
    grep -q '^cat <<EOF$' "$work/heredoc.sh" || { echo "could not extract the kubernetes-nodes heredoc" >&2; rm -rf "$work"; exit 1; }
    out=$(sh "$work/heredoc.sh" 2>"$work/err") || { echo "kubernetes-nodes heredoc shell exited non-zero" >&2; cat "$work/err" >&2; rm -rf "$work"; exit 1; }
    printf '%s' "$out" | grep -qF -e '- NVreg_RegistryDwords=PowerMizerEnable=0x1;PerfLevelSrc=0x2222' \
        || { echo "kernelModules parameter was not preserved literally" >&2; printf '%s\n' "$out" | grep -A6 'kernel:' >&2; rm -rf "$work"; exit 1; }
    rm -rf "$work"
}

@test "sysctl names and values survive the actual Job heredoc literally without shell execution" {
    TMP=$(mktemp -d)
    cat > "$TMP/values.yaml" <<'VALUES'
talos:
  sysctls:
    user.max_user_namespaces: "11255"
    net.ipv4.ip_local_port_range: "1024\t65535"
    kernel.panic: "0"
    '$(touch "$SYSCTL_MARKER")': '$(touch "$SYSCTL_MARKER")'
    '`touch "$SYSCTL_MARKER"`': '`touch "$SYSCTL_MARKER"`'
    '${SYSCTL_MARKER}': '${SYSCTL_MARKER}'
    'backslash\$(touch "$SYSCTL_MARKER")': 'backslash\$(touch "$SYSCTL_MARKER")'
    "quotes\"\\\nEOF\n$(touch \"$SYSCTL_MARKER\")": "quotes\"\\\nEOF\n$(touch \"$SYSCTL_MARKER\")"
    "unicode\u0085EOF\u2028EOF\u2029": "unicode\u0085EOF\u2028EOF\u2029"
    "empty": ""
VALUES
    render_sysctl_pool "$TMP/r.yaml" -f "$TMP/values.yaml"
    yq -r 'select(.kind == "Job") | .spec.template.spec.containers[0].command[2]' "$TMP/r.yaml" > "$TMP/job.sh"
    # Execute only the real manifest-producing heredoc, never the surrounding
    # kubectl/Talos operations. YAML is parsed on both sides of the shell so
    # escaping that prevents execution but corrupts the machine config also fails.
    awk '/^TCT_MANIFEST=/{emit=1} emit{print} emit && /^\)"$/{exit}' "$TMP/job.sh" > "$TMP/heredoc.sh"
    [ -s "$TMP/heredoc.sh" ]
    printf '\nprintf "%%s\\n" "$TCT_MANIFEST"\n' >> "$TMP/heredoc.sh"
    yq -o=json '.talos.sysctls | sort_keys(.)' "$TMP/values.yaml" > "$TMP/expected.json"
    for shell in bash dash; do
        SYSCTL_MARKER="$TMP/executed" TALOS_TOKEN=runtime-token "$shell" "$TMP/heredoc.sh" > "$TMP/tct.yaml"
        [ ! -e "$TMP/executed" ]
        yq -o=json '.spec.template.spec.data | from_yaml | .machine.sysctls | sort_keys(.)' "$TMP/tct.yaml" > "$TMP/actual.json"
        diff -u "$TMP/expected.json" "$TMP/actual.json"
        [ "$(yq '.spec.template.spec.data | from_yaml | .machine.token' "$TMP/tct.yaml")" = "runtime-token" ]
    done
    rm -rf "$TMP"
}

@test "sysctl types are rejected by both the schema and the render-time guard" {
    TMP=$(mktemp -d)
    for value in '11255' 'false' 'null' '["1"]' '{"evil":"$(id)"}'; do
        input="talos.sysctls={\"kernel.panic\":$value}"
        if render_sysctl_pool "$TMP/r.yaml" --set-json "$input"; then
            echo "FAIL: schema accepted a non-string sysctl: $value"
            false
        fi
        grep -q 'values don.t meet the specifications' "$TMP/r.yaml.err"
        if render_sysctl_pool "$TMP/r.yaml" --skip-schema-validation --set-json "$input"; then
            echo "FAIL: template accepted a non-string sysctl: $value"
            false
        fi
        grep -Fq 'talos.sysctls["kernel.panic"] must be a string' "$TMP/r.yaml.err"
    done
    for value in '"$(id)"' 'false' '0' '[]' '["1"]'; do
        input="talos.sysctls=$value"
        if render_sysctl_pool "$TMP/r.yaml" --set-json "$input"; then
            echo "FAIL: schema accepted a non-map sysctls: $value"
            false
        fi
        grep -q 'values don.t meet the specifications' "$TMP/r.yaml.err"
        if render_sysctl_pool "$TMP/r.yaml" --skip-schema-validation --set-json "$input"; then
            echo "FAIL: template accepted a non-map sysctls: $value"
            false
        fi
        grep -Fq 'talos.sysctls must be a map of strings' "$TMP/r.yaml.err"
    done
    rm -rf "$TMP"
}

@test "sysctl file values reject invalid UTF-8 rather than changing their bytes" {
    TMP=$(mktemp -d)
    # --set-file reads raw bytes, unlike the YAML/JSON-facing application API.
    for bytes in '\377' '\300\257' '\342\202'; do
        printf '%b' "$bytes" > "$TMP/value"
        if render_sysctl_pool "$TMP/r.yaml" --skip-schema-validation --set-file "talos.sysctls.kernel\\.panic=$TMP/value"; then
            echo "FAIL: accepted invalid UTF-8 sysctl bytes: $bytes"
            false
        fi
        grep -Fq 'must be valid UTF-8' "$TMP/r.yaml.err"
    done
    printf '\357\277\275' > "$TMP/value"
    render_sysctl_pool "$TMP/r.yaml" --skip-schema-validation --set-file "talos.sysctls.kernel\\.panic=$TMP/value"
    rm -rf "$TMP"
}
