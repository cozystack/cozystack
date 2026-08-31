#!/bin/bash
# E2E-only proof that a freshly granted COSI BucketAccess is already usable by
# the S3 server. accessGranted describes the COSI object state; it does not
# prove that the backing server has reloaded the new IAM identity.

cozy_backup_access_preflight() (
  set -euo pipefail

  local namespace="$1"
  local access_name="$2"
  local timeout_seconds="${3:-90}"
  local s3_namespace="${COZY_E2E_S3_NAMESPACE:-tenant-root}"
  local s3_service="${COZY_E2E_S3_SERVICE:-seaweedfs-s3}"
  local local_port="${COZY_E2E_S3_PREFLIGHT_PORT:-18333}"
  local workdir pf_pid= uploaded=false
  local access_key secret_key bucket deadline attempt object

  case "${timeout_seconds}:${local_port}" in
    *[!0-9:]*)
      echo "backup access preflight timeout and local port must be integers" >&2
      return 1
      ;;
  esac

  workdir=$(mktemp -d)
  cleanup() {
    local rc=$?
    trap - EXIT
    if [ -n "${pf_pid}" ] && kill -0 "${pf_pid}" 2>/dev/null; then
      if ! kill "${pf_pid}" 2>/dev/null; then
        echo "WARNING: could not stop S3 preflight port-forward process ${pf_pid}" >&2
      fi
      # wait reports the SIGTERM used above as a non-zero child status. That is
      # the expected port-forward shutdown, not a preflight failure.
      if ! wait "${pf_pid}" 2>/dev/null; then
        :
      fi
    fi
    if [ "${uploaded}" = true ]; then
      if ! timeout 15 mc rm --insecure "backup-preflight/${bucket}/${object}" >/dev/null 2>&1; then
        echo "WARNING: could not remove failed S3 preflight object ${bucket}/${object}" >&2
      fi
    fi
    rm -rf -- "${workdir}"
    exit "${rc}"
  }
  trap cleanup EXIT

  kubectl -n "${namespace}" get secret "${access_name}" \
    -o jsonpath='{.data.BucketInfo}' | base64 -d > "${workdir}/bucket-info.json"
  access_key=$(jq -r '.spec.secretS3.accessKeyID // ""' "${workdir}/bucket-info.json")
  secret_key=$(jq -r '.spec.secretS3.accessSecretKey // ""' "${workdir}/bucket-info.json")
  bucket=$(jq -r '.spec.bucketName // ""' "${workdir}/bucket-info.json")
  if [ -z "${access_key}" ] || [ -z "${secret_key}" ] || [ -z "${bucket}" ]; then
    echo "BucketInfo Secret ${namespace}/${access_name} lacks access key, secret key, or bucket name" >&2
    return 1
  fi

  echo "Checking that BucketAccess ${namespace}/${access_name} is live in S3 before provisioning the backup workload..."
  kubectl -n "${s3_namespace}" port-forward \
    "service/${s3_service}" "${local_port}:8333" >"${workdir}/port-forward.log" 2>&1 &
  pf_pid=$!
  if ! timeout 30 sh -ec "until nc -z 127.0.0.1 ${local_port}; do sleep 1; done"; then
    echo "S3 preflight port-forward did not become ready" >&2
    sed 's/^/  port-forward: /' "${workdir}/port-forward.log" >&2
    return 1
  fi

  export MC_CONFIG_DIR="${workdir}/mc"
  mc alias set backup-preflight "https://127.0.0.1:${local_port}" \
    "${access_key}" "${secret_key}" --insecure >/dev/null

  object=".cozystack-e2e-preflight/${access_name}-$(date +%s)-$$"
  printf 'cozystack backup access preflight: %s\n' "${access_name}" > "${workdir}/source"
  deadline=$(( $(date +%s) + timeout_seconds ))
  attempt=0
  while :; do
    attempt=$(( attempt + 1 ))
    : > "${workdir}/mc.log"
    if timeout 15 mc cp --insecure "${workdir}/source" \
        "backup-preflight/${bucket}/${object}" >"${workdir}/mc.log" 2>&1; then
      uploaded=true
      if timeout 15 mc cp --insecure "backup-preflight/${bucket}/${object}" \
          "${workdir}/download" >>"${workdir}/mc.log" 2>&1 \
        && cmp "${workdir}/source" "${workdir}/download" \
        && timeout 15 mc rm --insecure "backup-preflight/${bucket}/${object}" \
          >>"${workdir}/mc.log" 2>&1; then
        uploaded=false
        echo "BucketAccess ${namespace}/${access_name} passed PUT, GET, compare, and DELETE on attempt ${attempt}."
        return 0
      fi
    fi

    if [ "$(date +%s)" -ge "${deadline}" ]; then
      echo "BucketAccess ${namespace}/${access_name} was granted but did not pass S3 PUT/GET/DELETE within ${timeout_seconds}s" >&2
      sed 's/^/  s3-preflight: /' "${workdir}/mc.log" >&2
      return 1
    fi
    sleep 5
  done
)
