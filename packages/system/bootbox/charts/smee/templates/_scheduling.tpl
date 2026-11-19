{{- define "singleNodeClusterConfig" }}
- effect: NoSchedule
  key: node-role.kubernetes.io/control-plane
{{- end }}

{{- define "preferWorkerNodes" }}
- weight: {{ .nodeAffinityWeight }}
  preference:
    matchExpressions:
    - key: node-role.kubernetes.io/control-plane
      operator: DoesNotExist
{{- end }}
