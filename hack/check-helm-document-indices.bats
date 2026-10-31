#!/usr/bin/env bats

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"

setup() {
    strict_setup
    tmp=$(mktemp -d)
    cd "$tmp"
    git init -q
    mkdir -p packages/apps/example/templates packages/apps/example/tests
    cat > packages/apps/example/templates/multi.yaml <<'EOF'
apiVersion: v1
kind: ConfigMap
metadata:
  name: first
---
apiVersion: v1
kind: Secret
metadata:
  name: second
EOF
    head -n 4 packages/apps/example/templates/multi.yaml > packages/apps/example/templates/single.yaml
    git add .
    git -c user.name=Test -c user.email=test@example.org commit -qm fixture
    base=$(git rev-parse HEAD)
}

teardown() {
    cd "$REPO_ROOT"
    rm -rf "$tmp"
}

write_suite() {
    cat > packages/apps/example/tests/resource_test.yaml <<EOF
suite: resource selection
templates:
  - templates/$1.yaml
tests:
  - it: checks a resource
    documentIndex: 0
    asserts:
      - notExists:
          path: spec.missing
EOF
    git add .
}

@test "rejects a new test-level index on a multi-document template" {
    write_suite multi
    if python3 "$REPO_ROOT/hack/check-helm-document-indices.py" "$base" > output 2>&1; then
        echo "accepted a positional selector on a multi-document template" >&2
        false
    fi
    grep -q 'resource_test.yaml:6: use documentSelector' output
}

@test "allows an index on a single-document template and ignores comments" {
    write_suite single
    printf '\n# documentIndex: 1\n' >> packages/apps/example/tests/resource_test.yaml
    python3 "$REPO_ROOT/hack/check-helm-document-indices.py" "$base"
}

@test "leaves existing indices alone when another assertion changes" {
    write_suite multi
    git -c user.name=Test -c user.email=test@example.org commit -qm existing
    base=$(git rev-parse HEAD)
    sed -i 's/spec.missing/spec.other/' packages/apps/example/tests/resource_test.yaml
    python3 "$REPO_ROOT/hack/check-helm-document-indices.py" "$base"
}

@test "follows assertion template overrides in later YAML suites" {
    write_suite single
    cat >> packages/apps/example/tests/resource_test.yaml <<'EOF'
---
suite: overridden template
templates:
  - templates/single.yaml
tests:
  - it: checks another resource
    asserts:
      - notExists:
          path: spec.missing
        template: templates/multi.yaml
        documentIndex: 1
EOF
    if python3 "$REPO_ROOT/hack/check-helm-document-indices.py" "$base" > output 2>&1; then
        echo "accepted an assertion-level positional selector" >&2
        false
    fi
    grep -q 'use documentSelector.*templates/multi.yaml' output
}

@test "rejects an invalid base rather than passing an empty diff" {
    if python3 "$REPO_ROOT/hack/check-helm-document-indices.py" missing-base > output 2>&1; then
        echo "accepted an invalid base" >&2
        false
    fi
}

@test "test-level indices follow each assertion's template override" {
    write_suite multi
    cat > packages/apps/example/tests/resource_test.yaml <<'EOF'
suite: assertion override
templates: [templates/single.yaml, templates/multi.yaml]
tests:
  - it: checks only the single-document template
    documentIndex: 0
    asserts:
      - template: templates/single.yaml
        notExists:
          path: spec.missing
EOF
    python3 "$REPO_ROOT/hack/check-helm-document-indices.py" "$base"
}

@test "conditional kinds inside one document are not multiple documents" {
    write_suite single
    cat > packages/apps/example/templates/single.yaml <<'EOF'
apiVersion: v1
{{ if .Values.secret }}
kind: Secret
{{ else }}
kind: ConfigMap
{{ end }}
metadata:
  name: single
EOF
    python3 "$REPO_ROOT/hack/check-helm-document-indices.py" "$base"
}

@test "rejects a positional selector whose value is on a separate line" {
    write_suite multi
    sed -i 's/documentIndex: 0/documentIndex:\n      0/' packages/apps/example/tests/resource_test.yaml
    if python3 "$REPO_ROOT/hack/check-helm-document-indices.py" "$base" > output 2>&1; then
        echo "accepted a multiline positional selector" >&2
        false
    fi
    grep -q 'resource_test.yaml:6: use documentSelector' output
}
