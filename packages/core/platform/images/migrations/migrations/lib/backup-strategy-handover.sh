#!/bin/sh
# Hands the objects of the single backupstrategy-controller Helm release over
# to the per-strategy releases, before any chart changes. Until Cozystack 1.7
# one release owned every strategy CRD, every default Strategy object, the
# Velero BackupStorageLocation and one Deployment running every driver. Helm
# deletes what a release stops rendering, and deleting a CRD deletes every
# object of its kind, so the objects are protected and re-owned first.
# docs/design.md, "Upgrade of an existing installation", has the reasoning.
#
# Every step is idempotent, and a cluster that never had the single release
# is left untouched. kubectl is taken from $KUBECTL so that a caller outside
# the cluster can pass the context it was told to use.
set -eu

KUBECTL=${KUBECTL:-kubectl}
NAMESPACE=${BACKUP_STRATEGY_NAMESPACE:-cozy-backup-controller}
LEGACY_RELEASE=backupstrategy-controller
VELERO_NAMESPACE=${VELERO_NAMESPACE:-cozy-velero}
GROUP=strategy.backups.cozystack.io
CLASS=cozy-default
LEGACY_FRAGMENT=cozy-default-legacy

# release_for prints the release that renders the CRD and the default objects
# of a strategy resource. The Job strategy stays in the release that had it.
release_for() {
  case "$1" in
    jobs) echo "${LEGACY_RELEASE}" ;;
    cnpgs) echo backup-strategy-cnpg ;;
    mariadbs) echo backup-strategy-mariadb ;;
    mongodbs) echo backup-strategy-mongodb ;;
    etcds) echo backup-strategy-etcd ;;
    foundationdbs) echo backup-strategy-foundationdb ;;
    altinities) echo backup-strategy-altinity ;;
    rabbitmqs) echo backup-strategy-rabbitmq ;;
    redis) echo backup-strategy-redis ;;
    kafkas) echo backup-strategy-kafka ;;
    kubernetes) echo backup-strategy-kubernetes ;;
    veleroes) echo backup-strategy-velero ;;
    *) return 1 ;;
  esac
}

# owned_by_legacy succeeds when the object named by the arguments is rendered
# by the single release. An object an administrator created carries no such
# annotation and is never re-owned.
owned_by_legacy() {
  owner=$(${KUBECTL} get "$@" --ignore-not-found \
    --output=jsonpath='{.metadata.annotations.meta\.helm\.sh/release-name}')
  [ "${owner}" = "${LEGACY_RELEASE}" ]
}

# hand_over protects an object from the release that drops it and lets the
# release named last adopt it. Helm reads the keep policy from the live
# object when an upgrade removes a resource from a release, and refuses to
# adopt an object whose release annotations name another release.
hand_over() {
  release=$1
  shift
  ${KUBECTL} annotate --overwrite "$@" \
    helm.sh/resource-policy=keep \
    "meta.helm.sh/release-name=${release}" \
    "meta.helm.sh/release-namespace=${NAMESPACE}"
}

# The single controller must be gone before a per-strategy controller starts,
# or two controllers reconcile the same job. A Deployment that already runs
# with --strategies is the new base controller and is left alone.
stop_single_controller() {
  args=$(${KUBECTL} --namespace="${NAMESPACE}" get deployment "${LEGACY_RELEASE}" --ignore-not-found \
    --output=jsonpath='{.spec.template.spec.containers[0].args}')
  [ -n "${args}" ] || return 0
  case "${args}" in
    *--strategies=*) return 0 ;;
  esac
  echo "stopping the single backup strategy controller"
  ${KUBECTL} --namespace="${NAMESPACE}" delete deployment "${LEGACY_RELEASE}" --wait=true --timeout=5m
}

hand_over_strategies() {
  crds=$(${KUBECTL} get customresourcedefinitions --output=name | sed -n "s#^customresourcedefinition.apiextensions.k8s.io/\(.*\)\.${GROUP}\$#\1#p")
  for resource in ${crds}; do
    if ! release=$(release_for "${resource}"); then
      echo "leaving ${resource}.${GROUP} alone: no strategy chart renders it"
      continue
    fi
    [ "${release}" != "${LEGACY_RELEASE}" ] || continue

    if owned_by_legacy customresourcedefinition "${resource}.${GROUP}"; then
      echo "handing CRD ${resource}.${GROUP} over to ${release}"
      hand_over "${release}" customresourcedefinition "${resource}.${GROUP}"
    fi
    for object in $(${KUBECTL} get "${resource}.${GROUP}" --output=name); do
      if owned_by_legacy "${object}"; then
        echo "handing ${object} over to ${release}"
        hand_over "${release}" "${object}"
      fi
    done
  done
}

hand_over_storage_location() {
  ${KUBECTL} get customresourcedefinition backupstoragelocations.velero.io --ignore-not-found --output=name | grep -q . || return 0
  if owned_by_legacy --namespace="${VELERO_NAMESPACE}" backupstoragelocations.velero.io "${CLASS}"; then
    echo "handing BackupStorageLocation ${VELERO_NAMESPACE}/${CLASS} over to backup-strategy-velero"
    hand_over backup-strategy-velero --namespace="${VELERO_NAMESPACE}" backupstoragelocations.velero.io "${CLASS}"
  fi
}

# The base chart renders the class without bindings and the strategy charts
# add theirs one release at a time. The copy keeps every binding resolvable
# in between; the base controller deletes it once the other parts of the
# class provide all of them.
keep_class_resolving() {
  ${KUBECTL} get customresourcedefinition backupclasses.backups.cozystack.io --ignore-not-found --output=name | grep -q . || return 0
  owned_by_legacy backupclasses.backups.cozystack.io "${CLASS}" || return 0
  strategies=$(${KUBECTL} get backupclasses.backups.cozystack.io "${CLASS}" --output=jsonpath='{.spec.strategies}')
  case "${strategies}" in
    '' | '[]') return 0 ;;
  esac
  echo "copying the bindings of BackupClass ${CLASS} into ${LEGACY_FRAGMENT}"
  ${KUBECTL} apply --filename=- <<EOF
{
  "apiVersion": "backups.cozystack.io/v1alpha1",
  "kind": "BackupClass",
  "metadata": {
    "name": "${LEGACY_FRAGMENT}",
    "labels": {"backups.cozystack.io/class": "${CLASS}"},
    "annotations": {"backups.cozystack.io/transitional": "true"}
  },
  "spec": {"strategies": ${strategies}}
}
EOF
}

if ! ${KUBECTL} --namespace="${NAMESPACE}" get secrets \
  --selector="owner=helm,name=${LEGACY_RELEASE}" --output=name | grep -q .; then
  echo "no ${LEGACY_RELEASE} release in ${NAMESPACE}: nothing to hand over"
  exit 0
fi

stop_single_controller
keep_class_resolving
hand_over_strategies
hand_over_storage_location
echo "handover complete"
