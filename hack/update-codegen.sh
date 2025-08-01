#!/usr/bin/env bash

# Copyright 2024 The Cozystack Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_ROOT=$(dirname "${BASH_SOURCE[0]}")/..
CODEGEN_PKG=${CODEGEN_PKG:-$(cd "${SCRIPT_ROOT}"; ls -d -1 ./vendor/k8s.io/code-generator 2>/dev/null || echo ../code-generator)}
API_KNOWN_VIOLATIONS_DIR="${API_KNOWN_VIOLATIONS_DIR:-"${SCRIPT_ROOT}/api/api-rules"}"
UPDATE_API_KNOWN_VIOLATIONS="${UPDATE_API_KNOWN_VIOLATIONS:-true}"
CONTROLLER_GEN="go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.16.4"

source "${CODEGEN_PKG}/kube_codegen.sh"

THIS_PKG="k8s.io/sample-apiserver"

kube::codegen::gen_helpers \
    --boilerplate "${SCRIPT_ROOT}/hack/boilerplate.go.txt" \
    "${SCRIPT_ROOT}/pkg/apis"

kube::codegen::gen_helpers \
    --boilerplate "${SCRIPT_ROOT}/hack/boilerplate.go.txt" \
    "${SCRIPT_ROOT}/api"

if [[ -n "${API_KNOWN_VIOLATIONS_DIR:-}" ]]; then
    report_filename="${API_KNOWN_VIOLATIONS_DIR}/cozystack_api_violation_exceptions.list"
    if [[ "${UPDATE_API_KNOWN_VIOLATIONS:-}" == "true" ]]; then
        update_report="--update-report"
    fi
fi

kube::codegen::gen_openapi \
    --extra-pkgs "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1" \
    --output-dir "${SCRIPT_ROOT}/pkg/generated/openapi" \
    --output-pkg "${THIS_PKG}/pkg/generated/openapi" \
    --report-filename "${report_filename:-"/dev/null"}" \
    ${update_report:+"${update_report}"} \
    --boilerplate "${SCRIPT_ROOT}/hack/boilerplate.go.txt" \
    "${SCRIPT_ROOT}/pkg/apis"

$CONTROLLER_GEN object:headerFile="hack/boilerplate.go.txt" paths="./api/..."
$CONTROLLER_GEN rbac:roleName=manager-role crd paths="./api/..." output:crd:artifacts:config=packages/system/cozystack-controller/templates/crds
