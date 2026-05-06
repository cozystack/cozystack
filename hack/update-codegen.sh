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
# CODEGEN_VERSION must match the literal version pinned in CODEGEN_PKG
# below. The codegen-drift workflow's pre-fetch step greps this file
# for `code-generator@vX.Y.Z` and expects exactly one match, so keep
# the version literal only in CODEGEN_PKG (not duplicated in any
# comment) — bump both lines together when upgrading.
CODEGEN_VERSION=v0.34.1
CODEGEN_PKG=${CODEGEN_PKG:-~/go/pkg/mod/k8s.io/code-generator@v0.34.1}

# Pre-fetch the code-generator module if it is not yet in the local
# module cache. We intentionally do not declare k8s.io/code-generator
# in go.mod — the project does not import any of its packages from Go
# code; only this shell script consumes its kube_codegen.sh. On a
# clean CI runner the cache is empty, so the sourced script below
# would fail with "No such file or directory". Fetch it explicitly
# from a temporary module to keep the project's go.mod clean.
if [ ! -f "${CODEGEN_PKG}/kube_codegen.sh" ]; then
    codegen_tmp=$(mktemp -d)
    (
        cd "${codegen_tmp}"
        go mod init codegen-fetch >/dev/null
        go get "k8s.io/code-generator@${CODEGEN_VERSION}" >/dev/null
    )
    rm -rf "${codegen_tmp}"
fi
API_KNOWN_VIOLATIONS_DIR="${API_KNOWN_VIOLATIONS_DIR:-"${SCRIPT_ROOT}/api/api-rules"}"
UPDATE_API_KNOWN_VIOLATIONS="${UPDATE_API_KNOWN_VIOLATIONS:-true}"
CONTROLLER_GEN="go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.16.4"
TMPDIR=$(mktemp -d)
OPERATOR_CRDDIR=internal/crdinstall/manifests
COZY_CONTROLLER_CRDDIR=packages/system/cozystack-controller/definitions
COZY_RD_CRDDIR=packages/system/application-definition-crd/definition
BACKUPS_CORE_CRDDIR=packages/system/backup-controller/definitions
BACKUPSTRATEGY_CRDDIR=packages/system/backupstrategy-controller/definitions

trap 'rm -rf ${TMPDIR}' EXIT

source "${CODEGEN_PKG}/kube_codegen.sh"

THIS_PKG="github.com/cozystack/cozystack"

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

kube::codegen::gen_client \
    --with-applyconfig \
    --output-dir "${SCRIPT_ROOT}/pkg/generated" \
    --output-pkg "${THIS_PKG}/pkg/generated" \
    --boilerplate "${SCRIPT_ROOT}/hack/boilerplate.go.txt" \
    "${SCRIPT_ROOT}/pkg/apis"

$CONTROLLER_GEN object:headerFile="hack/boilerplate.go.txt" paths="./api/..."
$CONTROLLER_GEN rbac:roleName=manager-role crd paths="./api/v1alpha1/..." paths="./api/backups/..." paths="./api/dashboard/..." paths="./api/gateway/..." output:crd:artifacts:config=${TMPDIR}

mv ${TMPDIR}/cozystack.io_packages.yaml ${OPERATOR_CRDDIR}/cozystack.io_packages.yaml
mv ${TMPDIR}/cozystack.io_packagesources.yaml ${OPERATOR_CRDDIR}/cozystack.io_packagesources.yaml

mv ${TMPDIR}/cozystack.io_applicationdefinitions.yaml \
        ${COZY_RD_CRDDIR}/cozystack.io_applicationdefinitions.yaml

mv ${TMPDIR}/backups.cozystack.io*.yaml ${BACKUPS_CORE_CRDDIR}/
mv ${TMPDIR}/strategy.backups.cozystack.io*.yaml ${BACKUPSTRATEGY_CRDDIR}/

mv ${TMPDIR}/*.yaml ${COZY_CONTROLLER_CRDDIR}/

# Tidy dependencies for standalone api/apps/v1alpha1 submodule
(cd "${SCRIPT_ROOT}/api/apps/v1alpha1" && go mod tidy)

# Generate deepcopy for standalone api/apps/v1alpha1 submodule (separate Go module)
# Use absolute path for headerFile since we cd into the submodule directory
APPS_API_ROOT="$(cd "${SCRIPT_ROOT}" && pwd)"
(cd "${APPS_API_ROOT}/api/apps/v1alpha1" && $CONTROLLER_GEN object:headerFile="${APPS_API_ROOT}/hack/boilerplate.go.txt" paths="./...")

