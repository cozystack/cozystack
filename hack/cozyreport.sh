#!/bin/sh
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
kubectl get cm -n cozy-system --no-headers | awk '$1 ~ /^cozystack/' |
  while read NAME _; do
    DIR=$REPORT_DIR/cozystack/configs
    mkdir -p $DIR
    kubectl get cm -n cozy-system $NAME -o yaml > $DIR/$NAME.yaml 2>&1
  done

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

echo "Collecting helmreleases..."
kubectl get hr -A > $REPORT_DIR/kubernetes/helmreleases.txt 2>&1
kubectl get hr -A --no-headers | awk '$4 != "True"' | \
  while read NAMESPACE NAME _; do
    DIR=$REPORT_DIR/kubernetes/helmreleases/$NAMESPACE/$NAME
    mkdir -p $DIR
    kubectl get hr -n $NAMESPACE $NAME -o yaml > $DIR/hr.yaml 2>&1
    kubectl describe hr -n $NAMESPACE $NAME > $DIR/describe.txt 2>&1
  done

echo "Collecting pods..."
kubectl get pod -A -o wide > $REPORT_DIR/kubernetes/pods.txt 2>&1
kubectl get pod -A --no-headers | awk '$4 !~ /Running|Succeeded|Completed/' |
  while read NAMESPACE NAME _ STATE _; do
    DIR=$REPORT_DIR/kubernetes/pods/$NAMESPACE/$NAME
    mkdir -p $DIR
    CONTAINERS=$(kubectl get pod -o jsonpath='{.spec.containers[*].name}' -n $NAMESPACE $NAME)
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
fi

# -- finalization

echo "Creating archive..."
tar -czf $REPORT_NAME.tgz -C $REPORT_PDIR .
echo "Report created: $REPORT_NAME.tgz"

echo "Cleaning up..."
rm -rf $REPORT_PDIR
