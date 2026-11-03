#!/usr/bin/env bats

load test_helper

@test "GPU release watch requires readable history and the application retry strategy" {
    tmp=$(mktemp --directory)
    printf '%s\n' '#!/bin/bash' 'if [ "$GPU_HR_ERROR" = true ]; then exit 1; fi' 'cat "$GPU_HR_EVENT"' > "$tmp/kubectl"
    chmod +x "$tmp/kubectl"
    good='{"metadata":{"generation":1},"spec":{"install":{"strategy":{"name":"RetryOnFailure"}}},"status":{"observedGeneration":1,"conditions":[{"type":"Ready","status":"True"}],"history":[{"status":"deployed"}]}}'
    for state in ready superseded pending initial-list stalled failed stale history-failed history-uninstalled missing-history empty-history object-history missing-status null-status empty-status missing-strategy wrong-strategy read-error malformed; do
        event="$good"
        read_error=false
        case "$state" in
            superseded) event=$(printf '%s\n' "$good" | jq --compact-output '.status.history += [{status: "superseded"}]') ;;
            pending) event=$(printf '%s\n' "$good" | jq --compact-output '.status.conditions = [{type: "Ready", status: "False", reason: "Progressing"}] | .status.history = []') ;;
            initial-list) event='{"items":[]}' ;;
            stale) event=$(printf '%s\n' "$good" | jq --compact-output '.status.observedGeneration = 0') ;;
            history-*) event=$(printf '%s\n' "$good" | jq --compact-output --arg state "${state#history-}" '.status.history = [{status: $state}]') ;;
            stalled) event=$(printf '%s\n' "$good" | jq --compact-output '.status.conditions = [{type: "Stalled", status: "True", message: "broken chart"}]') ;;
            failed) event=$(printf '%s\n' "$good" | jq --compact-output '.status.conditions = [{type: "Ready", status: "False", reason: "InstallFailed", message: "broken chart"}]') ;;
            missing-history) event=$(printf '%s\n' "$good" | jq --compact-output 'del(.status.history)') ;;
            empty-history) event=$(printf '%s\n' "$good" | jq --compact-output '.status.history = []') ;;
            object-history) event=$(printf '%s\n' "$good" | jq --compact-output '.status.history = {snapshot: {status: "deployed"}}') ;;
            missing-status) event=$(printf '%s\n' "$good" | jq --compact-output '.status.history = [{}]') ;;
            null-status) event=$(printf '%s\n' "$good" | jq --compact-output '.status.history = [{status: null}]') ;;
            empty-status) event=$(printf '%s\n' "$good" | jq --compact-output '.status.history = [{status: ""}]') ;;
            missing-strategy) event=$(printf '%s\n' "$good" | jq --compact-output 'del(.spec.install.strategy)') ;;
            wrong-strategy) event=$(printf '%s\n' "$good" | jq --compact-output '.spec.install.strategy.name = "RemediateOnFailure"') ;;
            read-error) read_error=true ;;
            malformed) event='{' ;;
        esac
        printf '%s\n' "$event" > "$tmp/event"
        case "$state" in
            pending|initial-list|stalled|failed) printf '%s\n' "$good" >> "$tmp/event" ;;
        esac
        if PATH="$tmp:$PATH" GPU_HR_EVENT="$tmp/event" GPU_HR_ERROR="$read_error" COZY_E2E_CONTEXT=offline KUBECONFIG=/dev/null bash hack/e2e-chainsaw/gpuclass/wait-release.sh test release; then
            case "$state" in ready|superseded|pending|initial-list) ;; *) echo "FAIL: $state was accepted"; false ;; esac
        else
            case "$state" in ready|superseded|pending|initial-list) echo "FAIL: $state was refused"; false ;; esac
        fi
    done
    unlink "$tmp/kubectl"
    unlink "$tmp/event"
    rmdir "$tmp"
}

@test "GPU disk placement intersects every PV affinity term with ready nodes" {
    nodes='{"items":[{"metadata":{"name":"node-a","labels":{"topology.example.com/zone":"a"}},"status":{"conditions":[{"type":"Ready","status":"True"}]}},{"metadata":{"name":"node-b","labels":{"topology.example.com/zone":"b"}},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}'
    volumes='[{"spec":{"nodeAffinity":{"required":{"nodeSelectorTerms":[{"matchExpressions":[{"key":"topology.example.com/zone","operator":"In","values":["b"]}]}]}}}}]'
    output=$(printf '%s\n' "$nodes" | jq --exit-status --raw-output --argjson volumes "$volumes" --from-file hack/e2e-chainsaw/gpuclass/disk-node.jq)
    [ "$output" = node-b ]
    incompatible=$(printf '%s\n' "$volumes" | jq '. + [{spec: {nodeAffinity: {required: {nodeSelectorTerms: [{matchFields: [{key: "metadata.name", operator: "In", values: ["node-a"]}]}]}}}}]')
    if printf '%s\n' "$nodes" | jq --exit-status --raw-output --argjson volumes "$incompatible" --from-file hack/e2e-chainsaw/gpuclass/disk-node.jq; then
        echo "FAIL: disks on incompatible nodes were accepted" >&2
        false
    fi
}

@test "GPU disk placement refuses unschedulable or unready nodes" {
    [ -s hack/e2e-chainsaw/gpuclass/disk-node.jq ]
    for state in cordoned unready; do
        case "$state" in
            cordoned) spec='{"unschedulable":true}'; ready=True ;;
            unready) spec='{}'; ready=False ;;
        esac
        node=$(jq --null-input --argjson spec "$spec" --arg ready "$ready" '{items: [{metadata: {name: "node-a"}, spec: $spec, status: {conditions: [{type: "Ready", status: $ready}]}}]}')
        if printf '%s\n' "$node" | jq --exit-status --raw-output --argjson volumes '[{}]' --from-file hack/e2e-chainsaw/gpuclass/disk-node.jq; then
            echo "FAIL: an unusable GPU node was selected" >&2
            false
        fi
    done
}

@test "GPU disk placement evaluates PV selector operators and OR terms" {
    node='{"items":[{"metadata":{"name":"node-a","labels":{"size":"4"}},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}'
    for operator in In NotIn Exists DoesNotExist Gt Lt; do
        case "$operator" in
            In) yes=4; no=5; key=size ;;
            NotIn) yes=5; no=4; key=size ;;
            Exists) yes=4; no=4; key=size ;;
            DoesNotExist) yes=4; no=4; key=missing ;;
            Gt) yes=3; no=4; key=size ;;
            Lt) yes=5; no=4; key=size ;;
        esac
        volumes=$(jq --null-input --arg op "$operator" --arg key "$key" --arg value "$yes" '[{spec: {nodeAffinity: {required: {nodeSelectorTerms: [{matchExpressions: [{key: $key, operator: $op, values: [$value]}]}]}}}}]')
        [ "$(printf '%s\n' "$node" | jq --exit-status --raw-output --argjson volumes "$volumes" --from-file hack/e2e-chainsaw/gpuclass/disk-node.jq)" = node-a ]
        case "$operator" in
            Exists) bad=$(printf '%s\n' "$volumes" | jq '.[0].spec.nodeAffinity.required.nodeSelectorTerms[0].matchExpressions[0].key = "missing"') ;;
            DoesNotExist) bad=$(printf '%s\n' "$volumes" | jq '.[0].spec.nodeAffinity.required.nodeSelectorTerms[0].matchExpressions[0].key = "size"') ;;
            *) bad=$(printf '%s\n' "$volumes" | jq --arg value "$no" '.[0].spec.nodeAffinity.required.nodeSelectorTerms[0].matchExpressions[0].values = [$value]') ;;
        esac
        if printf '%s\n' "$node" | jq --exit-status --raw-output --argjson volumes "$bad" --from-file hack/e2e-chainsaw/gpuclass/disk-node.jq; then
            echo "FAIL: $operator mismatch was accepted" >&2
            false
        fi
        either=$(jq --null-input --argjson yes "$volumes" --argjson no "$bad" '$yes | .[0].spec.nodeAffinity.required.nodeSelectorTerms += $no[0].spec.nodeAffinity.required.nodeSelectorTerms')
        [ "$(printf '%s\n' "$node" | jq --exit-status --raw-output --argjson volumes "$either" --from-file hack/e2e-chainsaw/gpuclass/disk-node.jq)" = node-a ]
    done
}

@test "GPU disk placement requires every expression and field within a term" {
    nodes='{"items":[{"metadata":{"name":"node-a","labels":{"zone":"a"}},"status":{"conditions":[{"type":"Ready","status":"True"}]}},{"metadata":{"name":"node-b","labels":{"zone":"b","gpu":"true"}},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}'
    volumes='[{"spec":{"nodeAffinity":{"required":{"nodeSelectorTerms":[{"matchExpressions":[{"key":"zone","operator":"In","values":["a","b"]},{"key":"gpu","operator":"Exists"}]}]}}}}]'
    [ "$(printf '%s\n' "$nodes" | jq --exit-status --raw-output --argjson volumes "$volumes" --from-file hack/e2e-chainsaw/gpuclass/disk-node.jq)" = node-b ]
    fields=$(printf '%s\n' "$volumes" | jq '.[0].spec.nodeAffinity.required.nodeSelectorTerms[0] = {matchFields: [{key: "metadata.name", operator: "In", values: ["node-a"]}, {key: "metadata.name", operator: "NotIn", values: ["node-a"]}]}')
    if printf '%s\n' "$nodes" | jq --exit-status --raw-output --argjson volumes "$fields" --from-file hack/e2e-chainsaw/gpuclass/disk-node.jq; then
        echo "FAIL: conflicting PV matchFields were accepted" >&2
        false
    fi
}

@test "GPU disk placement skips nonnumeric labels in numeric comparisons" {
    nodes='{"items":[{"metadata":{"name":"node-a","labels":{"size":"large"}},"status":{"conditions":[{"type":"Ready","status":"True"}]}},{"metadata":{"name":"node-b","labels":{"size":"4"}},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}'
    volumes='[{"spec":{"nodeAffinity":{"required":{"nodeSelectorTerms":[{"matchExpressions":[{"key":"size","operator":"Gt","values":["3"]}]}]}}}}]'
    [ "$(printf '%s\n' "$nodes" | jq --exit-status --raw-output --argjson volumes "$volumes" --from-file hack/e2e-chainsaw/gpuclass/disk-node.jq)" = node-b ]
}

@test "GPU disk numeric affinity uses exact signed int64 comparisons" {
    while IFS='|' read -r operator threshold rejected accepted; do
        nodes=$(jq --null-input --arg rejected "$rejected" --arg accepted "$accepted" '[[$rejected, "node-a"], [$accepted, "node-b"]] | map({metadata: {name: .[1], labels: {size: .[0]}}, status: {conditions: [{type: "Ready", status: "True"}]}}) | {items: .} | if $rejected == "<missing>" then del(.items[0].metadata.labels.size) else . end')
        volumes=$(jq --null-input --arg op "$operator" --arg threshold "$threshold" '[{spec: {nodeAffinity: {required: {nodeSelectorTerms: [{matchExpressions: [{key: "size", operator: $op, values: [$threshold]}]}]}}}}]')
        [ "$(printf '%s\n' "$nodes" | jq --exit-status --raw-output --argjson volumes "$volumes" --from-file hack/e2e-chainsaw/gpuclass/disk-node.jq)" = node-b ]
    done <<'CASES'
Gt|3|4.5|4
Lt|3|2.5|2
Gt|3|4e1|4
Gt|3|0x10|4
Gt|3|4_0|4
Gt|3|9223372036854775808|4
Lt|0|-9223372036854775809|-1
Gt|9007199254740992|9007199254740992|9007199254740993
Gt|9223372036854775806|9223372036854775806|9223372036854775807
Lt|9223372036854775807|9223372036854775807|9223372036854775806
Gt|0003|0003|0004
Gt|-2|-3|-1
Lt|-2|-1|-3
Gt|0|-0|1
Gt|-9223372036854775808|-9223372036854775808|-9223372036854775807
Lt|-9223372036854775807|-9223372036854775807|-9223372036854775808
Gt|+3|+3|+4
Gt|3|<missing>|4
Lt|0|-0|-1
Gt|3|0003|0004
Gt|0003|3|4
Gt|3|+3|+4
CASES
}

@test "GPU suite guards VM install and upgrade retry histories" {
    suite="$PWD/hack/e2e-chainsaw/gpuclass"
    tmp=$(mktemp --directory)
    printf '%s\n' '#!/bin/bash' 'case " $* " in *" --namespace tenant-test-gpu get helmreleases --field-selector metadata.name=vm-instance-gpu --watch "*) cat "$GPU_HR_EVENT" ;; *) exit 1 ;; esac' > "$tmp/kubectl"
    chmod +x "$tmp/kubectl"
    for step in stopped-vm-reserves-the-class-count launcher-workload-reports-the-gpu-units; do
        count=$(STEP="$step" yq '[.spec.steps[] | select(.name == strenv(STEP)) | .try[].script.content // "" | select(contains("wait-release.sh"))] | length' "$suite/chainsaw-test.yaml")
        [ "$count" = 1 ]
        content=$(STEP="$step" yq --unwrapScalar '.spec.steps[] | select(.name == strenv(STEP)) | .try[].script.content // "" | select(contains("wait-release.sh"))' "$suite/chainsaw-test.yaml")
        for state in ready failed; do
            jq --null-input --compact-output --arg state "$state" '{metadata: {generation: 1}, spec: {install: {strategy: {name: "RetryOnFailure"}}}, status: {observedGeneration: 1, conditions: [{type: "Ready", status: "True"}], history: [{status: (if $state == "failed" then "failed" else "deployed" end)}]}}' > "$tmp/event"
            if (cd "$suite" && PATH="$tmp:$PATH" GPU_HR_EVENT="$tmp/event" COZY_E2E_CONTEXT=offline KUBECONFIG=/dev/null sh -c "$content"); then
                [ "$state" = ready ]
            else
                [ "$state" = failed ]
            fi
        done
    done
    unlink "$tmp/kubectl"
    unlink "$tmp/event"
    rmdir "$tmp"
}
