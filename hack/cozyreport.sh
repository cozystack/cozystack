#!/bin/sh
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPORT_DATE=$(date +%Y-%m-%d_%H-%M-%S)
REPORT_NAME=${1:-cozyreport-$REPORT_DATE}
REPORT_PDIR=$(mktemp -d)
REPORT_DIR=$REPORT_PDIR/$REPORT_NAME

# -- check dependencies
command -V kubectl >/dev/null || exit $?
command -V tar >/dev/null || exit $?

# -- cozystack module
echo "Collecting Cozystack information..."
mkdir -p $REPORT_DIR/cozystack
kubectl get deploy -n cozy-system cozystack -o jsonpath='{.spec.template.spec.containers[0].image}' > $REPORT_DIR/cozystack/image.txt 2>&1
if kubectl get deploy -n cozy-system cozystack-operator >/dev/null 2>&1; then
  kubectl logs -n cozy-system deploy/cozystack-operator --tail=2000 > $REPORT_DIR/cozystack/operator.log 2>&1
  kubectl logs -n cozy-system deploy/cozystack-operator --tail=2000 --previous > $REPORT_DIR/cozystack/operator-previous.log 2>&1 || true
fi
kubectl get cm -n cozy-system --no-headers | awk '$1 ~ /^cozystack/' |
  while read NAME _; do
    DIR=$REPORT_DIR/cozystack/configs
    mkdir -p $DIR
    kubectl get cm -n cozy-system $NAME -o yaml > $DIR/$NAME.yaml 2>&1
  done

# -- flux module
echo "Collecting Flux controller state..."
mkdir -p $REPORT_DIR/flux
for ctrl in helm-controller source-controller notification-controller kustomize-controller; do
  if kubectl get deploy -n cozy-fluxcd $ctrl >/dev/null 2>&1; then
    kubectl logs -n cozy-fluxcd deploy/$ctrl --tail=2000 > $REPORT_DIR/flux/$ctrl.log 2>&1
    kubectl logs -n cozy-fluxcd deploy/$ctrl --tail=2000 --previous > $REPORT_DIR/flux/$ctrl-previous.log 2>&1 || true
  fi
done

echo "Collecting Flux sources..."
for kind in helmrepositories.source.toolkit.fluxcd.io ocirepositories.source.toolkit.fluxcd.io gitrepositories.source.toolkit.fluxcd.io externalartifacts.source.toolkit.fluxcd.io; do
  short=${kind%%.*}
  kubectl get $kind -A > $REPORT_DIR/flux/$short.txt 2>&1
  kubectl get $kind -A -o yaml > $REPORT_DIR/flux/$short.yaml 2>&1
done

# -- cert-manager module
if kubectl get crd certificates.cert-manager.io >/dev/null 2>&1; then
  echo "Collecting cert-manager state..."
  DIR=$REPORT_DIR/cert-manager
  mkdir -p $DIR
  kubectl get certificates.cert-manager.io -A > $DIR/certificates.txt 2>&1
  kubectl get certificaterequests.cert-manager.io -A > $DIR/certificaterequests.txt 2>&1
  kubectl get orders.acme.cert-manager.io -A > $DIR/orders.txt 2>&1
  kubectl get challenges.acme.cert-manager.io -A > $DIR/challenges.txt 2>&1
  # Per non-Ready cert: full yaml + describe
  kubectl get certificates.cert-manager.io -A --no-headers 2>/dev/null | awk '$3 != "True"' | \
    while read NAMESPACE NAME _; do
      cdir=$DIR/certificates/$NAMESPACE/$NAME
      mkdir -p $cdir
      kubectl get certificates.cert-manager.io -n $NAMESPACE $NAME -o yaml > $cdir/cert.yaml 2>&1
      kubectl describe certificates.cert-manager.io -n $NAMESPACE $NAME > $cdir/describe.txt 2>&1
    done
  if kubectl get deploy -n cozy-cert-manager cert-manager >/dev/null 2>&1; then
    kubectl logs -n cozy-cert-manager deploy/cert-manager --tail=2000 > $DIR/cert-manager.log 2>&1
    kubectl logs -n cozy-cert-manager deploy/cert-manager-webhook --tail=2000 > $DIR/cert-manager-webhook.log 2>&1
  fi
fi

# -- kubernetes module

echo "Collecting Kubernetes information..."
mkdir -p $REPORT_DIR/kubernetes
kubectl version > $REPORT_DIR/kubernetes/version.txt 2>&1

echo "Collecting nodes..."
kubectl get nodes -o wide > $REPORT_DIR/kubernetes/nodes.txt 2>&1
kubectl get nodes --no-headers | awk '$2 != "Ready"' |
  while read NAME _; do
    DIR=$REPORT_DIR/kubernetes/nodes/$NAME
    mkdir -p $DIR
    kubectl get node $NAME -o yaml > $DIR/node.yaml 2>&1
    kubectl describe node $NAME > $DIR/describe.txt 2>&1
  done

echo "Collecting namespaces..."
kubectl get ns -o wide > $REPORT_DIR/kubernetes/namespaces.txt 2>&1
kubectl get ns --no-headers | awk '$2 != "Active"' |
  while read NAME _; do
    DIR=$REPORT_DIR/kubernetes/namespaces/$NAME
    mkdir -p $DIR
    kubectl get ns $NAME -o yaml > $DIR/namespace.yaml 2>&1
    kubectl describe ns $NAME > $DIR/describe.txt 2>&1
  done

echo "Collecting events..."
kubectl get events -A --sort-by=.lastTimestamp > $REPORT_DIR/kubernetes/events.txt 2>&1
# Filter to warning-class and recent for quick triage
kubectl get events -A --sort-by=.lastTimestamp \
  -o jsonpath='{range .items[?(@.type!="Normal")]}{.lastTimestamp}{"\t"}{.involvedObject.namespace}/{.involvedObject.kind}/{.involvedObject.name}{"\t"}{.reason}{"\t"}{.message}{"\n"}{end}' \
  > $REPORT_DIR/kubernetes/events-warnings.txt 2>&1

echo "Collecting helmreleases..."
kubectl get hr -A > $REPORT_DIR/kubernetes/helmreleases.txt 2>&1
kubectl get hr -A --no-headers | awk '$4 != "True"' | \
  while read NAMESPACE NAME _; do
    DIR=$REPORT_DIR/kubernetes/helmreleases/$NAMESPACE/$NAME
    mkdir -p $DIR
    kubectl get hr -n $NAMESPACE $NAME -o yaml > $DIR/hr.yaml 2>&1
    kubectl describe hr -n $NAMESPACE $NAME > $DIR/describe.txt 2>&1
    # Helm storage secrets: latest revision per release.
    kubectl get secret -n $NAMESPACE -l owner=helm,name=$NAME --sort-by='.metadata.creationTimestamp' --no-headers 2>/dev/null | \
      tail -1 | awk '{print $1}' | while read SECRET; do
        [ -z "$SECRET" ] && continue
        kubectl get secret -n $NAMESPACE $SECRET -o jsonpath='{.data.release}' 2>/dev/null \
          | base64 -d | base64 -d | gzip -d > $DIR/helm-release.json 2>&1 || true
      done
  done

echo "Collecting packages..."
kubectl get packages > $REPORT_DIR/kubernetes/packages.txt 2>&1
kubectl get packages --no-headers | awk '$3 != "True"' | \
  while read NAME _; do
    DIR=$REPORT_DIR/kubernetes/packages/$NAME
    mkdir -p $DIR
    kubectl get package $NAME -o yaml > $DIR/package.yaml 2>&1
    kubectl describe package $NAME > $DIR/describe.txt 2>&1
  done

echo "Collecting packagesources..."
kubectl get packagesources > $REPORT_DIR/kubernetes/packagesources.txt 2>&1
kubectl get packagesources --no-headers | awk '$3 != "True"' | \
  while read NAME _; do
    DIR=$REPORT_DIR/kubernetes/packagesources/$NAME
    mkdir -p $DIR
    kubectl get packagesource $NAME -o yaml > $DIR/packagesource.yaml 2>&1
    kubectl describe packagesource $NAME > $DIR/describe.txt 2>&1
  done

echo "Collecting cozystack apps..."
DIR=$REPORT_DIR/cozystack-apps
mkdir -p $DIR
for kind in applications.apps.cozystack.io applicationdefinitions.apps.cozystack.io tenants.apps.cozystack.io; do
  short=${kind%%.*}
  if kubectl get crd $kind >/dev/null 2>&1; then
    kubectl get $kind -A > $DIR/$short.txt 2>&1
    kubectl get $kind -A -o jsonpath='{range .items[?(@.status.conditions[?(@.type=="Ready")].status!="True")]}{.metadata.namespace}{" "}{.metadata.name}{"\n"}{end}' 2>/dev/null | \
      while read NAMESPACE NAME; do
        [ -z "$NAMESPACE" ] && continue
        d=$DIR/$short/$NAMESPACE/$NAME
        mkdir -p $d
        kubectl get $kind -n $NAMESPACE $NAME -o yaml > $d/$short.yaml 2>&1
        kubectl describe $kind -n $NAMESPACE $NAME > $d/describe.txt 2>&1
      done
  fi
done

echo "Collecting pods..."
kubectl get pod -A -o wide > $REPORT_DIR/kubernetes/pods.txt 2>&1
kubectl get pod -A --no-headers | awk '$4 !~ /Running|Succeeded|Completed/' |
  while read NAMESPACE NAME _ STATE _; do
    DIR=$REPORT_DIR/kubernetes/pods/$NAMESPACE/$NAME
    mkdir -p $DIR
    CONTAINERS=$(kubectl get pod -o jsonpath='{.spec.containers[*].name} {.spec.initContainers[*].name}' -n $NAMESPACE $NAME)
    kubectl get pod -n $NAMESPACE $NAME -o yaml > $DIR/pod.yaml 2>&1
    kubectl describe pod -n $NAMESPACE $NAME > $DIR/describe.txt 2>&1
    if [ "$STATE" != "Pending" ]; then
      for CONTAINER in $CONTAINERS; do
        kubectl logs -n $NAMESPACE $NAME $CONTAINER > $DIR/logs-$CONTAINER.txt 2>&1
        kubectl logs -n $NAMESPACE $NAME $CONTAINER --previous > $DIR/logs-$CONTAINER-previous.txt 2>&1
      done
    fi
  done

echo "Collecting virtualmachines..."
kubectl get vm -A > $REPORT_DIR/kubernetes/vms.txt 2>&1
kubectl get vm -A --no-headers | awk '$5 != "True"' |
  while read NAMESPACE NAME _; do
    DIR=$REPORT_DIR/kubernetes/vm/$NAMESPACE/$NAME
    mkdir -p $DIR
    kubectl get vm -n $NAMESPACE $NAME -o yaml > $DIR/vm.yaml 2>&1
    kubectl describe vm -n $NAMESPACE $NAME > $DIR/describe.txt 2>&1
  done

echo "Collecting virtualmachine instances..."
kubectl get vmi -A > $REPORT_DIR/kubernetes/vmis.txt 2>&1
kubectl get vmi -A --no-headers | awk '$4 != "Running"' |
  while read NAMESPACE NAME _; do
    DIR=$REPORT_DIR/kubernetes/vmi/$NAMESPACE/$NAME
    mkdir -p $DIR
    kubectl get vmi -n $NAMESPACE $NAME -o yaml > $DIR/vmi.yaml 2>&1
    kubectl describe vmi -n $NAMESPACE $NAME > $DIR/describe.txt 2>&1
  done

echo "Collecting services..."
kubectl get svc -A > $REPORT_DIR/kubernetes/services.txt 2>&1
kubectl get svc -A --no-headers | awk '$4 == "<pending>"' |
  while read NAMESPACE NAME _; do
    DIR=$REPORT_DIR/kubernetes/services/$NAMESPACE/$NAME
    mkdir -p $DIR
    kubectl get svc -n $NAMESPACE $NAME -o yaml > $DIR/service.yaml 2>&1
    kubectl describe svc -n $NAMESPACE $NAME > $DIR/describe.txt 2>&1
  done

echo "Collecting pvcs..."
kubectl get pvc -A > $REPORT_DIR/kubernetes/pvcs.txt 2>&1
kubectl get pvc -A --no-headers | awk '$3 != "Bound"'  |
  while read NAMESPACE NAME _; do
    DIR=$REPORT_DIR/kubernetes/pvc/$NAMESPACE/$NAME
    mkdir -p $DIR
    kubectl get pvc -n $NAMESPACE $NAME -o yaml > $DIR/pvc.yaml 2>&1
    kubectl describe pvc -n $NAMESPACE $NAME > $DIR/describe.txt 2>&1
  done

# -- objectstorage (COSI) module

if kubectl get crd bucketclaims.objectstorage.k8s.io >/dev/null 2>&1; then
  echo "Collecting objectstorage (COSI) state..."
  DIR=$REPORT_DIR/objectstorage
  mkdir -p $DIR
  # The COSI CRDs ship no printer columns, so plain `kubectl get` shows
  # only NAME/AGE — pull the readiness fields explicitly.
  kubectl get bucketclaims.objectstorage.k8s.io -A \
    -o custom-columns='NAMESPACE:.metadata.namespace,NAME:.metadata.name,READY:.status.bucketReady,BUCKET:.status.bucketName,CLASS:.spec.bucketClassName' \
    > $DIR/bucketclaims.txt 2>&1
  kubectl get bucketaccesses.objectstorage.k8s.io -A \
    -o custom-columns='NAMESPACE:.metadata.namespace,NAME:.metadata.name,GRANTED:.status.accessGranted,ACCOUNT:.status.accountID,CLAIM:.spec.bucketClaimName' \
    > $DIR/bucketaccesses.txt 2>&1
  kubectl get buckets.objectstorage.k8s.io \
    -o custom-columns='NAME:.metadata.name,READY:.status.bucketReady,ID:.status.bucketID,CLAIMNS:.spec.bucketClaim.namespace,CLAIM:.spec.bucketClaim.name' \
    > $DIR/buckets.txt 2>&1
  for kind in bucketclaims.objectstorage.k8s.io bucketaccesses.objectstorage.k8s.io; do
    short=${kind%%.*}
    kubectl get $kind -A -o yaml > $DIR/$short.yaml 2>&1
  done
  for kind in buckets.objectstorage.k8s.io bucketclasses.objectstorage.k8s.io bucketaccessclasses.objectstorage.k8s.io; do
    short=${kind%%.*}
    kubectl get $kind -o yaml > $DIR/$short.yaml 2>&1
  done
  if kubectl get deploy -n cozy-objectstorage-controller container-object-storage-controller >/dev/null 2>&1; then
    kubectl logs -n cozy-objectstorage-controller deploy/container-object-storage-controller --tail=2000 > $DIR/objectstorage-controller.log 2>&1
    kubectl logs -n cozy-objectstorage-controller deploy/container-object-storage-controller --tail=2000 --previous > $DIR/objectstorage-controller-previous.log 2>&1 || true
  fi
  # seaweedfs COSI provisioners run per seaweedfs instance, one per namespace
  kubectl get deploy -A --no-headers 2>/dev/null | awk '$2 ~ /objectstorage-provisioner$/ {print $1" "$2}' |
    while read NAMESPACE NAME; do
      kubectl logs -n $NAMESPACE deploy/$NAME --all-containers --tail=2000 > $DIR/provisioner-$NAMESPACE.log 2>&1
      kubectl logs -n $NAMESPACE deploy/$NAME --all-containers --tail=2000 --previous > $DIR/provisioner-$NAMESPACE-previous.log 2>&1 || true
    done
fi

# -- kamaji module

if kubectl get deploy -n cozy-linstor linstor-controller >/dev/null 2>&1; then
  echo "Collecting kamaji resources..."
  DIR=$REPORT_DIR/kamaji
  mkdir -p $DIR
  kubectl logs -n cozy-kamaji deployment/kamaji > $DIR/kamaji-controller.log 2>&1
  kubectl get kamajicontrolplanes.controlplane.cluster.x-k8s.io -A > $DIR/kamajicontrolplanes.txt 2>&1
  kubectl get kamajicontrolplanes.controlplane.cluster.x-k8s.io -A -o yaml > $DIR/kamajicontrolplanes.yaml 2>&1
  kubectl get tenantcontrolplanes.kamaji.clastix.io -A > $DIR/tenantcontrolplanes.txt 2>&1
  kubectl get tenantcontrolplanes.kamaji.clastix.io -A -o yaml > $DIR/tenantcontrolplanes.yaml 2>&1
fi

# -- linstor module

if kubectl get deploy -n cozy-linstor linstor-controller >/dev/null 2>&1; then
  echo "Collecting linstor resources..."
  DIR=$REPORT_DIR/linstor
  mkdir -p $DIR
  kubectl exec -n cozy-linstor deploy/linstor-controller -- linstor --no-color n l > $DIR/nodes.txt 2>&1
  kubectl exec -n cozy-linstor deploy/linstor-controller -- linstor --no-color sp l > $DIR/storage-pools.txt 2>&1
  kubectl exec -n cozy-linstor deploy/linstor-controller -- linstor --no-color r l > $DIR/resources.txt 2>&1
  # Cluster-wide ErrorReport index (IDs + timestamps + node + category)
  # for fast triage before diving into the per-satellite bundles below.
  kubectl exec -n cozy-linstor deploy/linstor-controller -- linstor --no-color error-reports list > $DIR/error-reports-index.txt 2>&1 || true

  # Controller-side ErrorReports live on the controller pod at
  # /var/log/linstor-controller/ and cover autoplace decisions, RPC
  # errors, and controller-JVM exceptions the index above only
  # references by ID. Bundle them the same way as the satellite ones
  # below so both ends of the storage stack are recoverable offline.
  kubectl -n cozy-linstor exec deploy/linstor-controller --container=linstor-controller -- sh -c '
    cd /var/log/linstor-controller 2>/dev/null || exit 0
    tar -czf - ErrorReport-*.log 2>/dev/null || true
  ' > "$DIR/controller-error-reports.tgz" 2>/dev/null || true
  # Drop the bundle if empty. `tar -czf - <no-match>` produces a valid
  # 45-byte gzipped empty archive (extracts cleanly, `tar -tzf` exits
  # 0), so a plain readability check keeps that stub in the tree.
  # Require the archive to contain at least one member to keep it.
  if [ ! -s "$DIR/controller-error-reports.tgz" ] || [ -z "$(tar -tzf "$DIR/controller-error-reports.tgz" 2>/dev/null | head -n 1)" ]; then
    rm -f "$DIR/controller-error-reports.tgz"
  fi

  # LINSTOR satellite ErrorReports carry the actual storage-driver error
  # text (the linstor-Satellite log only references them by ID:
  # `ERROR ... Failed to create zfsvolume [Report number 6A4A394E-...]`).
  # crust-gather ships only pod stdout, so without this capture the
  # report body stays on the satellite ephemeral filesystem and is lost
  # when the sandbox is torn down. Copy them off every satellite pod so
  # post-mortem of a `CreateVolume ResourceExhausted` or
  # `Failed to create zfsvolume` retry loop has the concrete cause
  # (out-of-space, dataset conflict, kernel error) instead of just the
  # driver-level symptom.
  DIR=$REPORT_DIR/linstor/error-reports
  mkdir -p "$DIR"
  for pod in $(kubectl -n cozy-linstor get pods -l app.kubernetes.io/component=linstor-satellite -o name 2>/dev/null); do
    # Read the node name directly off the pod spec so the tarball key
    # is stable across piraeus DaemonSet regenerations. Fallback to the
    # pod name if .spec.nodeName is not readable for any reason (last
    # resort; collisions between DS pod generations are theoretically
    # possible but the bundle would still land in a distinct file).
    node=$(kubectl -n cozy-linstor get "$pod" -o jsonpath='{.spec.nodeName}' 2>/dev/null)
    [ -z "$node" ] && node=$(basename "$pod")
    # Tar the ErrorReport-*.log files into a per-satellite bundle so a
    # burst of retry-loop reports (dozens per incident) does not explode
    # the artefact tree, and a missing directory or empty set never
    # fails the whole cozyreport run.
    kubectl -n cozy-linstor exec "$pod" --container=linstor-satellite -- sh -c '
      cd /var/log/linstor-satellite 2>/dev/null || exit 0
      tar -czf - ErrorReport-*.log 2>/dev/null || true
    ' > "$DIR/$node.tgz" 2>/dev/null || true
    # Drop the bundle if empty. `tar -czf - <no-match>` yields a valid
    # 45-byte gzipped empty archive that would otherwise slip past a
    # readability check. Require at least one member to keep it. A
    # satellite with zero ErrorReports (healthy case) leaves nothing.
    if [ ! -s "$DIR/$node.tgz" ] || [ -z "$(tar -tzf "$DIR/$node.tgz" 2>/dev/null | head -n 1)" ]; then
      rm -f "$DIR/$node.tgz"
    fi
  done
  # Drop the empty parent directory when no satellite had reports.
  rmdir "$DIR" 2>/dev/null || true
fi

# -- sandbox-host module

echo "Collecting sandbox host context..."
DIR=$REPORT_DIR/sandbox-host
mkdir -p $DIR
df -h > $DIR/df.txt 2>&1
free -m > $DIR/free.txt 2>&1
ps auxww > $DIR/ps.txt 2>&1
dmesg | tail -200 > $DIR/dmesg.txt 2>&1 || true
if [ -f /workspace/talosconfig ]; then
  NODES=$(kubectl get nodes -o jsonpath='{range .items[*]}{.status.addresses[?(@.type=="InternalIP")].address}{"\n"}{end}' 2>/dev/null)
  for node in ${NODES:-192.168.123.11 192.168.123.12 192.168.123.13}; do
    [ -z "$node" ] && continue
    talosctl --talosconfig /workspace/talosconfig -n "$node" dmesg --tail=200 > "$DIR/talos-$node-dmesg.txt" 2>&1 || true
    talosctl --talosconfig /workspace/talosconfig -n "$node" logs kubelet --tail=500 > "$DIR/talos-$node-kubelet.log" 2>&1 || true
    talosctl --talosconfig /workspace/talosconfig -n "$node" logs containerd --tail=500 > "$DIR/talos-$node-containerd.log" 2>&1 || true
  done
fi

# -- finalization

echo "Generating summary..."
"$SCRIPT_DIR/cozyreport-summary.sh" > "$REPORT_DIR/summary.txt" 2>&1 || true

# Fold in the per-test crust-gather snapshots cozytest.sh captured on failure
# (host + each nested tenant cluster) so the uploaded artifact carries an
# inspectable, `crust-gather serve`-able state for every failed test.
SNAP_DIR="${COZY_REPORT_DIR:-_out/cozyreport}/snapshots"
[ -d "$SNAP_DIR" ] && cp -a "$SNAP_DIR" "$REPORT_DIR/snapshots" 2>/dev/null || true

echo "Creating archive..."
tar -czf $REPORT_NAME.tgz -C $REPORT_PDIR .
echo "Report created: $REPORT_NAME.tgz"

echo "Cleaning up..."
rm -rf $REPORT_PDIR
