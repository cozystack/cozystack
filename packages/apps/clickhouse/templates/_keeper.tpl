{{- define "clickhouse.keeperName" -}}
{{- $lastReplica := sub (int .Values.clickhouseKeeper.replicas) 1 -}}
{{- $name := printf "%s-keeper" .Release.Name -}}
{{- if gt (len (printf "chk-%s-deploy-confd-cluster1-0-%d" $name $lastReplica)) 63 -}}
{{- $suffix := printf "-%s-keeper" (trunc 6 (sha256sum .Release.Name)) -}}
{{- $room := sub 63 (len (printf "chk-%s-deploy-confd-cluster1-0-%d" $suffix $lastReplica)) -}}
{{- $name = printf "%s%s" (trimSuffix "-" (trunc (int $room) .Release.Name)) $suffix -}}
{{- end -}}
{{- $name -}}
{{- end -}}
