{{/* "<namespace>/<name>" of Kube-OVN's TLS secret, both parts required. */}}
{{- define "proxmox-network.ovnTLSSecret" -}}
{{- $s := .Values.ovn.tlsSecret | default dict -}}
{{- if or (not $s.namespace) (not $s.name) -}}
{{- fail "ovn.tlsSecret.namespace and ovn.tlsSecret.name are required while ovn.manageGatewayChassis is on" -}}
{{- end -}}
{{- printf "%s/%s" $s.namespace $s.name -}}
{{- end -}}
