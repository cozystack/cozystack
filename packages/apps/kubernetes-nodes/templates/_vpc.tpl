{{- /* The apiserver address a reconcile Job writes into its workers' host
       entries. Expects the Job context: .vpc, .probeName, .clusterName,
       .mgmtClusterDomain and .kubectlImage. Sets SVC_IP to the VIP when the
       pool is in a VPC. */}}
{{- define "kubernetes-nodes.vpcApiserverAddress" -}}
{{- /* The render-time network check is skipped when the pool
       renders before the parent's Service exists. */ -}}
if ! PARENT_VPC=$(kubectl -n "$NS" get svc "$RELEASE" -o jsonpath='{.metadata.annotations.sdn\.cozystack\.io/vpc}'); then
  echo "[$(date -u +%H:%M:%S)] cannot read the apiserver Service's network"
  exit 1
fi
if [ "$PARENT_VPC" != {{ .vpc | quote }} ]; then
  echo "[$(date -u +%H:%M:%S)] network.name must match the parent Kubernetes cluster's network: the control plane of ${RELEASE} is in VPC '${PARENT_VPC}', this pool asks for '{{ .vpc }}'"
  exit 1
fi
{{- if .vpc }}

{{- /* Inside the VPC the ClusterIP is unreachable. The workers'
       host entry and the tenant's own kubernetes endpoint (Kamaji's
       advertise address) need the VIP the VPC knows the apiserver
       Service by, and only a pod inside the VPC can learn it, from
       the VPC resolver. The probe resolves the Service name and
       reports the answer as its termination message. */}}
echo "[$(date -u +%H:%M:%S)] resolving the apiserver VIP from inside VPC {{ .vpc }}"
{{- /* Polled with get: --wait=true watches, and the probe Role grants
       no list or watch. */}}
kubectl -n "$NS" delete pod {{ .probeName | quote }} --ignore-not-found --wait=false
STALE_PROBE=""
for i in $(seq 1 60); do
  STALE_PROBE=$(kubectl -n "$NS" get pod {{ .probeName | quote }} --ignore-not-found -o name)
  [ -z "$STALE_PROBE" ] && break
  sleep 2
done
if [ -n "$STALE_PROBE" ]; then
  echo "[$(date -u +%H:%M:%S)] the previous VIP probe pod {{ .probeName }} is still terminating"
  exit 1
fi
kubectl -n "$NS" apply -f - <<'YAML'
apiVersion: v1
kind: Pod
metadata:
  name: {{ .probeName | quote }}
  namespace: {{ $.Release.Namespace | quote }}
  annotations:
    sdn.cozystack.io/vpc: {{ .vpc | quote }}
spec:
  restartPolicy: Never
  automountServiceAccountToken: false
  securityContext:
    runAsNonRoot: true
    runAsUser: 65532
    seccompProfile:
      type: RuntimeDefault
  containers:
    - name: resolve
      image: "{{ .kubectlImage }}"
      command:
        - sh
        - -c
        - |
          for i in $(seq 1 60); do
            ip=$(getent hosts {{ printf "%s.%s.svc.%s" .clusterName $.Release.Namespace .mgmtClusterDomain }} | awk 'NR == 1 { print $1 }')
            if [ -n "$ip" ]; then printf '%s' "$ip" > /dev/termination-log; exit 0; fi
            sleep 5
          done
          exit 1
      resources:
        requests:
          cpu: 10m
          memory: 16Mi
        limits:
          cpu: 50m
          memory: 32Mi
      securityContext:
        allowPrivilegeEscalation: false
        capabilities:
          drop:
            - ALL
        readOnlyRootFilesystem: true
YAML
PROBE_PHASE=""
for i in $(seq 1 90); do
  PROBE_PHASE=$(kubectl -n "$NS" get pod {{ .probeName | quote }} -o jsonpath='{.status.phase}' 2>/dev/null || true)
  case "$PROBE_PHASE" in Succeeded|Failed) break ;; esac
  sleep 5
done
VIP=$(kubectl -n "$NS" get pod {{ .probeName | quote }} -o jsonpath='{.status.containerStatuses[0].state.terminated.message}' 2>/dev/null || true)
kubectl -n "$NS" delete pod {{ .probeName | quote }} --ignore-not-found --wait=false
if [ "$PROBE_PHASE" != "Succeeded" ] || [ -z "$VIP" ]; then
  echo "[$(date -u +%H:%M:%S)] the VPC resolver did not answer for the apiserver Service (probe phase '${PROBE_PHASE}'): is the Service attached to {{ .vpc }}, and is the VPC Ready?"
  exit 1
fi
SVC_IP="$VIP"
# Kamaji passes the advertise address to --advertise-address, the
# tenant's kubernetes endpoints and the konnectivity agents, and
# adds it to the apiserver certificate itself.
kubectl -n "$NS" patch kamajicontrolplane "$RELEASE" --type=merge --patch "{\"spec\":{\"network\":{\"advertiseAddress\":\"${SVC_IP}\"}}}"
echo "[$(date -u +%H:%M:%S)] advertising the apiserver VIP ${SVC_IP}"
{{- end }}
{{- end }}

{{- /* Role rules for the VIP probe pod vpcApiserverAddress creates. */}}
{{- define "kubernetes-nodes.vpcProbeRules" -}}
{{- if .vpc }}
  {{- /* The VIP probe: one named pod the Job creates, reads back and deletes. */}}
  - apiGroups: [""]
    resources: ["pods"]
    resourceNames: [{{ .probeName | quote }}]
    verbs: ["get", "delete"]
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["create"]
{{- end }}
{{- end }}
